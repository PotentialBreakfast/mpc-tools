# Update the Potential plugins on an MPC to their newest GitHub releases, in one go.
#
# Paste this into PowerShell (Windows key, type powershell, Enter; right-click pastes):
#   irm https://raw.githubusercontent.com/PotentialBreakfast/mpc-tools/main/scripts/update-potential-plugins.ps1 | iex
#
# What it does (MPC OS 3.10.1 or later, External Plugin Support on):
#   1. downloads each plugin's newest "-mpc-sdk.zip" (Akai's plugin folder format) from GitHub
#   2. copies the folders to the MPC (SSH as root; the MPC's address comes from MPC TOOLS' settings, else it asks)
#   3. puts them in "VST2 Plugins" on the drive that already has them (else the first drive with that folder), and
#      moves the old copies (there, in other drives' "VST2 Plugins" and in the old "Synths" folders) into
#      "Potential-backup-<date>" on that drive. Nothing is deleted: remove that folder once everything works.
#   4. offers to restart the MPC app so the new versions load (save your project first)
# Needs Windows' OpenSSH client and root SSH access to the MPC.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$plugins = 'potentialbreaks', 'potentialheat', 'potentialnoise', 'potentialpatina', 'potentialpunch', 'potentialwarp', 'potentialwax'

function Say($text, $colour = 'Gray') { Write-Host $text -ForegroundColor $colour }

# runs a shell script on the MPC (sent on stdin, so names with spaces need no extra quoting) and returns its output
function Invoke-Mpc($script) {
    $out = ($script -replace "`r", '') | ssh -o ConnectTimeout=8 "root@$mpc" 'tr -d ''\r'' | sh -s'
    if ($LASTEXITCODE -ne 0) { throw "The MPC didn't answer (ssh exit $LASTEXITCODE)." }
    return $out
}

