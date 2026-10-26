# Self-built device server: static gRPC + public screen capture

This directory is a working probe of the device-side server. It replaces the
vendor `.so` with an unsigned, self-built aarch64 OpenHarmony library that:

- exports the two symbols the uitest loader expects;
- serves the existing `ScrcpyService` protocol on
  `unix-abstract:scrcpy_grpc_socket`;
- captures the primary screen through the public `OH_AVScreenCapture` API;
- encodes H.264 with `OH_VideoEncoder` through an encoder input surface;
- statically links protobuf and gRPC (and libc++), so it does not depend on the
  PC's `libprotobuf.z.so` / `libgrpc*.z.so` ABI.

It was verified on the HarmonyOS PC `<serial>` (API 26, 2880x1920).

## Result

Build output:

```
hoscpy_server.z.so   ~28 MB
NEEDED: libnative_avscreen_capture.so
        libnative_media_venc.so
        libnative_media_codecbase.so
        libnative_media_core.so
        libnative_window.so
        libnative_buffer.so
        libc.so
```

There is **no** `libprotobuf.z.so`, `libgrpc.z.so`, `libgrpcxx.z.so`, or
`libgpr.z.so` dependency. The loader accepts the unsigned file:

```
DLOK 0x...
SYMS init=0x... run=0x...
INIT rc=0
run
listening on unix-abstract:scrcpy_grpc_socket
```

The host-side gRPC client receives live frames; the first stream message is a
self-contained key frame:

```
msg 1 type=1 len=111996 nals=[nal7 nal8 nal5]
msg 2 type=1 len=635 nals=[nal1]
...
```

`ffprobe` on the captured stream reports `h264`, `Main`, `2880x1920`,
`yuv420p`, level 52. The stream includes SPS/PPS before the IDR.

## Files

| Path | Purpose |
|---|---|
| `server.cpp` | loader entry, generic gRPC service, frame subscription, parameter-set/keyframe handling |
| `capture.cpp/.h` | public NDK `OH_AVScreenCapture` + `OH_VideoEncoder` pipeline and frame queue |
| `gen/scrcpy.pb.*` | C++ messages generated from `../../proto/scrcpy.proto` with protoc 33.5 |
| `CMakeLists.txt`, `toolchain.cmake` | static link / OHOS cross build |
| `build.sh` | builds this `.so` against an existing dependency prefix |
| `runprobe.c` | direct `dlopen` runner used because standalone `uitest start-daemon` needs a test-server/apiCaller in this environment |

## Static dependencies

The dependency prefixes used for the verified build are under
`/tmp/ohos-build`; they are not committed. Sources came from the OpenHarmony
tree (`/home/dev/ohos/openharmony/third_party`). The version set is:

- abseil-cpp from the OpenHarmony tree
- protobuf 33.5.0 (`GOOGLE_PROTOBUF_VERSION 6033005`)
- gRPC 1.73.0
- re2, c-ares, zlib, OpenSSL 3.0.9
- OHOS NDK clang 15, `-fPIC`, static libraries, static libc++ in the final `.so`

### Configure commands (exact shape used)

Set:

```sh
OHOS=/home/dev/ohos/openharmony
N=/home/dev/command-line-tools/sdk/default/openharmony/native
B=/tmp/ohos-build
P=$B/prefix
TOOLCHAIN=$PWD/native/server/toolchain.cmake   # run from the Go repo root
```

absl, zlib, c-ares, re2:

