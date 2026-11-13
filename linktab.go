package main

// The "Folders" tab: MPC Link (Padbangers) running inside MPC TOOLS. It connects by itself when MPC TOOLS opens,
// uses the same settings as the MPC Link app (%AppData%\MPC Link\config.json), and takes the folders off the MPC
// again when the window closes.

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"mpctools/linkcore/config"
	"mpctools/linkcore/device"
	"mpctools/linkcore/mpc"
)

type linkTab struct {
	mu       sync.Mutex
	app      *LinkApp
	external bool   // the separate MPC Link app is running: leave it alone
	err      string // couldn't start
}

// externalLinkRunning: is the stand-alone MPC Link app (MPC-Link-*.exe) running?
func externalLinkRunning() bool {
	if runtime.GOOS != "windows" { // a Mac: the MPC Link app's process
		out, _ := exec.Command("pgrep", "-if", "mpc[ -]link").Output()
		return len(strings.TrimSpace(string(out))) > 0
	}
	c := exec.Command("tasklist", "/fo", "csv", "/nh")
	hidden(c)
	out, err := c.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.ToLower(string(out)), "\n") {
		if strings.HasPrefix(line, `"mpc-link`) || strings.HasPrefix(line, `"mpc link`) {
			return true
		}
	}
	return false
}

func startLinkTab(ln net.Listener) *linkTab {
	t := &linkTab{}
	if externalLinkRunning() {
		t.external = true
		ln.Close()
		return t
	}
	mpc.SSHFS = device.SSHFS
	cfg, err := config.Load()
	if err != nil {
		t.err = "Couldn't read the MPC Link settings: " + err.Error()
		ln.Close()
		return t
	}
	if cfg.Host == "" {
		cfg.Host = getHost() // first run: use MPC TOOLS' address for the MPC
	}
	a := newLinkApp(cfg)
	if err := a.serve(ln); err != nil {
		t.err = err.Error()
		return t
	}
	a.restart()
	t.app = a
	return t
}

// useHost: the MPC TOOLS address changed; give it to the built-in MPC Link too.
func (t *linkTab) useHost(h string) {
	t.mu.Lock()
	a := t.app
	t.mu.Unlock()
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.cfg.Host != h {
		a.cfg.Host, a.cfg.HostKey = h, ""
		a.cfg.Save()
	}
	a.mu.Unlock()
	a.restart()
}

func (t *linkTab) stop() {
	if t.app != nil {
		t.app.link.Stop()
	}
}

// status for the tab's light and message: ok (folders on the MPC), connecting, setup (nothing to share yet), error
func (t *linkTab) status() map[string]string {
	if t.external {
		return map[string]string{"state": "external", "msg": "The separate MPC Link app is already running, so MPC TOOLS is leaving the folders to it.\n\nTo use it here instead: right-click the MPC Link icon by the clock, choose Quit, then click Try again.", "url": ""}
	}
	if t.app == nil {
		return map[string]string{"state": "error", "msg": t.err, "url": ""}
	}
	s := t.app.state()
	st := map[string]string{"url": t.app.url, "mount": s.Status.Mount}
	switch {
	case s.Status.Connected:
		st["state"] = "ok"
	case !s.Enabled:
		st["state"] = "off"
	case s.Host == "" || len(s.Folders) == 0:
		st["state"] = "setup"
	case s.Status.Err != "":
		st["state"] = "connecting"
		st["msg"] = s.Status.Err
	default:
		st["state"] = "connecting"
	}
	return st
}

func (t *linkTab) retry(ln func() net.Listener) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.app != nil {
		t.app.restart()
		return
	}
	if !externalLinkRunning() {
		nt := startLinkTab(ln())
		t.app, t.external, t.err = nt.app, nt.external, nt.err
	}
}

var _ = fmt.Sprint
