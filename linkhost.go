package main

// from MPC Link (Padbangers, MIT)

import (
	"mpctools/linkcore/config"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// hostName is the computer's user-visible name ("Mike's Mac Mini"), not the network host name.
func hostName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	h, _ := os.Hostname()
	return strings.TrimSuffix(h, ".local")
}

func computerName(cfg *config.Config) string {
	if cfg.ComputerName != "" {
		return config.SafeName(cfg.ComputerName)
	}
	return config.SafeName(hostName())
}
