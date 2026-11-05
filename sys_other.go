//go:build !windows

package main

import (
	"fmt"
	"os/exec"
)

func msgBox(text string, icon uint32) { fmt.Println("MSGBOX:", text) }

func hidden(cmd *exec.Cmd) {}
