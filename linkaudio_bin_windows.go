package main

import _ "embed"

//go:embed linkaudio/bin/linkaudio-rx.exe
var linkAudioRxWindows []byte

func init() { linkAudioRx = linkAudioRxWindows }
