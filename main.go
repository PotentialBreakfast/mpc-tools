// MPC TOOLS: one window with tabs for the Akai MPC One's addins:
//   - Remote       (jacob-sabella/mpc-addin-remote, port 6720): live screen, touch, transport buttons
//   - File Browser (jacob-sabella/mpc-addin-filebrowser, port 6760)
//   - Folders      (MPC Link by Padbangers, built in): the PC's folders in the MPC's own browser
//   - Save last 60 s (sd88me/mpc-addin-skipback)
// Opens hidden SSH tunnels to the MPC, serves the tabbed page on localhost:6721 and shows it in its own app window
// (Brave, else Chrome, else Edge, in app mode). Closing the window closes everything.
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mpctools/linkcore/discover"
)

//go:embed shell.html
var shellPage []byte

//go:embed remote.html
var remotePage []byte

//go:embed icon.png
var iconPNG []byte

//go:embed obs.html
var obsPage []byte

//go:embed plugins.html
var pluginsPage []byte

func validHostName(h string) bool {
	for _, c := range h {
		if !(c == '.' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return len(h) > 0 && len(h) < 254
}

const appTitle = "MPC TOOLS"

const appVersion = "1.4.0"

var (
	host        = "" // the MPC's address: set on the first-run screen (Find MPC), saved in %AppData%\MPC TOOLS\settings.ini
	user        = "root"
	sshPath     = "ssh"
	browserPref = "" // optional path from the .ini
	hostMu      sync.RWMutex
)

func getHost() string { hostMu.RLock(); defer hostMu.RUnlock(); return host }

func settingsPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = exeDir()
	}
	return filepath.Join(base, "MPC TOOLS", "settings.ini")
}

// The settings in %AppData%\MPC TOOLS\settings.ini, as key=value lines: host, and from 1.4.0 the Plugins tab's
// github_token, device and firmware. Writing one keeps the others.
var (
	setMu    sync.Mutex
	settings = map[string]string{}
	setOrder []string
)

func getSetting(k string) string { setMu.Lock(); defer setMu.Unlock(); return settings[k] }

func setSetting(k, v string) error {
	setMu.Lock()
	defer setMu.Unlock()
	if _, ok := settings[k]; !ok {
		setOrder = append(setOrder, k)
	}
	settings[k] = v
	var sb strings.Builder
	sb.WriteString("# MPC TOOLS settings\r\n")
	for _, key := range setOrder {
		if settings[key] != "" {
			sb.WriteString(key + "=" + settings[key] + "\r\n")
		}
	}
	p := settingsPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	return os.WriteFile(p, []byte(sb.String()), 0o600)
}

// saveHost remembers the MPC's address for next time.
func saveHost(h string) error {
	hostMu.Lock()
	host = h
	hostMu.Unlock()
	return setSetting("host", h)
}

// ---------- small helpers ----------

func fail(text string) { msgBox(text, 0x10); os.Exit(1) }

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(p)
}

// Settings: %AppData%\MPC TOOLS\settings.ini (written by the first-run screen), then an optional "MPC TOOLS.ini"
// next to the exe, which wins (for a portable copy): host=..., user=..., ssh=..., browser=<path to a browser exe>
func loadSettings() {
	readSettings(settingsPath(), true)
	readSettings(filepath.Join(exeDir(), "MPC TOOLS.ini"), false)
}

func readSettings(path string, keep bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		key := strings.ToLower(strings.TrimSpace(k))
		if keep { // the AppData file is rewritten by setSetting: remember everything in it
			if key == "ip" {
				key = "host"
			}
			if _, ok := settings[key]; !ok {
				setOrder = append(setOrder, key)
			}
			settings[key] = v
		}
		switch key {
		case "host", "ip":
			host = v
		case "user":
			user = v
		case "ssh":
			sshPath = v
		case "browser":
			browserPref = v
		}
	}
}

func sshArgs(extra ...string) []string {
	return append([]string{"-o", "ConnectTimeout=8", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}, extra...)
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

var probe = &http.Client{Timeout: 2 * time.Second}

// ---------- one connection (tunnel) per tool ----------

type tool struct {
	name     string
	port     int
	startCmd string                // run on the MPC first ("" = none)
	ready    func(port int) bool   // does the tool really answer?
	url      string                // what the tab shows when connected
	prepare  func(t *tool) bool    // runs before the tunnel; false = stop here (it has set the state)

	mu     sync.Mutex
	state  string // connecting, ok, error
	msg    string
	tunnel *exec.Cmd
	busy   bool
}

func (t *tool) set(state, msg string) {
	t.mu.Lock()
	t.state, t.msg = state, msg
	t.mu.Unlock()
}

func (t *tool) isBusy() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.busy }

