package main

import _ "embed"

//go:embed linkaudio/bin/linkaudio-rx-mac-x86_64
var linkAudioRxMac []byte

func init() { linkAudioRx = linkAudioRxMac }
