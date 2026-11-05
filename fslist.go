package main

// The PC Folders tab's own folder picker (MPC TOOLS): lists this computer's folders for the settings page, so choosing
// a folder never depends on a system dialog that Windows may open behind other windows.

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type fsEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"` // "drive" or "place" on the start list
}

type fsList struct {
	Dir     string    `json:"dir"`    // "" = the start list (drives and common places)
	Parent  string    `json:"parent"` // "" = back to the start list
	Entries []fsEntry `json:"entries"`
}

var skipDirs = map[string]bool{"$recycle.bin": true, "system volume information": true, "$windows.~bt": true,
	"$windows.~ws": true, "recovery": true, "config.msi": true, "msocache": true, "programdata": true,
	"windows": true, "program files": true, "program files (x86)": true, "appdata": true, "perflogs": true}

func listFolders(dir string) (fsList, error) {
	if dir == "" {
		return startList(), nil
	}
	dir = filepath.Clean(dir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return fsList{}, err
	}
	out := fsList{Dir: dir}
	if p := filepath.Dir(dir); p != dir {
		out.Parent = p
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "$") || skipDirs[strings.ToLower(n)] {
			continue
		}
		full := filepath.Join(dir, n)
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 { // a link to a folder
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			out.Entries = append(out.Entries, fsEntry{Name: n, Path: full})
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool { return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name) })
	return out, nil
}

func startList() fsList {
	out := fsList{}
	if home, err := os.UserHomeDir(); err == nil {
		for _, n := range []string{"Desktop", "Music", "Documents", "Downloads", "Videos"} {
			p := filepath.Join(home, n)
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				out.Entries = append(out.Entries, fsEntry{Name: n, Path: p, Kind: "place"})
			}
		}
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			p := string(c) + `:\`
			if _, err := os.Stat(p); err == nil {
				out.Entries = append(out.Entries, fsEntry{Name: string(c) + ":", Path: p, Kind: "drive"})
			}
		}
	} else {
		out.Entries = append(out.Entries, fsEntry{Name: "/", Path: "/", Kind: "drive"})
	}
	return out
}
