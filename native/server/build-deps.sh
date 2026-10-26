#!/bin/sh
# Build the static OHOS dependencies and the device server from source.
#
# Required:
#   OHOS       full OpenHarmony source tree (contains third_party/)
#   OHOS_NDK   OpenHarmony native SDK root (contains llvm/ and sysroot/)
#
# Optional:
#   BUILD      dependency build root (default: <this dir>/deps)
#   PREFIX     install prefix (default: $BUILD/prefix)
#   JOBS       parallel jobs (default: host CPU count)
#
# This is intentionally a plain POSIX shell script: it does not download
# anything and does not assume a user-specific /home path. It needs cmake,
# ninja, patch, make, perl, and a host C/C++ compiler for protoc.
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
: "${OHOS:?set OHOS to the full OpenHarmony source tree}"
: "${OHOS_NDK:?set OHOS_NDK to the OpenHarmony native SDK}"
export OHOS_NDK
BUILD=${BUILD:-"$DIR/deps"}
PREFIX=${PREFIX:-"$BUILD/prefix"}
JOBS=${JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)}
TOOLCHAIN="$DIR/toolchain.cmake"

for src in "$OHOS/third_party/abseil-cpp" "$OHOS/third_party/zlib" \
           "$OHOS/third_party/cares" "$OHOS/third_party/re2" \
           "$OHOS/third_party/openssl" "$OHOS/third_party/protobuf" \
           "$OHOS/third_party/grpc"; do
  [ -d "$src" ] || { echo "missing OpenHarmony source: $src" >&2; exit 2; }
done
[ -x "$OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang++" ] || {
  echo "OHOS_NDK does not contain the OHOS clang toolchain" >&2
  exit 2
}

mkdir -p "$BUILD" "$PREFIX"

echo "==> absl"
cmake -S "$OHOS/third_party/abseil-cpp" -B "$BUILD/absl-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DABSL_ENABLE_INSTALL=ON -DABSL_PROPAGATE_CXX_STD=ON -DBUILD_TESTING=OFF
cmake --build "$BUILD/absl-build" -j "$JOBS"
cmake --install "$BUILD/absl-build"

echo "==> zlib"
cmake -S "$OHOS/third_party/zlib" -B "$BUILD/zlib-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DZLIB_BUILD_SHARED=OFF -DZLIB_BUILD_TESTING=OFF
cmake --build "$BUILD/zlib-build" -j "$JOBS"
cmake --install "$BUILD/zlib-build"

echo "==> c-ares"
cmake -S "$OHOS/third_party/cares" -B "$BUILD/cares-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DCARES_STATIC=ON -DCARES_SHARED=OFF -DCARES_BUILD_TESTS=OFF \
  -DCARES_BUILD_TOOLS=OFF
cmake --build "$BUILD/cares-build" -j "$JOBS"
cmake --install "$BUILD/cares-build"

echo "==> re2"
cmake -S "$OHOS/third_party/re2" -B "$BUILD/re2-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DRE2_BUILD_TESTING=OFF \
  -DCMAKE_FIND_ROOT_PATH="$PREFIX;$OHOS_NDK/sysroot" \
  -Dabsl_DIR="$PREFIX/lib/cmake/absl"
cmake --build "$BUILD/re2-build" -j "$JOBS"
cmake --install "$BUILD/re2-build"

echo "==> OpenSSL"
mkdir -p "$BUILD/openssl-build"
if [ ! -f "$BUILD/openssl-build/Makefile" ]; then
  (
    cd "$BUILD/openssl-build"
    CC="$OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang --sysroot=$OHOS_NDK/sysroot -fPIC" \
    CXX="$OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang++ --sysroot=$OHOS_NDK/sysroot -fPIC" \
    AR="$OHOS_NDK/llvm/bin/llvm-ar" \
    RANLIB="$OHOS_NDK/llvm/bin/llvm-ranlib" \
      "$OHOS/third_party/openssl/Configure" linux-aarch64 \
      no-shared no-tests no-asm --prefix="$PREFIX" \
      --openssldir="$PREFIX/ssl"
  )
fi
make -C "$BUILD/openssl-build" -j "$JOBS"
make -C "$BUILD/openssl-build" install_sw

echo "==> protobuf runtime (target)"
cmake -S "$OHOS/third_party/protobuf" -B "$BUILD/protobuf-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -Dprotobuf_BUILD_TESTS=OFF -Dprotobuf_BUILD_SHARED_LIBS=OFF \
  -Dprotobuf_BUILD_PROTOC_BINARIES=OFF -Dprotobuf_BUILD_LIBUPB=OFF \
  -Dprotobuf_ABSL_PROVIDER=package -Dabsl_DIR="$PREFIX/lib/cmake/absl" \
  -DCMAKE_FIND_ROOT_PATH="$PREFIX;$OHOS_NDK/sysroot"
cmake --build "$BUILD/protobuf-build" -j "$JOBS"
cmake --install "$BUILD/protobuf-build"