try {
    # --- which MPC ---
    $mpc = ''
    $ini = Join-Path $env:APPDATA 'MPC TOOLS\settings.ini'
    if (Test-Path $ini) {
        $line = Get-Content $ini | Where-Object { $_ -match '^\s*(host|ip)\s*=' } | Select-Object -Last 1
        if ($line) { $mpc = ($line -split '=', 2)[1].Trim() }
    }
    if (-not $mpc) { $mpc = (Read-Host "Your MPC's address (e.g. 192.168.1.50)").Trim() }
    Say "MPC: $mpc" 'Cyan'

    # --- where the plugins go: the drive that already has a VST2 Plugins folder ---
    $drive = Invoke-Mpc @'
for d in /media/*/"VST2 Plugins"/"PotentialBreaks - VST - "*; do [ -d "$d" ] && { d="${d%/VST2 Plugins/*}"; echo "$d"; exit 0; }; done
for d in /media/*/"VST2 Plugins"; do [ -d "$d" ] && { echo "${d%/VST2 Plugins}"; exit 0; }; done
echo NONE
'@
    $drive = ($drive | Select-Object -First 1).Trim()
    if ($drive -eq 'NONE' -or -not $drive.StartsWith('/media/')) {
        throw "There's no 'VST2 Plugins' folder on the MPC's drives. Make one at the top of your USB stick or SD card, then run this again."
    }
    if ($drive -match '[^A-Za-z0-9_./-]') { throw "Unexpected drive name on the MPC: $drive" }
    Say "Plugins go to: $drive/VST2 Plugins" 'Cyan'

    # --- download the newest releases ---
    $work = Join-Path $env:TEMP 'potential-update'
    if (Test-Path $work) { Remove-Item -Recurse -Force $work }
    New-Item -ItemType Directory -Path $work | Out-Null
    $found = @()
    foreach ($id in $plugins) {
        $rel = Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/PotentialBreakfast/mpc-vst-$id/releases/latest"
        $asset = $rel.assets | Where-Object { $_.name -like '*-mpc-sdk.zip' } | Select-Object -First 1
        if (-not $asset) { Say "  $id $($rel.tag_name): no 3.10.1 download in this release, skipped" 'Yellow'; continue }
        $zip = Join-Path $work "$id.zip"
        Invoke-WebRequest -UseBasicParsing $asset.browser_download_url -OutFile $zip
        Expand-Archive -Path $zip -DestinationPath (Join-Path $work $id)
        $folder = Get-ChildItem -Directory (Join-Path $work $id) | Select-Object -First 1
        if (-not $folder -or $folder.Name -notmatch '^[A-Za-z0-9 ]+ - VST - [A-Za-z0-9 ]+$') { throw "Unexpected folder in $($asset.name)" }
        $found += [pscustomobject]@{ Id = $id; Name = $folder.Name; Path = $folder.FullName; Version = $rel.tag_name }
        Say "  downloaded $($folder.Name.Split('-')[-1].Trim()) $($rel.tag_name)"
    }
    if (-not $found) { throw 'Nothing to install.' }

    # --- copy to the MPC (a staging folder without spaces), then swap in one go ---
    $stage = "$drive/potential-update"
    Invoke-Mpc "rm -rf '$stage'; mkdir -p '$stage'" | Out-Null
    foreach ($p in $found) {
        Say "  copying $($p.Name) to the MPC..."
        scp -q -r -o ConnectTimeout=8 "$($p.Path)" "root@${mpc}:$stage/$($p.Id)"
        if ($LASTEXITCODE -ne 0) { throw "Copying $($p.Name) failed." }
    }
    $lines = ($found | ForEach-Object { "swap '$($_.Id)' '$($_.Name)'" }) -join "`n"
    $swap = @"
D='$drive'
V="`$D/VST2 Plugins"
B="`$D/Potential-backup-`$(date +%Y%m%d-%H%M)"
moved=0
swap() {
  [ -d "$stage/`$1" ] || { echo "missing upload: `$2"; return; }
  if [ -d "`$V/`$2" ]; then k="`${V#/}"; k="`${k#media/}"; mkdir -p "`$B/`$k"; mv "`$V/`$2" "`$B/`$k/"; moved=1; fi
  mv "$stage/`$1" "`$V/`$2"
  for s in /media/*/Synths /sdcard/Synths /media/*/"VST2 Plugins"; do
    [ "`$s" = "`$V" ] && continue
    if [ -d "`$s/`$2" ]; then k="`${s#/}"; k="`${k#media/}"; mkdir -p "`$B/`$k"; mv "`$s/`$2" "`$B/`$k/"; moved=1; echo "old copy moved out of `$s: `$2"; fi
  done
  v=`$(sed -n 's/.*"version": *"\([0-9.]*\)".*/\1/p' "`$V/`$2/plugin.json" | head -n 1)
  echo "installed: `$2 `$v"
}
$lines
rmdir "$stage" 2>/dev/null
sync
[ `$moved = 1 ] && echo "BACKUP:`$B"
true
"@
    $result = Invoke-Mpc $swap
    $backup = ''
    foreach ($l in $result) {
        if ($l -like 'BACKUP:*') { $backup = $l.Substring(7) } else { Say "  $l" 'Green' }
    }
    Remove-Item -Recurse -Force $work

    Say ''
    if ($backup) { Say "Old copies were moved to: $backup  (delete that folder once everything works)" 'Cyan' }
    Say 'The new versions load after the MPC app restarts.' 'Cyan'
    $answer = Read-Host 'Restart the MPC app now? SAVE YOUR PROJECT FIRST. Type y and press Enter (or just Enter to skip)'
    if ($answer -match '^[yY]') {
        Invoke-Mpc 'systemctl restart acvs' | Out-Null
        Say 'The MPC app is restarting. For Remote and Skipback, open MPC TOOLS in the next few minutes: it loads them again.' 'Green'
    } else {
        Say 'Skipped. Restart the MPC when it suits you.' 'Yellow'
    }
} catch {
    Say ''
    Say "Stopped: $($_.Exception.Message)" 'Red'
    Say 'Nothing was changed on the MPC unless a line above says "installed".' 'Yellow'
}
