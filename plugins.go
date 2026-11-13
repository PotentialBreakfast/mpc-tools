package main

// The Plugins tab (1.4.0): the PotentialSuite plugins on the MPC, in one window instead of PowerShell lines.
//   - which version each plugin is on the MPC, and the newest on GitHub
//   - UPDATE ALL: the newest release of every plugin into "VST2 Plugins" (as scripts/update-potential-plugins.ps1)
//   - RESTART MPC APP: so the new versions load
//   - TEST: Open MPC's CPU bench on each plugin (it also loads and runs it: the load test), on a Gen1 MPC
//   - RECORD ON GITHUB: runs each plugin repo's "Mark tested" workflow with the bench figure
//   - NEW VERSION: runs a plugin repo's "New version" workflow (bump, notes, release)
//   - PLUGIN FOLDERS: PotentialBreaks' and PotentialWax's folder lists and the SAVE KIT folder, kept on this PC and
//     written to the MPC (folders.txt / kit-folder.txt on its drives)
// GitHub needs a token for the workflows (pasted once, kept in settings.ini); everything else works without one.

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed scripts/bench/vstbench-armhf
var vstbench []byte

const ghOwner = "PotentialBreakfast"

type plugin struct {
	ID   string `json:"id"`   // potentialheat
	Name string `json:"name"` // PotentialHeat
}

var suite = []plugin{
	{"potentialbreaks", "PotentialBreaks"}, {"potentialheat", "PotentialHeat"}, {"potentialnoise", "PotentialNoise"},
	{"potentialpatina", "PotentialPatina"}, {"potentialpunch", "PotentialPunch"}, {"potentialwarp", "PotentialWarp"},
	{"potentialwax", "PotentialWax"},
}

func pluginByID(id string) (plugin, bool) {
	for _, p := range suite {
		if p.ID == id {
			return p, true
		}
	}
	return plugin{}, false
}

// ---------- running things on the MPC ----------

// mpcRun sends a shell script to the MPC on stdin (no quoting through ssh's command line) and returns its output.
func mpcRun(script string, timeout time.Duration) (string, error) {
	if getHost() == "" {
		return "", errors.New("set your MPC's address first (the ⚙ button)")
	}
	var out, errBuf bytes.Buffer
	c := exec.Command(sshPath, sshArgs(user+"@"+getHost(), "sh -s")...)
	c.Stdin = strings.NewReader(strings.ReplaceAll(script, "\r", ""))
	c.Stdout, c.Stderr = &out, &errBuf
	hidden(c)
	if err := c.Start(); err != nil {
		return "", fmt.Errorf("couldn't start ssh: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if _, ok := err.(*exec.ExitError); ok && out.Len() > 0 {
				return out.String(), nil // the script's own exit status; its output says what happened
			}
			return out.String(), fmt.Errorf("the MPC didn't answer: %s", strings.TrimSpace(errBuf.String()+" "+err.Error()))
		}
		return out.String(), nil
	case <-time.After(timeout):
		c.Process.Kill()
		return out.String(), errors.New("the MPC took too long to answer")
	}
}

// mpcPut copies a local file or folder to the MPC (scp -r).
func mpcPut(local, remote string) error {
	var errBuf bytes.Buffer
	c := exec.Command(sshPath2("scp"), append(sshArgs("-q", "-r"), local, user+"@"+getHost()+":"+remote)...)
	c.Stderr = &errBuf
	hidden(c)
	if err := c.Run(); err != nil {
		return fmt.Errorf("copying to the MPC failed: %s", strings.TrimSpace(errBuf.String()+" "+err.Error()))
	}
	return nil
}

// scp sits next to ssh (Windows OpenSSH): if ssh= in the settings points at one, use the scp beside it.
func sshPath2(name string) string {
	if sshPath == "ssh" || sshPath == "" {
		return name
	}
	p := filepath.Join(filepath.Dir(sshPath), name+filepath.Ext(sshPath))
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return name
}

// shq quotes a string for sh: 'it'\”s'
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ---------- GitHub ----------

var gh = &http.Client{Timeout: 30 * time.Second}

func ghToken() string { return strings.TrimSpace(getSetting("github_token")) }

func ghDo(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "https://api.github.com"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "MPC-TOOLS/"+appVersion)
	if t := ghToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := gh.Do(req)
	if err != nil {
		return 0, fmt.Errorf("couldn't reach GitHub: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if out != nil && resp.StatusCode < 300 && len(data) > 0 {
		json.Unmarshal(data, out)
	}
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("GitHub said %d: %s", resp.StatusCode, e.Message)
	}
	return resp.StatusCode, nil
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

