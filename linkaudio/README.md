# linkaudio-rx

The MPC audio receiver for MPC TOOLS. It joins the Ableton Link session on your network, subscribes to one **Link Audio** channel (the MPC's main mix, sent by HAKAI's MPC LINK AUDIO app) and passes the sound to MPC TOOLS.

**Licence:** GNU GPL, version 2 or later ([COPYING.md](COPYING.md)), because it's built with the [Ableton Link SDK](https://github.com/Ableton/link), which uses that licence. It's a separate program: MPC TOOLS runs it and they talk only through pipes. MPC TOOLS itself is MIT.

## Files

- `main.cpp`: the whole program.
- `build.sh`: builds `bin/linkaudio-rx.exe` (Windows x64) and `bin/linkaudio-rx-mac-aarch64` / `-x86_64` with zig, from the Link SDK at a fixed commit (`4f5be9939bd6d016758f021aa91f44440171ef2d`) and its bundled asio.
  ```
  pip install ziglang==0.17.0.post0
  sh linkaudio/build.sh
  ```
- `bin/`: the built programs that MPC TOOLS carries.

## How MPC TOOLS talks to it

- **stdin** (text lines): `sub <16 hex digits>` receive that channel, `off` stop receiving, `quit` exit. When stdin closes it exits, so it never outlives MPC TOOLS.
- **stderr** (text lines): `peers <n>`; whenever the channel list changes, `chs`, one `ch <id>\t<peer name>\t<channel name>` line per channel, then `end`; `sub <id>`; `off`.
- **stdout** (binary): one record per received buffer. A 16-byte header, `'L' 'A' channels 0 | frames(u16) 0 0 | sample rate(u32) | count(u32)`, little-endian, then `frames × channels` 16-bit samples, interleaved.

MPC TOOLS passes the records unchanged to its pages on `http://localhost:6721/audio/stream`, where `linkaudio.js` plays them with Web Audio.