```sh
cmake -S "$OHOS/third_party/abseil-cpp" -B "$B/absl-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$P" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DABSL_ENABLE_INSTALL=ON -DABSL_PROPAGATE_CXX_STD=ON -DBUILD_TESTING=OFF
cmake --build "$B/absl-build" -j 72 && cmake --install "$B/absl-build"

cmake -S "$OHOS/third_party/zlib" -B "$B/zlib-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$P" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DZLIB_BUILD_SHARED=OFF -DZLIB_BUILD_TESTING=OFF
cmake --build "$B/zlib-build" -j 72 && cmake --install "$B/zlib-build"

cmake -S "$OHOS/third_party/cares" -B "$B/cares-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$P" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DCARES_STATIC=ON -DCARES_SHARED=OFF -DCARES_BUILD_TESTS=OFF \
  -DCARES_BUILD_TOOLS=OFF
cmake --build "$B/cares-build" -j 72 && cmake --install "$B/cares-build"

cmake -S "$OHOS/third_party/re2" -B "$B/re2-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$P" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DRE2_BUILD_TESTING=OFF -DCMAKE_FIND_ROOT_PATH="$P;$N/sysroot" \
  -Dabsl_DIR="$P/lib/cmake/absl"
cmake --build "$B/re2-build" -j 72 && cmake --install "$B/re2-build"
```

OpenSSL (out-of-tree; `no-asm` avoids aarch64 assembler assumptions):

```sh
mkdir -p "$B/openssl-build" && cd "$B/openssl-build"
CC="$N/llvm/bin/aarch64-unknown-linux-ohos-clang --sysroot=$N/sysroot -fPIC" \
CXX="$N/llvm/bin/aarch64-unknown-linux-ohos-clang++ --sysroot=$N/sysroot -fPIC" \
AR="$N/llvm/bin/llvm-ar" RANLIB="$N/llvm/bin/llvm-ranlib" \
"$OHOS/third_party/openssl/Configure" linux-aarch64 no-shared no-tests no-asm \
  --prefix="$P" --openssldir="$P/ssl"
make -j72 && make install_sw
```

protobuf runtime (cross) and protoc (host):

```sh
cmake -S "$OHOS/third_party/protobuf" -B "$B/protobuf-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$P" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -Dprotobuf_BUILD_TESTS=OFF -Dprotobuf_BUILD_SHARED_LIBS=OFF \
  -Dprotobuf_BUILD_PROTOC_BINARIES=OFF -Dprotobuf_BUILD_LIBUPB=OFF \
  -Dprotobuf_ABSL_PROVIDER=package -Dabsl_DIR="$P/lib/cmake/absl" \
  -DCMAKE_FIND_ROOT_PATH="$P;$N/sysroot"
cmake --build "$B/protobuf-build" -j 72 && cmake --install "$B/protobuf-build"

# Host protoc must be the same 33.5 version as the runtime.
cmake -S "$OHOS/third_party/protobuf" -B "$B/host-protobuf" -G Ninja \
  -DCMAKE_BUILD_TYPE=Release -Dprotobuf_BUILD_TESTS=OFF \
  -Dprotobuf_BUILD_SHARED_LIBS=OFF -Dprotobuf_BUILD_PROTOC_BINARIES=ON \
  -Dprotobuf_BUILD_LIBUPB=ON -Dprotobuf_ABSL_PROVIDER=module
cmake --build "$B/host-protobuf" --target protoc -j 72
```

Generate the C++ messages (from the Go repo root):

```sh
mkdir -p native/server/gen
"$B/host-protobuf/protoc" -I proto --cpp_out=native/server/gen proto/scrcpy.proto
```

gRPC. `thd.cc` uses `pthread_cancel`, which OHOS libc does not provide. The
build tree was patched with:

```c
#if !defined(GPR_ANDROID) && !defined(__OHOS__)
void Thread::Kill(gpr_thd_id tid) {
```

Then:

```sh
cmake -S "$OHOS/third_party/grpc" -B "$B/grpc-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DCMAKE_CXX_STANDARD=17 \
  -DCMAKE_PREFIX_PATH="$P" -DCMAKE_FIND_ROOT_PATH="$P;$N/sysroot" \
  -DgRPC_BUILD_TESTS=OFF -DgRPC_BUILD_CODEGEN=OFF \
  -DgRPC_BUILD_CSHARP_EXT=OFF -DgRPC_INSTALL=OFF -DBUILD_SHARED_LIBS=OFF \
  -DgRPC_DOWNLOAD_ARCHIVES=OFF -DgRPC_USE_SYSTEMD=OFF \
  -DgRPC_ABSL_PROVIDER=package -DgRPC_CARES_PROVIDER=package \
  -DgRPC_RE2_PROVIDER=package -DgRPC_SSL_PROVIDER=package \
  -DgRPC_ZLIB_PROVIDER=package -DgRPC_PROTOBUF_PROVIDER=package \
  -DOPENSSL_ROOT_DIR="$P" \
  -DOPENSSL_INCLUDE_DIR="$P/include" \
  -DOPENSSL_SSL_LIBRARY="$P/lib/libssl.a" \
  -DOPENSSL_CRYPTO_LIBRARY="$P/lib/libcrypto.a" \
  -DZLIB_LIBRARY="$P/lib/libz.a" -DZLIB_INCLUDE_DIR="$P/include" \
  -Dabsl_DIR="$P/lib/cmake/absl" \
  -DProtobuf_DIR="$P/lib/cmake/protobuf" \
  -Dre2_DIR="$P/lib/cmake/re2" \
  -Dc-ares_DIR="$P/lib/cmake/c-ares" \
  -Dutf8_range_DIR="$P/lib/cmake/utf8_range"
cmake --build "$B/grpc-build" --target grpc++ -j 72
```

`native/server/build.sh` builds only the extension. It expects the prefixes
above and does not rebuild dependencies.

## Deploy and test

Build and push:

```sh
./native/server/build.sh
hdc -s 192.0.2.10:8710 -t <serial> file send \
  native/server/build/hoscpy_server.z.so /data/local/tmp/hoscpy_server.z.so
hdc ... shell "chmod 755 /data/local/tmp/hoscpy_server.z.so"
```

`uitest start-daemon` without a test-server/apiCaller still stops at
`Failed to get apiCaller object from peer` / `Failed to initialize server`
before the extension runs; this environment is not a valid end-to-end uitest
test. Use the direct loader for the probe:

```sh
# Build runprobe for aarch64 OHOS
$N/llvm/bin/aarch64-unknown-linux-ohos-clang --sysroot=$N/sysroot \
  -O2 -o runprobe native/server/runprobe.c -ldl
hdc ... file send runprobe /data/local/tmp/runprobe
hdc ... shell "chmod 755 /data/local/tmp/runprobe"
hdc ... shell "nohup /data/local/tmp/runprobe \
  /data/local/tmp/hoscpy_server.z.so -scale 1 -frameRate 60 \
  -bitRate 8388608 -iFrameInterval 500 \
  >/data/local/tmp/runprobe.out 2>&1 &"
```

The abstract socket is forwarded on the **hdc server host**, not necessarily
the local machine. For the remote server used here:

```sh
hdc -s 192.0.2.10:8710 -t <serial> \
  fport tcp:39501 localabstract:scrcpy_grpc_socket
```

Then connect a gRPC client to `192.0.2.10:39501`, call
`ScrcpyService/onStart`, and write `payload["data"].val_bytes` messages to an
Annex-B file. The `goscrcpy/client` probe used during development did exactly
this.

## Limitations

- Display size is hard-coded to the probed PC (`2880x1920`, divided by
  `-scale`); multi-display, rotation, and arbitrary resolutions are not handled.
- The public surface-capture path emits frames when the screen changes; a
  completely static screen can produce long gaps. Trigger input or use an
  encoder-side periodic frame if a constant frame rate is required.
- One active `onStart` stream is supported; audio, recording, and TLS are not.
- `onRequestIDRFrame` requests an encoder key frame; `onEnd` stops the capture
  pipeline.
- The `.so` is unsigned. The direct loader and the open-source analyzed `uitest`
  `ExecuteExtension` require no signature; a valid uitest test-server
  environment was not available to demonstrate the full daemon path.