var (
	relMu    sync.Mutex
	relCache = map[string]release{}
	relTime  time.Time
)

// latestReleases: each plugin's newest release (kept for 2 minutes).
func latestReleases(fresh bool) (map[string]release, error) {
	relMu.Lock()
	defer relMu.Unlock()
	if !fresh && time.Since(relTime) < 2*time.Minute && len(relCache) == len(suite) {
		return relCache, nil
	}
	out := map[string]release{}
	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range suite {
		wg.Add(1)
		go func(p plugin) {
			defer wg.Done()
			var r release
			_, err := ghDo("GET", "/repos/"+ghOwner+"/mpc-vst-"+p.ID+"/releases/latest", nil, &r)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if err == nil {
				out[p.ID] = r
			}
		}(p)
	}
	wg.Wait()
	if len(out) > 0 {
		relCache, relTime = out, time.Now()
	}
	return out, firstErr
}

func dispatch(id, workflow string, inputs map[string]string) error {
	if ghToken() == "" {
		return errors.New("connect GitHub first (the GitHub button on this tab)")
	}
	_, err := ghDo("POST", "/repos/"+ghOwner+"/mpc-vst-"+id+"/actions/workflows/"+workflow+"/dispatches",
		map[string]any{"ref": "main", "inputs": inputs}, nil)
	return err
}

// ---------- one job at a time, with a log the page polls ----------

type jobState struct {
	mu     sync.Mutex
	Name   string   `json:"name"`
	State  string   `json:"state"` // idle, running, done, error
	Log    []string `json:"log"`
	Bench  []bench  `json:"bench,omitempty"`
	Finish string   `json:"finish"` // the last line, for the page's summary
}

var job = &jobState{State: "idle"}

func (j *jobState) say(f string, a ...any) {
	j.mu.Lock()
	j.Log = append(j.Log, fmt.Sprintf(f, a...))
	j.mu.Unlock()
}

func (j *jobState) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]any{"name": j.Name, "state": j.State, "log": append([]string(nil), j.Log...), "bench": j.Bench, "finish": j.Finish}
}

// start runs fn as the job; false if one is already running.
func (j *jobState) start(name string, fn func() (string, error)) bool {
	j.mu.Lock()
	if j.State == "running" {
		j.mu.Unlock()
		return false
	}
	j.Name, j.State, j.Log, j.Finish = name, "running", nil, ""
	if name == "test" {
		j.Bench = nil
	}
	j.mu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				j.mu.Lock()
				j.State, j.Finish = "error", fmt.Sprint("Stopped: ", r)
				j.mu.Unlock()
			}
		}()
		msg, err := fn()
		j.mu.Lock()
		if err != nil {
			j.State, j.Finish = "error", "Stopped: "+err.Error()
			j.Log = append(j.Log, j.Finish)
		} else {
			j.State, j.Finish = "done", msg
			if msg != "" {
				j.Log = append(j.Log, msg)
			}
		}
		j.mu.Unlock()
		relMu.Lock()
		relTime = time.Time{} // the versions may have changed: ask GitHub again next time
		relMu.Unlock()
	}()
	return true
}

// ---------- what's on the MPC ----------

type installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Folder  string `json:"folder"`
}

type mpcInfo struct {
	Plugins map[string]installed `json:"plugins"` // by id
	Arch    string               `json:"arch"`
	Drive   string               `json:"drive"` // where updates go: the drive with VST2 Plugins
	Drives  []string             `json:"drives"`
}

const scanScript = `
for d in /media/*/"VST2 Plugins"/*" - VST - Potential"*; do
  [ -d "$d" ] || continue
  v=$(sed -n 's/.*"version": *"\([0-9.]*\)".*/\1/p' "$d/plugin.json" "$d/mpc-plugin.json" 2>/dev/null | head -n 1)
  echo "P|${d##* - VST - }|$v|$d"
done
echo "A|$(uname -m)"
for d in /media/*/"VST2 Plugins"/"PotentialBreaks - VST - "*; do [ -d "$d" ] && { d="${d%/VST2 Plugins/*}"; echo "V|$d"; break; }; done
for d in /media/*/"VST2 Plugins"; do [ -d "$d" ] && echo "W|${d%/VST2 Plugins}"; done
for d in /media/*; do [ -d "$d" ] && echo "D|$d"; done
true
`