echo "==> protoc (host)"
cmake -S "$OHOS/third_party/protobuf" -B "$BUILD/host-protobuf" -G Ninja \
  -DCMAKE_BUILD_TYPE=Release -Dprotobuf_BUILD_TESTS=OFF \
  -Dprotobuf_BUILD_SHARED_LIBS=OFF -Dprotobuf_BUILD_PROTOC_BINARIES=ON \
  -Dprotobuf_BUILD_LIBUPB=ON -Dprotobuf_ABSL_PROVIDER=module
cmake --build "$BUILD/host-protobuf" --target protoc -j "$JOBS"

echo "==> generated protocol"
mkdir -p "$DIR/gen"
"$BUILD/host-protobuf/protoc" -I "$DIR/../../proto" \
  --cpp_out="$DIR/gen" "$DIR/../../proto/scrcpy.proto"

echo "==> gRPC"
# gRPC's CMake needs xDS/googleapis/envoy API sources. They are not tracked by
# the OpenHarmony third_party_grpc repo; let gRPC download only if they are
# absent. Set GRPC_DOWNLOAD_ARCHIVES=ON in an offline tree after provisioning
# those directories manually.
GRPC_DOWNLOAD_ARCHIVES=OFF
for dep in xds envoy-api googleapis opencensus-proto protoc-gen-validate; do
  if [ ! -d "$OHOS/third_party/grpc/third_party/$dep" ]; then
    GRPC_DOWNLOAD_ARCHIVES=ON
  fi
done
[ "$GRPC_DOWNLOAD_ARCHIVES" = ON ] && \
  echo "    missing gRPC xDS sources; enabling CMake archive downloads"
THD="$OHOS/third_party/grpc/src/core/util/posix/thd.cc"
if ! grep -q '__OHOS__' "$THD"; then
  patch -d "$OHOS/third_party/grpc" -p1 \
    < "$DIR/patches/grpc-ohos-pthread-cancel.patch"
fi
cmake -S "$OHOS/third_party/grpc" -B "$BUILD/grpc-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DCMAKE_CXX_STANDARD=17 \
  -DCMAKE_PREFIX_PATH="$PREFIX" \
  -DCMAKE_FIND_ROOT_PATH="$PREFIX;$OHOS_NDK/sysroot" \
  -DgRPC_BUILD_TESTS=OFF -DgRPC_BUILD_CODEGEN=OFF \
  -DgRPC_BUILD_CSHARP_EXT=OFF -DgRPC_INSTALL=OFF -DBUILD_SHARED_LIBS=OFF \
  -DgRPC_DOWNLOAD_ARCHIVES="$GRPC_DOWNLOAD_ARCHIVES" -DgRPC_USE_SYSTEMD=OFF \
  -DgRPC_ABSL_PROVIDER=package -DgRPC_CARES_PROVIDER=package \
  -DgRPC_RE2_PROVIDER=package -DgRPC_SSL_PROVIDER=package \
  -DgRPC_ZLIB_PROVIDER=package -DgRPC_PROTOBUF_PROVIDER=package \
  -DOPENSSL_ROOT_DIR="$PREFIX" \
  -DOPENSSL_INCLUDE_DIR="$PREFIX/include" \
  -DOPENSSL_SSL_LIBRARY="$PREFIX/lib/libssl.a" \
  -DOPENSSL_CRYPTO_LIBRARY="$PREFIX/lib/libcrypto.a" \
  -DZLIB_LIBRARY="$PREFIX/lib/libz.a" \
  -DZLIB_INCLUDE_DIR="$PREFIX/include" \
  -Dabsl_DIR="$PREFIX/lib/cmake/absl" \
  -DProtobuf_DIR="$PREFIX/lib/cmake/protobuf" \
  -Dre2_DIR="$PREFIX/lib/cmake/re2" \
  -Dc-ares_DIR="$PREFIX/lib/cmake/c-ares" \
  -Dutf8_range_DIR="$PREFIX/lib/cmake/utf8_range"
cmake --build "$BUILD/grpc-build" --target grpc++ -j "$JOBS"

# build.sh defaults to $DIR/deps/grpc/include; expose the source headers there
# so a plain ./native/server/build.sh works after this script.
mkdir -p "$DIR/deps/grpc"
ln -sfn "$OHOS/third_party/grpc/include" "$DIR/deps/grpc/include"

echo "==> server extension"
OHOS_DEPS="$PREFIX" OHOS_GRPC_BUILD="$BUILD/grpc-build" \
OHOS_GRPC_INCLUDE="$OHOS/third_party/grpc/include" \
BUILD="${SERVER_BUILD:-$BUILD/server-build}" JOBS="$JOBS" \
  "$DIR/build.sh"

cat <<EOF

Dependencies and server are built.
  OHOS_DEPS=$PREFIX
  OHOS_GRPC_BUILD=$BUILD/grpc-build
  OHOS_GRPC_INCLUDE=$OHOS/third_party/grpc/include
  server: ${SERVER_BUILD:-$BUILD/server-build}/hoscpy_server.z.so

To embed a stripped build:
  $OHOS_NDK/llvm/bin/llvm-strip --strip-unneeded "${SERVER_BUILD:-$BUILD/server-build}/hoscpy_server.z.so"
  cp "${SERVER_BUILD:-$BUILD/server-build}/hoscpy_server.z.so" \
    "$DIR/../../internal/assets/device/server_static.z.so"
EOF
