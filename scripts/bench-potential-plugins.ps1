# CPU bench of the Potential plugins on a Gen1 MPC (MPC One, Live, Live II, X, Force, Key 37): the Open MPC catalog's
# "run the CPU bench" (docs/BENCH.md in open-mpc/mpc-vst-plugins), without Linux or Docker.
#
# Paste this into PowerShell (Windows key, type powershell, Enter; right-click pastes):
#   irm https://raw.githubusercontent.com/PotentialBreakfast/mpc-tools/main/scripts/bench-potential-plugins.ps1 | iex
#
# For each Potential plugin in "VST2 Plugins" it copies the plugin's 32-bit .so and the bench to /tmp on the MPC, runs
# the bench there (about half a minute each; MPC can keep running, the bench never takes its audio's place) and
# deletes both. Nothing is installed. The results go to Potential-bench.txt on your Desktop, one JSON line per plugin:
# the figures a release records with tools/release.py --bench.
# bench/vstbench-armhf is Open MPC's tools/bench.c (MIT, sd88me), built for armhf with zig cc (see bench/README.md).

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Say($text, $colour = 'Gray') { Write-Host $text -ForegroundColor $colour }
function Invoke-Mpc($script) {
    $out = ($script -replace "`r", '') | ssh -o ConnectTimeout=8 "root@$mpc" 'tr -d ''\r'' | sh -s'
    if ($LASTEXITCODE -ne 0) { throw "The MPC didn't answer (ssh exit $LASTEXITCODE)." }
    return $out
}

try {
    $mpc = ''
    $ini = Join-Path $env:APPDATA 'MPC TOOLS\settings.ini'
    if (Test-Path $ini) {
        $line = Get-Content $ini | Where-Object { $_ -match '^\s*(host|ip)\s*=' } | Select-Object -Last 1
        if ($line) { $mpc = ($line -split '=', 2)[1].Trim() }
    }
    if (-not $mpc) { $mpc = (Read-Host "Your MPC's address (e.g. 192.168.1.50)").Trim() }
    Say "MPC: $mpc" 'Cyan'

    $arch = (Invoke-Mpc 'uname -m' | Select-Object -First 1).Trim()
    if ($arch -notlike 'arm*') { throw "This MPC is $arch (a Gen2): this bench is for Gen1 MPCs (armv7), where the catalog's figure is measured." }

    $bench = Join-Path $env:TEMP 'vstbench-armhf'
    Invoke-WebRequest -UseBasicParsing 'https://raw.githubusercontent.com/PotentialBreakfast/mpc-tools/main/scripts/bench/vstbench-armhf' -OutFile $bench
    scp -q -o ConnectTimeout=8 "$bench" "root@${mpc}:/tmp/vstbench"
    if ($LASTEXITCODE -ne 0) { throw 'Copying the bench to the MPC failed.' }

    Say 'Benching each plugin (about half a minute each)...' 'Cyan'
    $run = @'
chmod +x /tmp/vstbench
for d in /media/*/"VST2 Plugins"/"PotentialBreaks - VST - "*; do
  [ -f "$d/plugin.json" ] || continue
  so=$(sed -n 's/.*"file32": *"\([^"]*\)".*/\1/p' "$d/plugin.json" | head -n 1)
  [ -n "$so" ] && [ -f "$d/$so" ] || { echo "SKIP ${d##*/}: no 32-bit build"; continue; }
  v=$(sed -n 's/.*"version": *"\([0-9.]*\)".*/\1/p' "$d/plugin.json" | head -n 1)
  cp "$d/$so" /tmp/benchplug.so
  echo "== ${d##* - VST - } $v"
  taskset 2 /tmp/vstbench /tmp/benchplug.so -j 2>&1 | tail -n 12
  rm -f /tmp/benchplug.so
done
rm -f /tmp/vstbench
'@
    $out = Invoke-Mpc $run
    $out | ForEach-Object {
        if ($_ -like '== *') { Say $_ 'Cyan' }
        elseif ($_ -match '-> (PASS|WARN|FAIL)') { Say $_ ($(if ($Matches[1] -eq 'PASS') { 'Green' } elseif ($Matches[1] -eq 'WARN') { 'Yellow' } else { 'Red' })) }
        else { Say $_ }
    }
    $file = Join-Path ([Environment]::GetFolderPath('Desktop')) 'Potential-bench.txt'
    $out | Set-Content -Encoding UTF8 $file
    Say ''
    Say "Saved to $file" 'Green'
} catch {
    Say ''
    Say "Stopped: $($_.Exception.Message)" 'Red'
}
