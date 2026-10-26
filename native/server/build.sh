#!/bin/sh
# Build the device-side server against a static OHOS dependency prefix.
#
# Required:
#   OHOS_NDK          OpenHarmony native SDK root (contains llvm/ and sysroot/)
#
# Optional (defaults are local to this directory; populate them with the
# dependency build in README.md):
#   OHOS_DEPS         prefix with static absl/protobuf/grpc dependencies
#   OHOS_GRPC_BUILD   gRPC static build tree (libgrpc++.a, libgrpc.a, libupb_*.a)
#   OHOS_GRPC_INCLUDE gRPC source/include directory
#   BUILD             CMake build directory (default: ./build)
#   JOBS              parallel jobs (default: host CPU count)
#
# This script builds only the extension; it does not download or build gRPC.
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
: "${OHOS_NDK:?set OHOS_NDK to the OpenHarmony native SDK (the directory containing llvm/ and sysroot/)}"
export OHOS_NDK
BUILD=${BUILD:-"$DIR/build"}
OHOS_DEPS=${OHOS_DEPS:-"$DIR/deps/prefix"}
OHOS_GRPC_BUILD=${OHOS_GRPC_BUILD:-"$DIR/deps/grpc-build"}
OHOS_GRPC_INCLUDE=${OHOS_GRPC_INCLUDE:-"$DIR/deps/grpc/include"}
JOBS=${JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)}

cmake -S "$DIR" -B "$BUILD" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$DIR/toolchain.cmake" \
  -DCMAKE_BUILD_TYPE=Release \
  -DOHOS_DEPS="$OHOS_DEPS" \
  -DOHOS_GRPC_BUILD="$OHOS_GRPC_BUILD" \
  -DOHOS_GRPC_INCLUDE="$OHOS_GRPC_INCLUDE"

cmake --build "$BUILD" -j "$JOBS"
ls -lh "$BUILD/hoscpy_server.z.so"
readelf -d "$BUILD/hoscpy_server.z.so" | grep NEEDED
