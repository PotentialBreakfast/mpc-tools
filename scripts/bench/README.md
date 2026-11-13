# vstbench-armhf

Open MPC's CPU bench (`tools/bench.c` in [open-mpc/mpc-vst-plugins](https://github.com/open-mpc/mpc-vst-plugins), MIT,
Copyright (c) 2026 sd88me) at commit ed1a0ac7, built for Gen1 MPCs (armhf, glibc 2.31) without Docker:

```
python3 -m ziglang cc -target arm-linux-gnueabihf.2.31 -mcpu=cortex_a9 -mfpu=neon -O2 -s -o vstbench-armhf tools/bench.c -ldl -lm
```

`../bench-potential-plugins.ps1` copies it to the MPC's `/tmp`, runs it on each Potential plugin and deletes it.
See the catalog's [docs/BENCH.md](https://github.com/open-mpc/mpc-vst-plugins/blob/main/docs/BENCH.md) for what the figures mean.