func (t *tool) status() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]string{"state": t.state, "msg": t.msg, "url": t.url}
}

func (t *tool) stop() {
	t.mu.Lock()
	tun := t.tunnel
	t.tunnel = nil
	t.mu.Unlock()
	if tun != nil && tun.Process != nil {
		tun.Process.Kill()
		tun.Wait()
	}
}

func (t *tool) connect() {
	t.mu.Lock()
	if t.busy {
		t.mu.Unlock()
		return
	}
	t.busy = true
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.busy = false; t.mu.Unlock() }()

	if getHost() == "" {
		t.set("setup", "")
		return
	}
	t.set("connecting", "")
	if portOpen(t.port) && t.ready(t.port) {
		t.set("ok", "") // a tunnel is already open (e.g. an old .bat or app): use it
		return
	}
	t.stop()
	if t.prepare != nil && !t.prepare(t) {
		return
	}
	h := getHost()
	target := user + "@" + h
	var errBuf bytes.Buffer

	if t.startCmd != "" {
		c := exec.Command(sshPath, sshArgs(target, t.startCmd)...)
		c.Stderr = &errBuf
		hidden(c)
		if err := c.Run(); err != nil {
			if _, isExit := err.(*exec.ExitError); !isExit {
				t.set("error", "Couldn't start ssh (Windows OpenSSH): "+err.Error()+
					"\nSettings > System > Optional features > OpenSSH Client must be installed.")
				return
			}
			t.set("error", "Couldn't reach the MPC at "+h+".\n\n"+strings.TrimSpace(errBuf.String()))
			return
		}
		errBuf.Reset()
	}

	tun := exec.Command(sshPath, sshArgs("-N",
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", t.port, t.port),
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		target)...)
	tun.Stderr = &errBuf
	hidden(tun)
	if err := tun.Start(); err != nil {
		t.set("error", "Couldn't start ssh (Windows OpenSSH): "+err.Error())
		return
	}
	t.mu.Lock()
	t.tunnel = tun
	t.mu.Unlock()
	exited := make(chan struct{})
	go func() { tun.Wait(); close(exited) }()

	for i := 0; i < 50; i++ { // up to ~15 s
		select {
		case <-exited:
			t.set("error", "The connection to the MPC at "+h+" closed.\n\n"+strings.TrimSpace(errBuf.String()))
			return
		default:
		}
		if t.ready(t.port) {
			t.set("ok", "")
			go func() { // tell the page if the tunnel drops later
				<-exited
				t.mu.Lock()
				if t.tunnel == tun {
					t.tunnel = nil
					t.state, t.msg = "error", "The connection to the MPC dropped.\n\n"+strings.TrimSpace(errBuf.String())
				}
				t.mu.Unlock()
			}()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.stop()
	msg := t.name + " isn't answering on the MPC."
	if t.name == "MPC Remote" {
		msg += "\nIf the MPC was just switched on, wait for the MPC app to finish starting."
	}
	if s := strings.TrimSpace(errBuf.String()); s != "" {
		msg += "\n\n" + s
	}
	t.set("error", msg)
}

// ---------- Skipback (sd88me/mpc-addin-skipback): save the last seconds of main out ----------

// The addin saves when /tmp/mpc-addin-skipback.trigger appears and writes "ok <path>" or "error <why>" to .done.
const skipbackScript = `rm -f /tmp/mpc-addin-skipback.done; touch /tmp/mpc-addin-skipback.trigger; i=0; ` +
	`while [ $i -lt 20 ] && [ ! -s /tmp/mpc-addin-skipback.done ]; do sleep 1; i=$((i+1)); done; ` +
	`if [ -s /tmp/mpc-addin-skipback.done ]; then cat /tmp/mpc-addin-skipback.done; ` +
	`elif [ -e /tmp/mpc-addin-skipback.trigger ]; then rm -f /tmp/mpc-addin-skipback.trigger; echo notloaded; ` +
	`else echo timeout; fi`

func skipbackSave() (string, string) {
	var out, errBuf bytes.Buffer
	if getHost() == "" {
		return "error", "Set your MPC's address first."
	}
	c := exec.Command(sshPath, sshArgs(user+"@"+getHost(), skipbackScript)...)
	c.Stdout, c.Stderr = &out, &errBuf
	hidden(c)
	if err := c.Run(); err != nil {
		return "error", "Couldn't reach the MPC. " + strings.TrimSpace(errBuf.String())
	}
	res := strings.TrimSpace(out.String())
	switch {
	case strings.HasPrefix(res, "ok "):
		p := strings.TrimSpace(res[3:])
		return "ok", p
	case strings.HasPrefix(res, "error "):
		return "error", "Skipback: " + strings.TrimSpace(res[6:])
	case res == "notloaded":
		return "error", "Skipback isn't loaded in the MPC app. Tap Load add-ons on the Remote tab."
	default:
		return "error", "Skipback didn't answer in time."
	}
}

// ---------- the window keeps us alive ----------

var lastSeen atomic.Int64

func touch() { lastSeen.Store(time.Now().UnixNano()) }

func waitForPageToClose(limit time.Duration) {
	for time.Since(time.Unix(0, lastSeen.Load())) < limit {
		time.Sleep(time.Second)
	}
}

func listen(preferred int) net.Listener {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferred))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fail("Couldn't open a local port: " + err.Error())
		}
	}
	return ln
}

