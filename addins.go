package main

// Loading the addins that run inside the MPC app (Remote, Skipback, File Browser's part).
//
// Since the inMusic firmware with "user access" (built 2026-11-06), the MPC starts its app the standard way and
// ignores the acvs.service drop-ins in /etc that addin installers (and HAKAI) use to load addins, so the addins sit
// on the MPC but are never loaded: the Remote tab can't connect, and Skipback doesn't answer.
//
// The fix MPC TOOLS applies: a drop-in under /run (memory only, gone when the MPC is switched off) that keeps the
// app's own start command and adds the addins to LD_PRELOAD, then a restart of the MPC app. Nothing on the MPC is
// changed for good: if anything goes wrong, switching the MPC off and on starts it normally again.
//
// Restarting the MPC app drops an unsaved project, so MPC TOOLS only does it by itself when the app started less
// than a few minutes ago (the MPC was just switched on). Otherwise the Remote tab offers a "Load add-ons" button.

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

const addinsFreshSeconds = 240 // the MPC app counts as "just started" for this long

// addinsScript prints one of: running | noaddin | busy <seconds the app has run> | loaded | failed <log>
func addinsScript(force bool) string {
	age := ""
	if !force {
		age = `T=$(systemctl show acvs -p ActiveEnterTimestampMonotonic --value 2>/dev/null); ` +
			`G=$(awk -v t="${T:-0}" '{printf "%d", $1 - t/1000000}' /proc/uptime); ` +
			fmt.Sprintf(`if [ "$G" -gt %d ]; then echo busy $G; exit 0; fi; `, addinsFreshSeconds)
	}
	return `up() { cat /proc/net/tcp 2>/dev/null | grep -q ':1A40 00000000:0000 0A'; }; ` +
		`if up; then echo running; exit 0; fi; ` +
		`A=/data/mpc-addins; [ -f $A/remote/mpc_remote_addin.so ] || { echo noaddin; exit 0; }; ` +
		age +
		// the addin list: the installer's own drop-in if there is one, else whichever addins are on the MPC
		`L=$(grep -h '^Environment=LD_PRELOAD=' /etc/systemd/system/acvs.service.d/90-mpc-addins.conf 2>/dev/null | tail -n 1 | cut -d= -f3-); ` +
		`if [ -z "$L" ]; then for f in filebrowser/mpc_filebrowser_addin.so remote/mpc_remote_addin.so skipback/libmpc_skipback.so; do ` +
		`[ -f $A/$f ] && L="$L${L:+:}$A/$f"; done; fi; ` +
		`D=/run/systemd/system/acvs.service.d; mkdir -p $D; ` +
		// stock firmware: also put back the app's own start command, in case a HAKAI launcher drop-in is picked up
		// (its driver files are gone on the stock firmware). On HAKAI itself, its launcher stays in charge.
		`if [ -e /usr/lib/hakai_driver.so ]; then printf '[Service]\nEnvironment=LD_PRELOAD=%s\n' "$L" > $D/99-mpc-tools-addins.conf; ` +
		`else E=$(grep '^ExecStart=' $(systemctl show acvs -p FragmentPath --value) | tail -n 1); ` +
		`printf '[Service]\nExecStart=\n%s\nEnvironment=LD_PRELOAD=%s\n' "$E" "$L" > $D/99-mpc-tools-addins.conf; fi; ` +
		`systemctl daemon-reload; systemctl restart acvs; ` +
		`i=0; while [ $i -lt 45 ]; do up && break; sleep 2; i=$((i+1)); done; ` +
		`if up; then echo loaded; else echo failed; journalctl -u acvs -b --no-pager 2>/dev/null | tail -n 15; fi`
}

var addinsMu sync.Mutex

// loadAddins makes sure the MPC app has its addins. force restarts the MPC app even if it has been running a while.
// It returns running, loaded, busy, noaddin, failed or sshfail, and a message for the page.
func loadAddins(force bool) (string, string) {
	addinsMu.Lock()
	defer addinsMu.Unlock()
	if getHost() == "" {
		return "sshfail", ""
	}
	var out, errBuf bytes.Buffer
	c := exec.Command(sshPath, sshArgs(user+"@"+getHost(), addinsScript(force))...)
	c.Stdout, c.Stderr = &out, &errBuf
	hidden(c)
	if err := c.Run(); err != nil {
		return "sshfail", strings.TrimSpace(errBuf.String())
	}
	res := strings.TrimSpace(out.String())
	word, rest, _ := strings.Cut(res, "\n")
	word = strings.TrimSpace(word)
	switch {
	case word == "running", word == "loaded", word == "noaddin":
		return word, ""
	case strings.HasPrefix(word, "busy"):
		return "busy", ""
	case word == "failed":
		return "failed", "The MPC app restarted, but MPC Remote still didn't start.\n" +
			"Switching the MPC off and on starts it normally again.\n\n" + strings.TrimSpace(rest)
	default:
		return "failed", res
	}
}
