# Third-party code

MPC TOOLS includes code and programs from these projects, each under its own licence.

## MPC Link (Padbangers) – MIT
`linkcore/` and `linkapp.go` / `linkhost.go` come from https://github.com/padbangers/mpc-link. Their licence is in [`linkcore/LICENSE-mpc-link`](linkcore/LICENSE-mpc-link). Changes: the import paths; a folder picker inside the settings page (`linkcore/web/page.go` + `fslist.go`, because the Windows folder window could open behind other windows); the settings page is served by MPC TOOLS instead of a tray icon.

## sshfs, libfuse, GLib – GPL-2.0 / LGPL-2.1
`linkcore/device/sshfs` is the unmodified sshfs 3.7.3 binary for 32-bit ARM from MPC Link. MPC TOOLS copies it to the MPC as a separate program. The source code and build script are listed in [`linkcore/device/README.md`](linkcore/device/README.md): https://github.com/libfuse/sshfs/releases/tag/sshfs-3.7.3, https://github.com/libfuse/libfuse and https://gitlab.gnome.org/GNOME/glib.

## Go libraries
- `github.com/pkg/sftp` – BSD-2-Clause
- `golang.org/x/crypto`, `golang.org/x/sys` – BSD-3-Clause (The Go Authors)
- `github.com/kr/fs` – BSD-3-Clause

## Addins it talks to (not included; installed on the MPC separately)
- mpc-addin-remote and mpc-addin-filebrowser by Jacob Sabella (MIT)
- mpc-addin-skipback by sd88me
