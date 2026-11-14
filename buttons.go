package main

// The MPC's own hardware buttons for the Remote tab (PLAY, STOP, REC, SHIFT, MENU, MAIN, the data wheel ...).
//
// The Remote addin (jacob-sabella/mpc-addin-remote 1.4+) can press any hardware button: it adds the button's note to
// what the MPC app reads from its control surface's "Private" port, so to the MPC it is the same as a real press.
// This needs no MIDI settings on the MPC (unlike MMC, which needs Receive MMC on), so it is what the Remote tab's
// transport uses now, with MMC only as a fallback.
//
// The addin knows the Force, MPC Sample and MPC Live III by itself. Other devices need a buttons.conf next to the
// addin: for the MPC One (and the Live / Live II / X family, which use the same notes) MPC TOOLS writes one, adding
// only names that aren't there yet, so anything the user learned or edited by hand stays as it is.
// The notes come from TheKikGen's MPC tables (MPCLiveXplore-libs, tkgl_midimapper.h): channel 1, press = velocity 127.

import (
	"io"
	"net/http"
	"strings"
	"time"
)

const mpcOneButtons = `PLAY=82
STOP=81
REC=73
OVERDUB=80
PLAY-START=83
SHIFT=49
MENU=123
MAIN=52
BROWSE=50
UNDO=67
COPY=122
ERASE=9
TAP-TEMPO=53
NOTE-REPEAT=11
FULL-LEVEL=39
16-LEVEL=40
TRACK-MUTE=43
NEXT-SEQ=42
STEP-SEQ=41
SAMPLE-EDIT=6
PROG-EDIT=2
QLINK=0
MINUS=55
PLUS=54
ENTER=111
BANK-A=35
BANK-B=36
BANK-C=37
BANK-D=38
UP=56
DOWN=57
LEFT=65
RIGHT=66
`

// buttonProfileScript adds the MPC One profile's missing names to the addin's buttons.conf.
// Prints: noaddin | ok <number of lines added>
func buttonProfileScript() string {
	return `D=/data/mpc-addins/remote; [ -d $D ] || { echo noaddin; exit 0; }
F=$D/buttons.conf; [ -f $F ] || printf '# Button profile: NAME=note [channel]. MPC One notes added by MPC TOOLS; edit freely.\n' > $F
n=0
while IFS= read -r l; do
  [ -n "$l" ] || continue
  k=${l%%=*}
  grep -q "^$k=" $F || { echo "$l" >> $F; n=$((n+1)); }
done <<'EOF'
` + mpcOneButtons + `EOF
sync
echo ok $n
`
}

var hwClient = &http.Client{Timeout: 8 * time.Second}

// addButtonRoutes: /hw/button, /hw/dial, /hw/buttons pass through to the addin so the page can read its answers;
// /api/buttonprofile writes the MPC One profile.
func addButtonRoutes(mux *http.ServeMux, touch func()) {
	for _, p := range []string{"button", "dial", "buttons"} {
		p := p
		mux.HandleFunc("/hw/"+p, func(w http.ResponseWriter, r *http.Request) {
			touch()
			resp, err := hwClient.Get("http://127.0.0.1:6720/" + p + "?" + r.URL.RawQuery)
			if err != nil {
				http.Error(w, "Can't reach the MPC. Is it switched on and on the network?", 502)
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, io.LimitReader(resp.Body, 64*1024))
		})
	}
	mux.HandleFunc("/api/buttonprofile", func(w http.ResponseWriter, r *http.Request) {
		touch()
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		out, err := mpcRun(buttonProfileScript(), 20*time.Second)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		out = strings.TrimSpace(out)
		if out == "noaddin" {
			http.Error(w, "MPC Remote isn't installed on the MPC", 409)
			return
		}
		w.Write([]byte(out))
	})
}