func findBrowser() string {
	if browserPref != "" {
		if _, err := os.Stat(browserPref); err == nil {
			return browserPref
		}
	}
	rel := []string{ // Brave first (main browser), then Chrome, then Edge
		`BraveSoftware\Brave-Browser\Application\brave.exe`,
		`Google\Chrome\Application\chrome.exe`,
		`Microsoft\Edge\Application\msedge.exe`,
	}
	for _, r := range rel {
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			b := os.Getenv(env)
			if b == "" {
				continue
			}
			p := filepath.Join(b, r)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func main() {
	loadSettings()

	// Local addresses: the tabbed page (6721) and the file browser passed through (6722), so it can sit in a tab.
	pageLn := listen(6721)
	filesLn := listen(6722)
	pageURL := fmt.Sprintf("http://localhost:%d/", pageLn.Addr().(*net.TCPAddr).Port)
	filesURL := fmt.Sprintf("http://localhost:%d/", filesLn.Addr().(*net.TCPAddr).Port)

	remote := &tool{name: "MPC Remote", port: 6720, url: "/remote", state: "connecting",
		ready: func(p int) bool {
			r, err := probe.Get(fmt.Sprintf("http://127.0.0.1:%d/info", p))
			if err != nil {
				return false
			}
			r.Body.Close()
			return r.StatusCode == 200
		}}
	// Since the "user access" firmware the MPC app no longer loads its addins by itself: load them when needed.
	// force is set by the Remote tab's "Load add-ons" button; otherwise only right after the MPC was switched on.
	var forceAddins atomic.Bool
	remote.prepare = func(t *tool) bool {
		force := forceAddins.Swap(false)
		if force {
			t.set("connecting", "Loading the add-ons. The MPC app is restarting, this takes up to a minute…")
		} else {
			t.set("connecting", "Checking the add-ons on the MPC…")
		}
		state, msg := loadAddins(force)
		switch state {
		case "busy":
			t.set("addins", "")
			return false
		case "failed":
			t.set("error", msg)
			return false
		case "loaded":
			t.set("connecting", "Add-ons loaded. Connecting…")
		default:
			t.set("connecting", "")
		}
		return true
	}
	files := &tool{name: "File Browser", port: 6760, url: filesURL, state: "connecting",
		startCmd: "systemctl start filebrowser",
		ready: func(p int) bool {
			r, err := probe.Get(fmt.Sprintf("http://127.0.0.1:%d/", p))
			if err != nil {
				return false
			}
			r.Body.Close()
			return r.StatusCode < 500
		}}
	tools := map[string]*tool{"remote": remote, "files": files}
	go remote.connect()
	go files.connect()
	defer remote.stop()
	defer files.stop()

	// MPC Link, built in: connects by itself, stops when the window closes
	link := startLinkTab(listen(6723))
	defer link.stop()

	// --- the tabbed page ---
	mux := http.NewServeMux()
	page := func(b []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			touch()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(b)
		}
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page(shellPage)(w, r)
	})
	mux.HandleFunc("/remote", page(remotePage))
	registerPlugins(mux) // the Plugins tab: /plugins and /api/plugins/...
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(iconPNG)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		touch()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"remote": remote.status(), "files": files.status(), "link": link.status(),
			"host": getHost(), "version": appVersion})
	})
	mux.HandleFunc("/api/loadaddins", func(w http.ResponseWriter, r *http.Request) { // the Remote tab's button
		touch()
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		forceAddins.Store(true)
		remote.set("connecting", "Loading the add-ons. The MPC app is restarting, this takes up to a minute…")
		go func() {
			for i := 0; i < 100 && remote.isBusy(); i++ { // a check may still be running: wait for it
				time.Sleep(200 * time.Millisecond)
			}
			remote.connect()
		}()
		w.WriteHeader(204)
	})
	mux.HandleFunc("/retry", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if r.URL.Query().Get("t") == "link" {
			go link.retry(func() net.Listener { return listen(6723) })
		} else if t, ok := tools[r.URL.Query().Get("t")]; ok {
			t.set("connecting", "")
			go t.connect()
		}
		w.WriteHeader(204)
	})
	var skipMu sync.Mutex
	mux.HandleFunc("/skipback", func(w http.ResponseWriter, r *http.Request) { // one-tap Skipback save
		touch()
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		if !skipMu.TryLock() {
			json.NewEncoder(w).Encode(map[string]string{"state": "busy", "msg": "Already saving…"})
			return
		}
		defer skipMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		state, msg := skipbackSave()
		json.NewEncoder(w).Encode(map[string]string{"state": state, "msg": msg})
	})
	mcpClient := &http.Client{Timeout: 40 * time.Second}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) { // the Remote tab's MIDI requests
		touch()
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		resp, err := mcpClient.Post("http://127.0.0.1:6720/mcp", "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(w, "can't reach the MPC: "+err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
	// first-run / change MPC: find MPCs on the network, save the chosen address, reconnect everything
	mux.HandleFunc("/api/discover", func(w http.ResponseWriter, r *http.Request) {
		touch()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(discover.Scan())
	})
	mux.HandleFunc("/api/sethost", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		h := strings.TrimSpace(r.URL.Query().Get("host"))
		if h == "" || strings.ContainsAny(h, " /\\@;'\"&|<>") || net.ParseIP(h) == nil && !validHostName(h) {
			http.Error(w, "That doesn't look like an address", 400)
			return
		}
		if err := saveHost(h); err != nil {
			http.Error(w, "Couldn't save the settings: "+err.Error(), 500)
			return
		}
		remote.stop()
		files.stop()
		go remote.connect()
		go files.connect()
		link.useHost(h)
		w.WriteHeader(204)
	})
	// OBS: the MPC screen on its own (Browser Source: http://localhost:6721/obs, 1280x800)
	mux.HandleFunc("/obs", page(obsPage))
	streamClient := &http.Client{} // no timeout: a stream runs for as long as OBS shows it
	mux.HandleFunc("/obs/stream", func(w http.ResponseWriter, r *http.Request) {
		touch()
		req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://127.0.0.1:6720/stream?"+r.URL.RawQuery, nil)
		resp, err := streamClient.Do(req)
		if err != nil {
			http.Error(w, "MPC Remote isn't connected", 502)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 64*1024)
		fl, _ := w.(http.Flusher)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
				touch()
			}
			if err != nil {
				return
			}
		}
	})
	go (&http.Server{Handler: mux}).Serve(pageLn)

	// --- the file browser, passed through so it can be shown inside a tab ---
	fbTarget, _ := url.Parse("http://127.0.0.1:6760")
	fb := httputil.NewSingleHostReverseProxy(fbTarget)
	fb.ModifyResponse = func(r *http.Response) error {
		r.Header.Del("X-Frame-Options")
		if csp := r.Header.Get("Content-Security-Policy"); strings.Contains(csp, "frame-ancestors") {
			var keep []string
			for _, part := range strings.Split(csp, ";") {
				if !strings.Contains(part, "frame-ancestors") {
					keep = append(keep, part)
				}
			}
			r.Header.Set("Content-Security-Policy", strings.Join(keep, ";"))
		}
		return nil
	}
	fb.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "The MPC File Browser isn't connected.", 502)
	}
	go (&http.Server{Handler: fb}).Serve(filesLn)

	// --- the window ---
	browser := findBrowser()
	touch()
	if browser == "" {
		exec.Command("rundll32", "url.dll,FileProtocolHandler", pageURL).Start()
		time.Sleep(15 * time.Second)
		waitForPageToClose(70 * time.Second)
		return
	}
	profile := filepath.Join(os.Getenv("LOCALAPPDATA"), "MPC TOOLS", "window")
	os.MkdirAll(profile, 0o755)
	win := exec.Command(browser,
		"--app="+pageURL,
		"--user-data-dir="+profile,
		"--window-size=1200,1000",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate")
	if err := win.Start(); err != nil {
		fail("Couldn't open the window: " + err.Error())
	}
	win.Wait() // normally returns when the window is closed
	// If the page is still polling (the browser handed the window to another process), keep going until it stops.
	time.Sleep(3 * time.Second)
	if time.Since(time.Unix(0, lastSeen.Load())) < 2500*time.Millisecond {
		waitForPageToClose(70 * time.Second)
	}
}