func scanMPC() (mpcInfo, error) {
	out, err := mpcRun(scanScript, 20*time.Second)
	info := mpcInfo{Plugins: map[string]installed{}}
	if err != nil {
		return info, err
	}
	first := ""
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(l), "|", 4)
		switch f[0] {
		case "P":
			if len(f) == 4 {
				id := strings.ToLower(f[1])
				if _, ok := pluginByID(id); ok {
					if cur, have := info.Plugins[id]; !have || newer(f[2], cur.Version) {
						info.Plugins[id] = installed{Name: f[1], Version: f[2], Folder: f[3]}
					}
				}
			}
		case "A":
			if len(f) > 1 {
				info.Arch = f[1]
			}
		case "V":
			if len(f) > 1 && info.Drive == "" {
				info.Drive = f[1]
			}
		case "W":
			if len(f) > 1 && first == "" {
				first = f[1]
			}
		case "D":
			if len(f) > 1 {
				info.Drives = append(info.Drives, f[1])
			}
		}
	}
	if info.Drive == "" {
		info.Drive = first
	}
	return info, nil
}

// newer: is version a later than b? ("1.10.0" > "1.9.2")
func newer(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// ---------- UPDATE ALL ----------

var folderName = regexp.MustCompile(`^[A-Za-z0-9 ]+ - VST - [A-Za-z0-9 ]+$`)
var safeDrive = regexp.MustCompile(`^/media/[A-Za-z0-9_.-]+$`)

func updateAll(ids []string) (string, error) {
	j := job
	j.say("Looking at the MPC…")
	info, err := scanMPC()
	if err != nil {
		return "", err
	}
	if info.Drive == "" {
		return "", errors.New("there's no \"VST2 Plugins\" folder on the MPC's drives. Make one at the top of your USB stick or SD card, then try again")
	}
	if !safeDrive.MatchString(info.Drive) {
		return "", fmt.Errorf("unexpected drive name on the MPC: %s", info.Drive)
	}
	j.say("Plugins go to: %s/VST2 Plugins", info.Drive)
	rels, err := latestReleases(true)
	if err != nil && len(rels) == 0 {
		return "", err
	}
	work, err := os.MkdirTemp("", "mpctools-update-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	type got struct{ id, folder, local, version string }
	var todo []got
	for _, p := range suite {
		if len(ids) > 0 && !contains(ids, p.ID) {
			continue
		}
		r, ok := rels[p.ID]
		if !ok {
			j.say("  %s: no release found on GitHub, skipped", p.Name)
			continue
		}
		v := strings.TrimPrefix(r.Tag, "v")
		if cur, have := info.Plugins[p.ID]; have && cur.Version == v && len(ids) == 0 {
			j.say("  %s %s: up to date", p.Name, v)
			continue
		}
		url := ""
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, "-mpc-sdk.zip") {
				url = a.URL
			}
		}
		if url == "" {
			j.say("  %s %s: no MPC OS 3.10.1 download in this release, skipped", p.Name, v)
			continue
		}
		j.say("  downloading %s %s…", p.Name, v)
		dir := filepath.Join(work, p.ID)
		folder, err := downloadAndUnzip(url, dir)
		if err != nil {
			return "", fmt.Errorf("%s: %v", p.Name, err)
		}
		if !folderName.MatchString(folder) {
			return "", fmt.Errorf("%s: unexpected folder in the download: %s", p.Name, folder)
		}
		todo = append(todo, got{p.ID, folder, filepath.Join(dir, folder), v})
	}
	if len(todo) == 0 {
		return "Everything is up to date.", nil
	}
	stage := info.Drive + "/potential-update"
	if _, err := mpcRun("rm -rf "+shq(stage)+"; mkdir -p "+shq(stage), 20*time.Second); err != nil {
		return "", err
	}
	for _, t := range todo {
		j.say("  copying %s to the MPC…", t.folder)
		if err := mpcPut(t.local, stage+"/"+t.id); err != nil {
			return "", err
		}
	}
	var sb strings.Builder
	sb.WriteString("D=" + shq(info.Drive) + "\nV=\"$D/VST2 Plugins\"\nS=" + shq(stage) + "\n")
	sb.WriteString(`B="$D/Potential-backup-$(date +%Y%m%d-%H%M)"
moved=0
swap() {
  [ -d "$S/$1" ] || { echo "missing upload: $2"; return; }
  if [ -d "$V/$2" ]; then k="${V#/}"; k="${k#media/}"; mkdir -p "$B/$k"; mv "$V/$2" "$B/$k/"; moved=1; fi
  mv "$S/$1" "$V/$2"
  for s in /media/*/Synths /sdcard/Synths /media/*/"VST2 Plugins"; do
    [ "$s" = "$V" ] && continue
    if [ -d "$s/$2" ]; then k="${s#/}"; k="${k#media/}"; mkdir -p "$B/$k"; mv "$s/$2" "$B/$k/"; moved=1; echo "old copy moved out of $s: $2"; fi
  done
  v=$(sed -n 's/.*"version": *"\([0-9.]*\)".*/\1/p' "$V/$2/plugin.json" | head -n 1)
  echo "installed: $2 $v"
}
`)
	for _, t := range todo {
		sb.WriteString("swap " + shq(t.id) + " " + shq(t.folder) + "\n")
	}
	sb.WriteString("rmdir \"$S\" 2>/dev/null\nsync\n[ $moved = 1 ] && echo \"BACKUP:$B\"\ntrue\n")
	out, err := mpcRun(sb.String(), 2*time.Minute)
	if err != nil {
		return "", err
	}
	backup := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(l, "BACKUP:") {
			backup = strings.TrimPrefix(l, "BACKUP:")
		} else if l != "" {
			j.say("  %s", l)
		}
	}
	if backup != "" {
		j.say("Old copies were moved to %s (delete that folder once everything works).", backup)
	}
	return fmt.Sprintf("Updated %d plugin%s. Tap RESTART MPC APP so the new versions load (save your project first).", len(todo), plural(len(todo))), nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func downloadAndUnzip(url, dir string) (string, error) {
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Get(url)
	if err != nil {
		return "", fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("the download isn't a zip: %v", err)
	}
	top := ""
	for _, f := range zr.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), "/")
		if strings.Contains(name, "..") {
			return "", fmt.Errorf("unsafe path in the zip: %s", f.Name)
		}
		if i := strings.Index(name, "/"); i > 0 && top == "" {
			top = name[:i]
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if f.FileInfo().IsDir() {
			os.MkdirAll(dst, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		w, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		w.Close()
		if err != nil {
			return "", err
		}
	}
	if top == "" {
		return "", errors.New("the zip has no plugin folder")
	}
	return top, nil
}

// ---------- RESTART MPC APP ----------

func restartMPCApp() (string, error) {
	job.say("Restarting the MPC app…")
	out, err := mpcRun(`systemctl restart acvs; i=0; while [ $i -lt 40 ]; do systemctl is-active -q acvs && break; sleep 2; i=$((i+1)); done; systemctl is-active acvs`, 2*time.Minute)
	if err != nil {
		return "", err
	}
	if !strings.Contains(out, "active") || strings.Contains(out, "inactive") {
		return "", fmt.Errorf("the MPC app didn't come back: %s", strings.TrimSpace(out))
	}
	return "The MPC app is restarting: give it half a minute. The new versions are loaded when you add the plugins again.", nil
}

// ---------- TEST (bench + load) ----------

type bench struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Line    string  `json:"line"` // the bench's JSON line, as recorded
	Verdict string  `json:"verdict"`
	P99     float64 `json:"p99"`
	Max     float64 `json:"max"`
	OpenMs  float64 `json:"open_ms"`
	Err     string  `json:"err,omitempty"`
}

