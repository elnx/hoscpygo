#!/bin/sh
# Build the device-side server against the static OHOS dependency prefix.
#
# Required environment (defaults are the local probe workspace):
#   OHOS_NDK        OHOS native SDK root
#   OHOS_DEPS       prefix with static absl/protobuf/grpc dependencies
#   OHOS_GRPC_BUILD gRPC static build tree (libgrpc++.a, libgrpc.a, libupb_*.a)
#   OHOS_GRPC_INCLUDE gRPC source include directory
#
# The static dependency build is documented in README.md. This script only
# builds the extension; it deliberately does not download or build gRPC.
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BUILD=${BUILD:-"$DIR/build"}

cmake -S "$DIR" -B "$BUILD" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$DIR/toolchain.cmake" \
  -DCMAKE_BUILD_TYPE=Release \
  -DOHOS_DEPS="${OHOS_DEPS:-/tmp/ohos-build/prefix}" \
  -DOHOS_GRPC_BUILD="${OHOS_GRPC_BUILD:-/tmp/ohos-build/grpc-build}" \
  -DOHOS_GRPC_INCLUDE="${OHOS_GRPC_INCLUDE:-/home/dev/ohos/openharmony/third_party/grpc/include}"

cmake --build "$BUILD" -j "${JOBS:-72}"
ls -lh "$BUILD/hoscpy_server.z.so"
readelf -d "$BUILD/hoscpy_server.z.so" | grep NEEDED
