#!/bin/sh
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

# Locate the OpenHarmony native SDK. Explicit OHOS_NDK wins; otherwise try the
# usual DevEco command-line-tools layouts under $HOME.
if [ -z "${OHOS_NDK:-}" ]; then
  for candidate in \
    "${DEVECO_SDK_HOME:-}/default/openharmony/native" \
    "$HOME/command-line-tools/sdk/default/openharmony/native" \
    "$HOME/command-line-tools/sdk/26.0.0/native"
  do
    if [ -x "$candidate/llvm/bin/aarch64-unknown-linux-ohos-clang" ]; then
      OHOS_NDK=$candidate
      break
    fi
  done
fi
: "${OHOS_NDK:?set OHOS_NDK to the OpenHarmony native SDK (the directory containing llvm/ and sysroot/)}"
SDK=$OHOS_NDK
CC="$SDK/llvm/bin/aarch64-unknown-linux-ohos-clang"
[ -x "$CC" ] || CC="$SDK/llvm/bin/clang"
[ -x "$CC" ] || { echo "no OHOS clang under $SDK" >&2; exit 2; }

mkdir -p "$DIR/build"
"$CC" --target=aarch64-linux-ohos --sysroot="$SDK/sysroot" \
  -shared -fPIC -O2 -fvisibility=hidden -Wl,-soname,libhoscpy_test.z.so \
  -o "$DIR/build/libhoscpy_test.z.so" "$DIR/minimal_ext.c"
file "$DIR/build/libhoscpy_test.z.so"