var benchLine = regexp.MustCompile(`^\{.*"verdict".*\}$`)

func testAll() (string, error) {
	j := job
	j.say("Looking at the MPC…")
	info, err := scanMPC()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(info.Arch, "arm") || strings.HasPrefix(info.Arch, "arm64") {
		return "", fmt.Errorf("this MPC is %s (a Gen2): the bench is for Gen1 MPCs (armv7), where the catalog's figure is measured", info.Arch)
	}
	if len(info.Plugins) == 0 {
		return "", errors.New("no Potential plugins found in \"VST2 Plugins\" on the MPC")
	}
	tmp, err := os.CreateTemp("", "vstbench-")
	if err != nil {
		return "", err
	}
	tmp.Write(vstbench)
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := mpcPut(tmp.Name(), "/tmp/vstbench"); err != nil {
		return "", err
	}
	ids := make([]string, 0, len(info.Plugins))
	for id := range info.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var results []bench
	for _, id := range ids {
		p, _ := pluginByID(id)
		in := info.Plugins[id]
		j.say("  %s %s: benching (about half a minute)…", p.Name, in.Version)
		script := "d=" + shq(in.Folder) + `
chmod +x /tmp/vstbench
so=$(sed -n 's/.*"file32": *"\([^"]*\)".*/\1/p' "$d/plugin.json" | head -n 1)
[ -n "$so" ] && [ -f "$d/$so" ] || { echo "NO32"; exit 0; }
cp "$d/$so" /tmp/benchplug.so
taskset 2 /tmp/vstbench /tmp/benchplug.so -j 2>&1 | tail -n 12
rm -f /tmp/benchplug.so
`
		out, err := mpcRun(script, 3*time.Minute)
		b := bench{ID: id, Name: p.Name, Version: in.Version}
		switch {
		case err != nil:
			b.Err = err.Error()
		case strings.Contains(out, "NO32"):
			b.Err = "no 32-bit build in the plugin folder"
		default:
			for _, l := range strings.Split(out, "\n") {
				l = strings.TrimSpace(l)
				if benchLine.MatchString(l) {
					var m map[string]any
					if json.Unmarshal([]byte(l), &m) == nil {
						b.Line = l
						b.Verdict, _ = m["verdict"].(string)
						b.P99, _ = m["p99_pct"].(float64)
						b.Max, _ = m["max_pct"].(float64)
						b.OpenMs, _ = m["open_ms"].(float64)
					}
				}
			}
			if b.Line == "" {
				b.Err = "the bench gave no result (the plugin may not have loaded): " + lastLines(out, 3)
			}
		}
		if b.Err != "" {
			j.say("    %s", b.Err)
		} else {
			j.say("    %s: loads in %.1f ms, %.1f%% of an audio block (worst %.1f%%)", b.Verdict, b.OpenMs, b.P99, b.Max)
		}
		results = append(results, b)
		j.mu.Lock()
		j.Bench = append([]bench(nil), results...)
		j.mu.Unlock()
	}
	mpcRun("rm -f /tmp/vstbench /tmp/benchplug.so", 15*time.Second)
	saveBenchFile(results)
	ok := 0
	for _, b := range results {
		if b.Err == "" && b.Verdict != "FAIL" {
			ok++
		}
	}
	return fmt.Sprintf("%d of %d plugins load and run within the MPC's limits. Play them on the MPC; if they work, tap RECORD ON GITHUB.", ok, len(results)), nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " / ")
}

