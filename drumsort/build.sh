#!/usr/bin/env bash
# Builds drumsort for the MPC (Gen1 armv7 and Gen2 aarch64) as static programs, into bin/, where MPC TOOLS embeds them.
# Needs: pip install ziglang==0.17.0
set -euo pipefail
cd "$(dirname "$0")"
Z="python3 -m ziglang cc"
$Z -target arm-linux-musleabihf -mcpu=cortex_a9 -mfpu=neon -O2 -static -s -o bin/drumsort-armv7 drumsort.c -lm
$Z -target aarch64-linux-musl -mcpu=cortex_a55 -O2 -static -s -o bin/drumsort-aarch64 drumsort.c -lm
ls -l bin
