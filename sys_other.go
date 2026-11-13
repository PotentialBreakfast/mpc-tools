//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// msgBox: a dialog on a Mac (osascript), else the console.
func msgBox(text string, icon uint32) {
	if runtime.GOOS == "darwin" {
		s := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text)
		exec.Command("osascript", "-e", `display dialog "`+s+`" with title "`+appTitle+`" buttons {"OK"} default button "OK"`).Run()
		return
	}
	fmt.Println("MSGBOX:", text)
}

func hidden(cmd *exec.Cmd) {}