// the results go to Potential-bench.txt on the Desktop too, as the PowerShell bench did
func saveBenchFile(results []bench) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	var sb strings.Builder
	sb.WriteString("# MPC TOOLS bench, " + time.Now().Format("2006-01-02 15:04") + "\r\n")
	for _, b := range results {
		if b.Line != "" {
			sb.WriteString("== " + b.Name + " " + b.Version + "\r\n" + b.Line + "\r\n")
		} else {
			sb.WriteString("== " + b.Name + " " + b.Version + ": " + b.Err + "\r\n")
		}
	}
	os.WriteFile(filepath.Join(home, "Desktop", "Potential-bench.txt"), []byte(sb.String()), 0o644)
}

// ---------- RECORD ON GITHUB ----------

func recordTested(device, firmware string) (string, error) {
	j := job
	j.mu.Lock()
	results := append([]bench(nil), j.Bench...)
	j.mu.Unlock()
	if len(results) == 0 {
		return "", errors.New("run TEST first")
	}
	if ghToken() == "" {
		return "", errors.New("connect GitHub first (the GitHub button on this tab)")
	}
	n := 0
	for _, b := range results {
		if b.Err != "" || b.Verdict == "FAIL" || b.Version == "" {
			j.say("  %s: not recorded (%s)", b.Name, firstNonEmpty(b.Err, "the bench said FAIL"))
			continue
		}
		if err := dispatch(b.ID, "mark-tested.yml", map[string]string{"version": b.Version, "device": device,
			"firmware": firmware, "bench": b.Line}); err != nil {
			j.say("  %s: %v", b.Name, err)
			continue
		}
		j.say("  %s %s: recorded", b.Name, b.Version)
		n++
	}
	if n == 0 {
		return "", errors.New("nothing was recorded")
	}
	return fmt.Sprintf("Sent to GitHub for %d plugin%s: each repo adds the tested entry and the CPU figure in a minute or so.", n, plural(n)), nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---------- PLUGIN FOLDERS ----------

type pluginFolders struct {
	Breaks []string `json:"breaks"`
	Wax    []string `json:"wax"`
	Kit    string   `json:"kit"`
}

func foldersPath() string { return filepath.Join(filepath.Dir(settingsPath()), "plugin-folders.json") }

func savedFolders() (pluginFolders, bool) {
	var f pluginFolders
	b, err := os.ReadFile(foldersPath())
	if err != nil {
		return f, false
	}
	return f, json.Unmarshal(b, &f) == nil
}

const readFoldersScript = `
for d in /media/*; do
  [ -d "$d" ] || continue
  for f in PotentialBreaks/folders.txt PotentialWax/folders.txt PotentialBreaks/kit-folder.txt PotentialWax/kit-folder.txt; do
    [ -f "$d/$f" ] || continue
    echo "F|$d|$f"
    sed -e 's/\r$//' -e 's/^/L|/' "$d/$f"
  done
done
true
`

// readFolders: what the MPC's lists say now (full paths; a line without a / is a folder on that file's drive).
func readFolders() (pluginFolders, error) {
	out, err := mpcRun(readFoldersScript, 20*time.Second)
	res := pluginFolders{Breaks: []string{}, Wax: []string{}}
	if err != nil {
		return res, err
	}
	drive, file := "", ""
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, "F|") {
			f := strings.SplitN(l, "|", 3)
			if len(f) == 3 {
				drive, file = f[1], f[2]
			}
			continue
		}
		if !strings.HasPrefix(l, "L|") {
			continue
		}
		t := strings.Trim(strings.TrimSpace(l[2:]), `"`)
		t = strings.TrimRight(t, "/")
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if !strings.HasPrefix(t, "/") {
			t = drive + "/" + t
		}
		switch file {
		case "PotentialBreaks/folders.txt":
			if !seen["b"+t] {
				res.Breaks, seen["b"+t] = append(res.Breaks, t), true
			}
		case "PotentialWax/folders.txt":
			if !seen["w"+t] {
				res.Wax, seen["w"+t] = append(res.Wax, t), true
			}
		case "PotentialBreaks/kit-folder.txt", "PotentialWax/kit-folder.txt":
			if res.Kit == "" {
				res.Kit = t
			}
		}
	}
	return res, nil
}

