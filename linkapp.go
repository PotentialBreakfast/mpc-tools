// MPC Link, built into MPC TOOLS. This file is MPC Link's cmd/mpc-link/app.go (Padbangers, MIT - see
// linkcore/LICENSE-mpc-link), adapted: MPC TOOLS chooses the listener, there is no tray menu, and the link starts and
// stops with the MPC TOOLS window. The packages under linkcore/ are MPC Link's, unchanged apart from import paths and
// pick.Folder opening in front of other windows.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"

	"mpctools/linkcore/assets"
	"mpctools/linkcore/config"
	"mpctools/linkcore/discover"
	"mpctools/linkcore/mpc"
	"mpctools/linkcore/pick"
	"mpctools/linkcore/web"
)

// App ties the settings, the link to the MPC and the settings page together.
type LinkApp struct {
	mu      sync.Mutex
	cfg     *config.Config
	link    *mpc.Link
	enabled bool   // the user wants to be connected (Connect / Disconnect)
	token   string // guards the settings page: only this computer's browser, opened from the menu, gets it
	url     string
	onState func(mpc.Status)
}

func newLinkApp(cfg *config.Config) *LinkApp {
	b := make([]byte, 16)
	rand.Read(b)
	a := &LinkApp{cfg: cfg, enabled: true, token: hex.EncodeToString(b)}
	a.link = &mpc.Link{OnState: func(s mpc.Status) {
		if a.onState != nil {
			a.onState(s)
		}
	}}
	return a
}

func (a *LinkApp) name() string { return computerName(a.cfg) }

// restart (re)connects with the current settings, or disconnects when the user switched the link off.
func (a *LinkApp) restart() {
	a.link.Stop()
	if a.enabled && a.cfg.Host != "" && len(a.cfg.Folders) > 0 {
		a.link.Start(a.cfg, a.name(), func() { a.cfg.Save() })
	}
}

func (a *LinkApp) setEnabled(on bool) {
	a.mu.Lock()
	a.enabled = on
	a.mu.Unlock()
	a.restart()
}

// serve starts the settings page on a random local port.
func (a *LinkApp) serve(ln net.Listener) error {
	a.url = "http://" + ln.Addr().String() + "/?t=" + a.token
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != a.token {
			http.Error(w, "open the settings from the MPC Link menu", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(web.Page))
	})
	mux.HandleFunc("/banner.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(assets.Banner)
	})
	api := func(path string, h func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != a.token || r.Method != http.MethodPost {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			v, err := h(r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(v)
		})
	}
	api("/api/state", func(*http.Request) (any, error) { return a.state(), nil })
	api("/api/settings", func(r *http.Request) (any, error) {
		var in struct {
			Host         string `json:"host"`
			ComputerName string `json:"computer_name"`
			Drive        string `json:"drive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if in.Host != a.cfg.Host {
			a.cfg.HostKey = "" // another MPC: learn its key
		}
		a.cfg.Host, a.cfg.ComputerName, a.cfg.Drive = in.Host, in.ComputerName, in.Drive
		err := a.cfg.Save()
		a.mu.Unlock()
		a.restart()
		return a.state(), err
	})
	api("/api/folders/add", func(*http.Request) (any, error) {
		p, err := pick.Folder("Choose a folder to show on the MPC")
		if err == pick.ErrCancelled {
			return a.state(), nil
		}
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		name := uniqueName(a.cfg.Folders, config.SafeName(filepath.Base(p)))
		a.cfg.Folders = append(a.cfg.Folders, config.Folder{Name: name, Path: p})
		err = a.cfg.Save()
		a.mu.Unlock()
		a.restart()
		return a.state(), err
	})
	api("/api/folders/remove", func(r *http.Request) (any, error) {
		i, _ := strconv.Atoi(r.URL.Query().Get("i"))
		a.mu.Lock()
		if i >= 0 && i < len(a.cfg.Folders) {
			a.cfg.Folders = append(a.cfg.Folders[:i], a.cfg.Folders[i+1:]...)
		}
		err := a.cfg.Save()
		a.mu.Unlock()
		a.restart()
		return a.state(), err
	})
	api("/api/folders/rename", func(r *http.Request) (any, error) {
		i, _ := strconv.Atoi(r.URL.Query().Get("i"))
		name := config.SafeName(r.URL.Query().Get("name"))
		a.mu.Lock()
		if i >= 0 && i < len(a.cfg.Folders) {
			others := append(append([]config.Folder{}, a.cfg.Folders[:i]...), a.cfg.Folders[i+1:]...)
			a.cfg.Folders[i].Name = uniqueName(others, name)
		}
		err := a.cfg.Save()
		a.mu.Unlock()
		a.restart()
		return a.state(), err
	})
	api("/api/discover", func(*http.Request) (any, error) { return discover.Scan(), nil })
	api("/api/connect", func(*http.Request) (any, error) { a.setEnabled(true); return a.state(), nil })
	api("/api/disconnect", func(*http.Request) (any, error) { a.setEnabled(false); return a.state(), nil })
	go http.Serve(ln, mux)
	return nil
}

type state struct {
	Host         string          `json:"host"`
	ComputerName string          `json:"computer_name"`
	Drive        string          `json:"drive"`
	Name         string          `json:"name"`
	Folders      []config.Folder `json:"folders"`
	Enabled      bool            `json:"enabled"`
	Status       mpc.Status      `json:"status"`
	Version      string          `json:"version"`
}

func (a *LinkApp) state() state {
	a.mu.Lock()
	defer a.mu.Unlock()
	return state{Host: a.cfg.Host, ComputerName: a.cfg.ComputerName, Drive: a.cfg.Drive, Name: a.name(),
		Folders: append([]config.Folder{}, a.cfg.Folders...), Enabled: a.enabled, Status: a.link.Status(), Version: version}
}

func uniqueName(folders []config.Folder, name string) string {
	taken := map[string]bool{}
	for _, f := range folders {
		taken[f.Name] = true
	}
	out := name
	for i := 2; taken[out]; i++ {
		out = name + " " + strconv.Itoa(i)
	}
	return out
}

const version = "1.0.2 (in MPC TOOLS)"
