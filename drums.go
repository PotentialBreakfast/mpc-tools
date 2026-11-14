package main

// DRUM ONE SHOTS: one tap in the Plugins tab finds the drum and percussion one-shots among every sample on the MPC's
// drives and copies them to "DRUM ONE SHOTS" on the SD card, sorted into Kicks, Snares, Claps, Hats, Cymbals, Toms
// and Percs. No basses, chords, melodic sounds or loops.
//
// The work happens on the MPC itself: drumsort (drumsort/drumsort.c, built by drumsort/build.sh for Gen1 armv7 and
// Gen2 aarch64) is copied to /tmp and started over SSH, so the samples never travel to the PC. It remembers what it
// has looked at (DRUM ONE SHOTS/.drumsort/index.tsv), so tapping again later only checks new or changed files.

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed drumsort/bin/drumsort-armv7
var drumsortArm7 []byte

//go:embed drumsort/bin/drumsort-aarch64
var drumsortArm64 []byte

const drumFolder = "DRUM ONE SHOTS"

// progress replaces the job's last line when that was a progress line too, so the log doesn't fill up with counts.
func (j *jobState) progress(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if n := len(j.Log); n > 0 && strings.HasPrefix(j.Log[n-1], "  … ") {
		j.Log[n-1] = "  … " + line
		return
	}
	j.Log = append(j.Log, "  … "+line)
}

// /proc/mounts writes a space in a path as \040
func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

const drumsLookScript = `
echo "A|$(uname -m)"
grep ' /media/' /proc/mounts | while read -r dev mp rest; do echo "M|$dev|$mp"; done
for d in /media/*; do [ -d "$d" ] && echo "D|$d"; done
`

// pickSD: the SD card is the MMC device that isn't the MPC's own storage; else a drive with "SD" in its name.
func pickSD(mounts [][2]string) string {
	for _, m := range mounts {
		n := filepath.Base(m[1])
		if strings.HasPrefix(m[0], "/dev/mmcblk") && !strings.HasPrefix(n, "az01") && !strings.HasPrefix(n, "acvs") {
			return m[1]
		}
	}
	for _, m := range mounts {
		if strings.Contains(strings.ToLower(filepath.Base(m[1])), "sd") {
			return m[1]
		}
	}
	return ""
}

func makeDrumFolder() (string, error) {
	j := job
	j.say("Looking at the MPC…")
	out, err := mpcRun(drumsLookScript, 30e9)
	if err != nil {
		return "", err
	}
	arch := ""
	var mounts [][2]string
	var drives []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(strings.TrimSpace(l), "|", 3)
		switch {
		case len(f) == 2 && f[0] == "A":
			arch = f[1]
		case len(f) == 3 && f[0] == "M":
			mounts = append(mounts, [2]string{f[1], unescapeMount(f[2])})
		case len(f) == 2 && f[0] == "D":
			if n := filepath.Base(f[1]); !strings.HasPrefix(n, "acvs") {
				drives = append(drives, f[1])
			}
		}
	}
	bin := drumsortArm7
	if strings.Contains(arch, "aarch64") || strings.Contains(arch, "arm64") {
		bin = drumsortArm64
	}
	sd := pickSD(mounts)
	if sd == "" {
		return "", errors.New("there's no SD card in the MPC (or it isn't mounted). Put one in and try again")
	}
	if len(drives) == 0 {
		return "", errors.New("the MPC shows no drives under /media")
	}
	dest := sd + "/" + drumFolder
	j.say("The folder goes to: %s", dest)
	j.say("Looking through: %s", strings.Join(drives, ", "))

	// copy drumsort over
	tmp, err := os.CreateTemp("", "drumsort-")
	if err != nil {
		return "", err
	}
	tmp.Write(bin)
	tmp.Close()
	defer os.Remove(tmp.Name())
	const remote = "/tmp/mpctools-drumsort"
	if err := mpcPut(tmp.Name(), remote); err != nil {
		return "", err
	}

	args := []string{"chmod +x " + remote + " && exec " + remote, "--out", shq(dest)}
	for _, d := range drives {
		args = append(args, shq(d))
	}
	j.say("Checking every sound file. The first time can take a while (the MPC keeps working, but leave heavy projects for after).")
	c := exec.Command(sshPath, sshArgs(user+"@"+getHost(), strings.Join(args, " "))...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return "", err
	}
	var errBuf strings.Builder
	c.Stderr = &errBuf
	hidden(c)
	if err := c.Start(); err != nil {
		return "", fmt.Errorf("couldn't start ssh: %v", err)
	}
	done, total := "", 0
	counts := []string{}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		f := strings.SplitN(line, "|", 4)
		switch {
		case len(f) == 4 && f[0] == "P":
			where := f[3]
			if len(where) > 70 {
				where = "…" + where[len(where)-69:]
			}
			j.progress(fmt.Sprintf("looked at %s files, kept %s – %s", f[1], f[2], where))
		case len(f) == 3 && f[0] == "K":
			if n, _ := strconv.Atoi(f[2]); n > 0 {
				counts = append(counts, fmt.Sprintf("%s %d", f[1], n))
				total += n
			}
		case len(f) >= 2 && f[0] == "D":
			done = strings.Join(f[1:], "|")
		default:
			if strings.TrimSpace(line) != "" {
				j.say("%s", line)
			}
		}
	}
	werr := c.Wait()
	j.mu.Lock() // the last progress line is out of date now
	if n := len(j.Log); n > 0 && strings.HasPrefix(j.Log[n-1], "  … ") {
		j.Log = j.Log[:n-1]
	}
	j.mu.Unlock()
	if done == "" { // drumsort didn't get to the end
		if werr != nil && strings.TrimSpace(errBuf.String()) != "" {
			return "", fmt.Errorf("the MPC stopped answering: %s", strings.TrimSpace(errBuf.String()))
		}
		return "", errors.New("the drum folder wasn't finished (see above). Tap MAKE DRUM FOLDER again to carry on where it stopped")
	}
	if len(counts) > 0 {
		j.say("New this time: %s", strings.Join(counts, ", "))
	}
	return done + " They're in " + dest + ". In PotentialKits, pick it on the SETUP page.", nil
}

func addDrumRoutes(mux *http.ServeMux, startJob func(http.ResponseWriter, string, func() (string, error))) {
	mux.HandleFunc("/api/drums/make", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if postOnly(w, r) {
			startJob(w, "drums", makeDrumFolder)
		}
	})
}