var badPath = regexp.MustCompile(`[\x00-\x1f]`)

// writeFolders: each list into one folders.txt (the first drive that already has one, else the drive with VST2
// Plugins), the other drives' copies cut to their notes (lines with #), and the kit folder into kit-folder.txt on every
// drive (both plugins read it). The plugins' own instructions at the top of a file are kept.
func writeFolders(f pluginFolders, home string) (string, error) {
	for _, p := range append(append([]string{f.Kit}, f.Breaks...), f.Wax...) {
		if p != "" && (!strings.HasPrefix(p, "/media/") || badPath.MatchString(p) || strings.Contains(p, "/../")) {
			return "", fmt.Errorf("not a folder on the MPC's drives: %s", p)
		}
	}
	if home == "" || !safeDrive.MatchString(home) {
		return "", errors.New("no drive on the MPC to write the lists to")
	}
	list := func(l []string) string {
		var sb strings.Builder
		for _, p := range l {
			sb.WriteString(p + "\n")
		}
		return sb.String()
	}
	var sb strings.Builder
	sb.WriteString("H=" + shq(home) + "\n")
	sb.WriteString(`put() {   # put <plugin dir> <list>: one file with the list, the others notes only
  P=$1; L=$2; T=""
  for d in /media/*; do [ -f "$d/$P/folders.txt" ] && { T="$d"; break; }; done
  [ -n "$T" ] || T="$H"
  for d in /media/*; do
    f="$d/$P/folders.txt"
    if [ "$d" = "$T" ]; then
      mkdir -p "$d/$P"
      { [ -f "$f" ] && grep '^#' "$f" | grep -v 'kept by MPC TOOLS'; echo "# (kept by MPC TOOLS on the PC: change the list there)"; printf '%s' "$L"; } > "$f.new" && mv "$f.new" "$f"
      echo "wrote $f"
    elif [ -f "$f" ]; then
      grep '^#' "$f" > "$f.new"; mv "$f.new" "$f"; echo "notes only now: $f"
    fi
  done
}
`)
	sb.WriteString("put PotentialBreaks " + shq(list(f.Breaks)) + "\n")
	sb.WriteString("put PotentialWax " + shq(list(f.Wax)) + "\n")
	sb.WriteString("K=" + shq(f.Kit) + "\n")
	sb.WriteString(`for d in /media/*; do
  [ -d "$d" ] || continue
  case "${d##*/}" in az01*|acvs*) continue;; esac
  for P in PotentialBreaks PotentialWax; do
    if [ -n "$K" ]; then
      [ "$P" = PotentialWax ] && [ ! -d "$d/$P" ] && continue
      mkdir -p "$d/$P" 2>/dev/null && printf '%s\n' "$K" > "$d/$P/kit-folder.txt" && echo "kit folder set on ${d##*/} ($P)"
    else
      [ -f "$d/$P/kit-folder.txt" ] && rm -f "$d/$P/kit-folder.txt" && echo "kit folder back to the default on ${d##*/} ($P)"
    fi
  done
done
sync
true
`)
	out, err := mpcRun(sb.String(), 30*time.Second)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			job.say("  %s", l)
		}
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	os.MkdirAll(filepath.Dir(foldersPath()), 0o755)
	os.WriteFile(foldersPath(), b, 0o644)
	return "Saved on this PC and written to the MPC. Tap RESCAN in PotentialBreaks and PotentialWax to see the folders.", nil
}

