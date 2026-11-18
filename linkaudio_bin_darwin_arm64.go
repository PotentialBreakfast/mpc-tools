package main

import _ "embed"

//go:embed linkaudio/bin/linkaudio-rx-mac-aarch64
var linkAudioRxMac []byte

func init() { linkAudioRx = linkAudioRxMac }
