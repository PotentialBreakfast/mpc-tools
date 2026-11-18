// MPC audio: the MPC's main mix over the network with Ableton Link Audio (HAKAI's MPC LINK AUDIO app sends it).
//
// The receiving is done by linkaudio-rx (linkaudio/, GPL-2.0-or-later, built with Ableton's Link SDK), a separate
// program MPC TOOLS carries inside it, writes to its cache folder and runs while someone is listening. They talk
// through pipes: channel lists and commands as text, audio as records (see linkaudio/main.cpp).
//
// The pages listen on /audio/stream: the header's Audio button, and the OBS page (so OBS gets sound with the
// picture). The receiver starts with the first listener and stops a little while after the last one leaves.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// linkAudioRx is the receiver for this computer (linkaudio_bin_*.go); nil where there isn't one.
var linkAudioRx []byte

type laChannel struct {
	ID   string `json:"id"`
	Peer string `json:"peer"`
	Name string `json:"name"`
}

type linkAudio struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	state     string // off, starting, searching, on, error
	msg       string
	peers     int
	channels  []laChannel
	current   string // subscribed channel id
	choice    string // "peer\tname" the user picked by tapping, "" = automatic
	rate      int
	nch       int
	lastAudio time.Time
	listeners map[chan []byte]struct{}
	stopTimer *time.Timer
}

func newLinkAudio() *linkAudio {
	return &linkAudio{state: "off", listeners: map[chan []byte]struct{}{}, choice: getSetting("audio_channel")}
}

func (a *linkAudio) status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.state
	if st == "on" && time.Since(a.lastAudio) > 1500*time.Millisecond {
		st = "quiet" // subscribed, but nothing arriving (MPC LINK AUDIO switched off?)
	}
	src := ""
	for _, c := range a.channels {
		if c.ID == a.current {
			src = c.Peer + " · " + c.Name
		}
	}
	return map[string]any{"state": st, "msg": a.msg, "peers": a.peers, "source": src,
		"channels": len(a.channels), "rate": a.rate, "nch": a.nch, "listeners": len(a.listeners),
		"available": linkAudioRx != nil}
}

// rxPath writes the receiver to the cache folder (once per version) and returns where it is.
// A fixed place, so the Windows firewall only asks about it once.
func rxPath() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserCacheDir()
	}
	dir := filepath.Join(base, "MPC TOOLS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "linkaudio-rx"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, linkAudioRx) {
		return p, nil
	}
	if err := os.WriteFile(p, linkAudioRx, 0o755); err != nil {
		// still running from before (Windows locks it): use a copy named after this version
		sum := sha256.Sum256(linkAudioRx)
		p = filepath.Join(dir, strings.TrimSuffix(name, ".exe")+"-"+strconv.FormatUint(uint64(binary.BigEndian.Uint32(sum[:4])), 16)+filepath.Ext(name))
		if err2 := os.WriteFile(p, linkAudioRx, 0o755); err2 != nil {
			return "", err
		}
	}
	return p, nil
}

func (a *linkAudio) set(state, msg string) { a.state, a.msg = state, msg }

// start runs the receiver if it isn't running. Called with a.mu held.
func (a *linkAudio) start() {
	if a.cmd != nil {
		return
	}
	if linkAudioRx == nil {
		a.set("error", "MPC audio isn't available on this computer yet.")
		return
	}
	p, err := rxPath()
	if err != nil {
		a.set("error", "Couldn't set up the audio receiver: "+err.Error())
		return
	}
	c := exec.Command(p)
	hidden(c)
	in, _ := c.StdinPipe()
	out, _ := c.StdoutPipe()
	errp, _ := c.StderrPipe()
	if err := c.Start(); err != nil {
		a.set("error", "Couldn't start the audio receiver: "+err.Error())
		return
	}
	a.cmd, a.stdin = c, in
	a.peers, a.channels, a.current, a.rate, a.nch = 0, nil, "", 0, 0
	a.set("searching", "")
	go a.readAudio(out)
	go a.readStatus(errp)
	go func() {
		c.Wait()
		a.mu.Lock()
		if a.cmd == c {
			a.cmd, a.stdin = nil, nil
			a.set("error", "The audio receiver stopped. Tap Audio to try again.")
			for ch := range a.listeners { // the pages reconnect, which starts it again
				close(ch)
				delete(a.listeners, ch)
			}
		}
		a.mu.Unlock()
	}()
}

// stop ends the receiver. Called with a.mu held.
func (a *linkAudio) stop() {
	if a.cmd == nil {
		return
	}
	c, in := a.cmd, a.stdin
	a.cmd, a.stdin = nil, nil
	a.set("off", "")
	a.current = ""
	go func() {
		io.WriteString(in, "quit\n")
		in.Close()
		done := make(chan struct{})
		go func() { c.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			c.Process.Kill()
		}
	}()
}

