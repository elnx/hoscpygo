#!/bin/sh
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SDK=${OHOS_NDK:-/home/dev/command-line-tools/sdk/26.0.0/native}
CC="$SDK/llvm/bin/aarch64-unknown-linux-ohos-clang"
[ -x "$CC" ] || CC="$SDK/llvm/bin/clang"
mkdir -p "$DIR/build"
"$CC" --target=aarch64-linux-ohos --sysroot="$SDK/sysroot" \
  -shared -fPIC -O2 -fvisibility=hidden -Wl,-soname,libhoscpy_test.z.so \
  -o "$DIR/build/libhoscpy_test.z.so" "$DIR/minimal_ext.c"
file "$DIR/build/libhoscpy_test.z.so"