// listMPCDirs: the folders inside dir on the MPC ("" = the drives), for the folder picker.
func listMPCDirs(dir string) ([]string, error) {
	if dir == "" {
		dir = "/media"
	}
	if !strings.HasPrefix(dir, "/media") || badPath.MatchString(dir) || strings.Contains(dir, "/..") {
		return nil, errors.New("only the MPC's drives can be browsed")
	}
	out, err := mpcRun("cd "+shq(dir)+" 2>/dev/null || exit 0\nfor f in * .[!.]*; do [ -d \"$f\" ] && echo \"$f\"; done; true", 15*time.Second)
	if err != nil {
		return nil, err
	}
	var l []string
	for _, n := range strings.Split(out, "\n") {
		n = strings.TrimSpace(n)
		if n == "" || strings.HasPrefix(n, ".") || n == "lost+found" || n == "System Volume Information" {
			continue
		}
		l = append(l, n)
	}
	sort.Slice(l, func(a, b int) bool { return strings.ToLower(l[a]) < strings.ToLower(l[b]) })
	return l, nil
}

// ---------- HTTP ----------

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func postOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return false
	}
	return true
}

func registerPlugins(mux *http.ServeMux) {
	mux.HandleFunc("/plugins", func(w http.ResponseWriter, r *http.Request) {
		touch()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(pluginsPage)
	})
	mux.HandleFunc("/api/plugins/status", func(w http.ResponseWriter, r *http.Request) {
		touch()
		info, mErr := scanMPC()
		rels, gErr := latestReleases(r.URL.Query().Get("fresh") == "1")
		type row struct {
			plugin
			Installed string `json:"installed"`
			Latest    string `json:"latest"`
			State     string `json:"state"` // ok, old, missing, unknown
		}
		var rows []row
		for _, p := range suite {
			x := row{plugin: p}
			if in, ok := info.Plugins[p.ID]; ok {
				x.Installed = in.Version
			}
			if rel, ok := rels[p.ID]; ok {
				x.Latest = strings.TrimPrefix(rel.Tag, "v")
			}
			switch {
			case x.Installed == "" && mErr == nil:
				x.State = "missing"
			case x.Installed == "" || x.Latest == "":
				x.State = "unknown"
			case newer(x.Latest, x.Installed):
				x.State = "old"
			default:
				x.State = "ok"
			}
			rows = append(rows, x)
		}
		res := map[string]any{"plugins": rows, "arch": info.Arch, "drive": info.Drive, "github": ghToken() != "",
			"githubUser": getSetting("github_user"),
			"device":     firstNonEmpty(getSetting("device"), "MPC One"), "firmware": firstNonEmpty(getSetting("firmware"), "MPC OS 3.10.1")}
		if mErr != nil {
			res["mpcError"] = mErr.Error()
		}
		if gErr != nil {
			res["githubError"] = gErr.Error()
		}
		jsonOut(w, res)
	})
	mux.HandleFunc("/api/plugins/job", func(w http.ResponseWriter, r *http.Request) { touch(); jsonOut(w, job.snapshot()) })
	startJob := func(w http.ResponseWriter, name string, fn func() (string, error)) {
		if !job.start(name, fn) {
			http.Error(w, "Something is still running: wait for it to finish.", 409)
			return
		}
		w.WriteHeader(204)
	}
	mux.HandleFunc("/api/plugins/update", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if !postOnly(w, r) {
			return
		}
		var ids []string
		if id := r.URL.Query().Get("id"); id != "" {
			if _, ok := pluginByID(id); !ok {
				http.Error(w, "no such plugin", 400)
				return
			}
			ids = []string{id}
		}
		startJob(w, "update", func() (string, error) { return updateAll(ids) })
	})
	mux.HandleFunc("/api/plugins/restart", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if postOnly(w, r) {
			startJob(w, "restart", restartMPCApp)
		}
	})
	mux.HandleFunc("/api/plugins/test", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if postOnly(w, r) {
			startJob(w, "test", testAll)
		}
	})
	mux.HandleFunc("/api/plugins/record", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if !postOnly(w, r) {
			return
		}
		dev := strings.TrimSpace(r.URL.Query().Get("device"))
		fw := strings.TrimSpace(r.URL.Query().Get("firmware"))
		if dev == "" || fw == "" || len(dev) > 60 || len(fw) > 60 {
			http.Error(w, "say which device and firmware", 400)
			return
		}
		setSetting("device", dev)
		setSetting("firmware", fw)
		startJob(w, "record", func() (string, error) { return recordTested(dev, fw) })
	})
	mux.HandleFunc("/api/plugins/newversion", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if !postOnly(w, r) {
			return
		}
		var req struct{ ID, Bump, Notes string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		p, ok := pluginByID(req.ID)
		if !ok || (req.Bump != "patch" && req.Bump != "minor" && req.Bump != "major") {
			http.Error(w, "bad request", 400)
			return
		}
		startJob(w, "newversion", func() (string, error) {
			job.say("Asking GitHub for a new %s version of %s…", req.Bump, p.Name)
			if err := dispatch(p.ID, "new-version.yml", map[string]string{"bump": req.Bump, "notes": req.Notes}); err != nil {
				return "", err
			}
			return "GitHub is making the new " + p.Name + " version: the release appears in about five minutes, then UPDATE ALL installs it.", nil
		})
	})
	mux.HandleFunc("/api/plugins/folders", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if r.Method == http.MethodGet {
			mpc, err := readFolders()
			saved, have := savedFolders()
			res := map[string]any{"mpc": mpc, "saved": nil}
			if have {
				res["saved"] = saved
			}
			if err != nil {
				res["error"] = err.Error()
			}
			jsonOut(w, res)
			return
		}
		if !postOnly(w, r) {
			return
		}
		var f pluginFolders
		if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&f); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if f.Breaks == nil {
			f.Breaks = []string{}
		}
		if f.Wax == nil {
			f.Wax = []string{}
		}
		startJob(w, "folders", func() (string, error) {
			job.say("Writing the folder lists to the MPC…")
			info, err := scanMPC()
			if err != nil {
				return "", err
			}
			home := info.Drive
			if home == "" {
				for _, d := range info.Drives {
					if n := filepath.Base(d); !strings.HasPrefix(n, "az01") && !strings.HasPrefix(n, "acvs") {
						home = d
						break
					}
				}
			}
			return writeFolders(f, home)
		})
	})
	mux.HandleFunc("/api/mpcdirs", func(w http.ResponseWriter, r *http.Request) {
		touch()
		l, err := listMPCDirs(r.URL.Query().Get("dir"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		jsonOut(w, map[string]any{"dirs": l})
	})
	mux.HandleFunc("/api/github", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if !postOnly(w, r) {
			return
		}
		var req struct{ Token string }
		json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req)
		t := strings.TrimSpace(req.Token)
		if t == "" { // forget it
			setSetting("github_token", "")
			setSetting("github_user", "")
			w.WriteHeader(204)
			return
		}
		if len(t) > 400 || strings.ContainsAny(t, " \r\n\t") {
			http.Error(w, "That doesn't look like a GitHub token.", 400)
			return
		}
		old := getSetting("github_token")
		setSetting("github_token", t)
		var me struct {
			Login string `json:"login"`
		}
		if _, err := ghDo("GET", "/user", nil, &me); err != nil || me.Login == "" {
			setSetting("github_token", old)
			msg := "GitHub didn't accept that token"
			if err != nil {
				msg += ": " + err.Error()
			}
			http.Error(w, msg, 400)
			return
		}
		setSetting("github_user", me.Login)
		w.WriteHeader(204)
	})
}