func (a *linkAudio) shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stop()
}

// pick chooses the channel to listen to: the one the user tapped to, else the MPC's (or a Force's), else the first.
// Called with a.mu held.
func (a *linkAudio) pick() {
	if a.stdin == nil {
		return
	}
	best := ""
	for _, c := range a.channels {
		if a.choice != "" && c.Peer+"\t"+c.Name == a.choice {
			best = c.ID
			break
		}
	}
	if best == "" {
		for _, c := range a.channels {
			n := strings.ToLower(c.Peer)
			if strings.Contains(n, "mpc") || strings.Contains(n, "force") {
				best = c.ID
				break
			}
		}
	}
	if best == "" && len(a.channels) > 0 {
		best = a.channels[0].ID
	}
	if best == a.current {
		return
	}
	a.current = best
	if best == "" {
		io.WriteString(a.stdin, "off\n")
		a.set("searching", "")
		return
	}
	io.WriteString(a.stdin, "sub "+best+"\n")
	a.set("on", "")
}

// next switches to the following channel (a tap on the source name) and remembers it.
func (a *linkAudio) next() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.channels) < 2 {
		return
	}
	i := 0
	for j, c := range a.channels {
		if c.ID == a.current {
			i = j + 1
		}
	}
	c := a.channels[i%len(a.channels)]
	a.choice = c.Peer + "\t" + c.Name
	go setSetting("audio_channel", a.choice)
	a.pick()
}

func (a *linkAudio) readStatus(r io.Reader) {
	sc := bufio.NewScanner(r)
	var list []laChannel
	inList := false
	for sc.Scan() {
		line := sc.Text()
		a.mu.Lock()
		switch {
		case line == "chs":
			inList, list = true, nil
		case inList && strings.HasPrefix(line, "ch "):
			f := strings.SplitN(line[3:], "\t", 3)
			if len(f) == 3 && f[1] != "MPC TOOLS" {
				list = append(list, laChannel{ID: f[0], Peer: f[1], Name: f[2]})
			}
		case line == "end":
			inList = false
			a.channels = list
			a.pick()
		case strings.HasPrefix(line, "peers "):
			a.peers, _ = strconv.Atoi(line[6:])
		}
		a.mu.Unlock()
	}
}

// readAudio passes every record (16-byte header + samples) to the listening pages, as it is.
func (a *linkAudio) readAudio(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<16)
	hdr := make([]byte, 16)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			return
		}
		if hdr[0] != 'L' || hdr[1] != 'A' {
			return // out of step: the receiver is restarted by the next listener
		}
		nch := int(hdr[2])
		frames := int(binary.LittleEndian.Uint16(hdr[4:6]))
		rate := int(binary.LittleEndian.Uint32(hdr[8:12]))
		rec := make([]byte, 16+nch*frames*2)
		copy(rec, hdr)
		if _, err := io.ReadFull(br, rec[16:]); err != nil {
			return
		}
		a.mu.Lock()
		a.rate, a.nch, a.lastAudio = rate, nch, time.Now()
		for ch := range a.listeners {
			select {
			case ch <- rec:
			default: // that page is behind: it catches up by itself
			}
		}
		a.mu.Unlock()
	}
}

func (a *linkAudio) listen() chan []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopTimer != nil {
		a.stopTimer.Stop()
		a.stopTimer = nil
	}
	ch := make(chan []byte, 512)
	a.listeners[ch] = struct{}{}
	a.start()
	return ch
}

func (a *linkAudio) unlisten(ch chan []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.listeners[ch]; !ok {
		return
	}
	delete(a.listeners, ch)
	if len(a.listeners) == 0 && a.stopTimer == nil {
		a.stopTimer = time.AfterFunc(20*time.Second, func() { // a page reloading shouldn't restart it
			a.mu.Lock()
			defer a.mu.Unlock()
			a.stopTimer = nil
			if len(a.listeners) == 0 {
				a.stop()
			}
		})
	}
}

func addAudioRoutes(mux *http.ServeMux, a *linkAudio, touch func()) {
	mux.HandleFunc("/audio/stream", func(w http.ResponseWriter, r *http.Request) {
		touch()
		ch := a.listen()
		defer a.unlisten(ch)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				touch()
			case rec, ok := <-ch:
				if !ok {
					return
				}
				if _, err := w.Write(rec); err != nil {
					return
				}
				for n := len(ch); n > 0; n-- { // send what's waiting in one go
					if _, err := w.Write(<-ch); err != nil {
						return
					}
				}
				if fl != nil {
					fl.Flush()
				}
			}
		}
	})
	mux.HandleFunc("/api/audio/next", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		a.next()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.status())
	})
	mux.HandleFunc("/linkaudio.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(linkAudioJS)
	})
}
