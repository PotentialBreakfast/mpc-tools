#!/bin/sh
# Builds linkaudio-rx (the Link Audio receiver MPC TOOLS runs) into bin/, with zig as the compiler:
#   pip install ziglang==0.17.0.post0
#   sh linkaudio/build.sh            (Windows exe + Mac binaries)
#   sh linkaudio/build.sh linux      (a Linux build too, for testing)
# Uses the Ableton Link SDK at a fixed commit, with its bundled asio.
set -eu
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
LINK_COMMIT=4f5be9939bd6d016758f021aa91f44440171ef2d
SRC=${LINK_SRC:-$HERE/.link-sdk}

if [ ! -d "$SRC/include/ableton" ]; then
    git clone -q https://github.com/Ableton/link.git "$SRC"
fi
git -C "$SRC" fetch -q origin "$LINK_COMMIT" 2>/dev/null || true
git -C "$SRC" checkout -q "$LINK_COMMIT"
git -C "$SRC" submodule update -q --init modules/asio-standalone

ASIO_VER=$(sed -n 's/^#define ASIO_VERSION \([0-9]*\).*/\1/p' "$SRC/modules/asio-standalone/include/asio/version.hpp")
ASIO_NS="link_asio_$((ASIO_VER / 100000))_$((ASIO_VER / 100 % 1000))_$((ASIO_VER % 100))"
ZIG=${ZIG:-"python3 -m ziglang"}
COMMON="-std=c++17 -O2 -DNDEBUG -I$SRC/include -I$SRC/modules/asio-standalone/include
  -DASIO_STANDALONE=1 -DASIO_NO_TYPEID=1 -DASIO_VERSION_NAMESPACE=$ASIO_NS"
mkdir -p "$HERE/bin"

echo "Windows x64"
# The SDK spells some Windows headers with capitals; MinGW's are lower case. Shims for a Linux/Mac build:
SHIM="$HERE/.win-shim"
mkdir -p "$SHIM"
for h in WS2tcpip:ws2tcpip WinSock2:winsock2 Windows:windows; do
    echo "#include <${h#*:}.h>" > "$SHIM/${h%%:*}.h"
done
# Windows' headers #define interface (COM); the SDK uses it as a name. Include them first, then drop it.
printf '%s\n' '#include <winsock2.h>' '#include <ws2tcpip.h>' '#include <windows.h>' '#include <iphlpapi.h>' \
    '#undef interface' > "$SHIM/prefix.h"
$ZIG c++ -target x86_64-windows-gnu $COMMON -I"$SHIM" -include "$SHIM/prefix.h" -DLINK_PLATFORM_WINDOWS=1 -DINCL_EXTRA_HTON_FUNCTIONS=1 \
    -U_WIN32_WINNT -D_WIN32_WINNT=0x0601 -DWIN32_LEAN_AND_MEAN -DNOMINMAX \
    "$HERE/main.cpp" -o "$HERE/bin/linkaudio-rx.exe" -static -s \
    -lws2_32 -liphlpapi -lwinmm -lmswsock
rm -f "$HERE/bin/linkaudio-rx.pdb"

for arch in aarch64 x86_64; do
    echo "macOS $arch"
    $ZIG c++ -target $arch-macos.11.0 $COMMON -DLINK_PLATFORM_UNIX=1 -DLINK_PLATFORM_MACOSX=1 \
        "$HERE/main.cpp" -o "$HERE/bin/linkaudio-rx-mac-$arch" -s
done

if [ "${1:-}" = linux ]; then
    echo "Linux x64"
    $ZIG c++ -target x86_64-linux-gnu $COMMON -DLINK_PLATFORM_UNIX=1 -DLINK_PLATFORM_LINUX=1 \
        "$HERE/main.cpp" -o "$HERE/bin/linkaudio-rx-linux" -lpthread
fi
ls -l "$HERE/bin"
