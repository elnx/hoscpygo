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
| `build-deps.sh` | builds all static dependencies and the extension; env-driven, no user-specific paths |
| `patches/` | small OHOS compatibility patches applied to dependency sources |
| `runprobe.c` | direct `dlopen` runner used because standalone `uitest start-daemon` needs a test-server/apiCaller in this environment |

## Static dependencies

The dependency prefixes are not committed. They are built from the OpenHarmony
source tree; the paths below are supplied by environment variables, not baked
into the scripts.

Convenience script (builds every dependency, regenerates the protocol, patches
gRPC for OHOS, then builds the server):

```sh
export OHOS=/path/to/openharmony
export OHOS_NDK=/path/to/command-line-tools/sdk/default/openharmony/native
./native/server/build-deps.sh
```

It defaults to `native/server/deps/` (gitignored) and leaves the result at
`native/server/deps/server-build/hoscpy_server.z.so`. Set `BUILD` to use a
different tree. The detailed commands below are the same steps, for auditing
or manual use.

The native dependency/server build is POSIX (Linux, macOS, or WSL). Windows only
needs Go for `make dist`, because the checked-in `server_static.z.so` is already
embedded; the Go build does **not** invoke this toolchain.

### Component dependencies

This table is the exact set used by the probe. The revisions are from the
OpenHarmony tree used for the verified build; use a consistent OpenHarmony
branch/tag rather than mixing arbitrary latest commits.

| Path under `$OHOS` | OpenHarmony repo | Version / revision in probe |
|---|---|---|
| `third_party/abseil-cpp` | `third_party_abseil-cpp` | LTS 20250127, `0456d6d` |
| `third_party/zlib` | `third_party_zlib` | 1.3.2, `0a0c45d` |
| `third_party/cares` | `third_party_cares` | 1.34.5, `d5552d4` |
| `third_party/re2` | `third_party_re2` | `7357c59` |
| `third_party/openssl` | `third_party_openssl` | 3.0.9, `4ea1f14` |
| `third_party/protobuf` | `third_party_protobuf` | 33.5.0, `8043cc3` |
| `third_party/grpc` | `third_party_grpc` | 1.73.0, `2f114c2` |

The OpenHarmony mirrors are:

```
https://gitcode.com/openharmony/<repo>
https://gitee.com/openharmony/<repo>      # mirror
```

A full `repo sync` is not required: create `$OHOS/third_party/` and clone only
the seven projects above into the listed paths. Using the exact OpenHarmony
revisions avoids mixing incompatible protobuf/gRPC branches.

These seven can be added as Git submodules. For the current CMake configuration
(`gRPC_BUILD_CODEGEN=OFF`, `gRPC_DOWNLOAD_ARCHIVES=OFF`) they are sufficient;
the five xDS source trees are not required. Still consider a separate
`hoscpy-native-deps` repository instead of submodules in the main Go repository:
submodule initialization downloads ~300 MB of sources, and normal `git clone
hoscpygo` / `make dist` do not need them. If submodules are used, put them under
a dedicated source root and set `OHOS` to that root; do not point `OHOS` at this
repository and expect the Go tree to look like an OpenHarmony checkout.

`upb`, `utf8_range`, and `address_sorting` are tracked inside the gRPC repo;
protobuf's host build also uses its bundled `third_party/abseil-cpp` and
`third_party/utf8_range`. They are not separate OpenHarmony repo projects.

The OHOS NDK/clang/sysroot is a separate DevEco command-line-tools install, not
part of `$OHOS`.

### gRPC xDS source trees

For this build, the five `third_party/grpc/third_party/{xds,envoy-api,googleapis,opencensus-proto,protoc-gen-validate}` trees are **not required**.

`third_party_grpc` already tracks the generated `src/core/ext/upb-gen/...` and
`src/core/ext/upbdefs-gen/...` xDS sources. Our CMake build uses:

- `gRPC_BUILD_CODEGEN=OFF`
- `gRPC_DOWNLOAD_ARCHIVES=OFF`

With those options, a clean OpenHarmony gRPC checkout builds `grpc++` without
those five directories; this was verified in a clean worktree. gRPC's default
CMake behavior (`gRPC_DOWNLOAD_ARCHIVES=ON`) downloads them for xDS code
generation/tests, which is why they appeared as untracked directories in the
probe tree. Do not enable downloads unless you need full xDS regeneration.

Defining `GRPC_NO_XDS` also compiles and links, but it only disables xDS
registration paths; it does not remove the xDS source files from the CMake
compile list. OpenHarmony's GN build gets the stronger behavior: default
`grpc_enable_profiler_minimal=true` defines `GRPC_NO_XDS` and filters the xDS
sources. Implementing that filtering in CMake is optional if a smaller binary
or a no-xDS build is required; it is not needed for the seven source repos.

### Configure commands

Set these paths for your machine (the values are examples):

```sh
export OHOS=/path/to/openharmony                 # full OpenHarmony source tree
export OHOS_NDK=/path/to/command-line-tools/sdk/default/openharmony/native
export BUILD=/tmp/hoscpy-deps
export PREFIX=$BUILD/prefix
export JOBS=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)
TOOLCHAIN=$PWD/native/server/toolchain.cmake   # run from the Go repo root

# Used by native/server/build.sh after the dependencies are installed:
export OHOS_DEPS=$PREFIX
export OHOS_GRPC_BUILD=$BUILD/grpc-build
export OHOS_GRPC_INCLUDE=$OHOS/third_party/grpc/include
```

Throughout this section, `OHOS`, `OHOS_NDK`, `BUILD`, `PREFIX`, and `JOBS`
refer to those values; substitute your own directory names.

absl, zlib, c-ares, re2:

```sh
cmake -S "$OHOS/third_party/abseil-cpp" -B "$BUILD/absl-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DABSL_ENABLE_INSTALL=ON -DABSL_PROPAGATE_CXX_STD=ON -DBUILD_TESTING=OFF
cmake --build "$BUILD/absl-build" -j "$JOBS" && cmake --install "$BUILD/absl-build"

cmake -S "$OHOS/third_party/zlib" -B "$BUILD/zlib-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DZLIB_BUILD_SHARED=OFF -DZLIB_BUILD_TESTING=OFF
cmake --build "$BUILD/zlib-build" -j "$JOBS" && cmake --install "$BUILD/zlib-build"

cmake -S "$OHOS/third_party/cares" -B "$BUILD/cares-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DCARES_STATIC=ON -DCARES_SHARED=OFF -DCARES_BUILD_TESTS=OFF \
  -DCARES_BUILD_TOOLS=OFF
cmake --build "$BUILD/cares-build" -j "$JOBS" && cmake --install "$BUILD/cares-build"

cmake -S "$OHOS/third_party/re2" -B "$BUILD/re2-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -DRE2_BUILD_TESTING=OFF -DCMAKE_FIND_ROOT_PATH="$PREFIX;$OHOS_NDK/sysroot" \
  -Dabsl_DIR="$PREFIX/lib/cmake/absl"
cmake --build "$BUILD/re2-build" -j "$JOBS" && cmake --install "$BUILD/re2-build"
```

OpenSSL (out-of-tree; `no-asm` avoids aarch64 assembler assumptions):

```sh
mkdir -p "$BUILD/openssl-build" && cd "$BUILD/openssl-build"
CC="$OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang --sysroot=$OHOS_NDK/sysroot -fPIC" \
CXX="$OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang++ --sysroot=$OHOS_NDK/sysroot -fPIC" \
AR="$OHOS_NDK/llvm/bin/llvm-ar" RANLIB="$OHOS_NDK/llvm/bin/llvm-ranlib" \
"$OHOS/third_party/openssl/Configure" linux-aarch64 no-shared no-tests no-asm \
  --prefix="$PREFIX" --openssldir="$PREFIX/ssl"
make -j"$JOBS" && make install_sw
```

protobuf runtime (cross) and protoc (host):

```sh
cmake -S "$OHOS/third_party/protobuf" -B "$BUILD/protobuf-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_INSTALL_PREFIX="$PREFIX" -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
  -Dprotobuf_BUILD_TESTS=OFF -Dprotobuf_BUILD_SHARED_LIBS=OFF \
  -Dprotobuf_BUILD_PROTOC_BINARIES=OFF -Dprotobuf_BUILD_LIBUPB=OFF \
  -Dprotobuf_ABSL_PROVIDER=package -Dabsl_DIR="$PREFIX/lib/cmake/absl" \
  -DCMAKE_FIND_ROOT_PATH="$PREFIX;$OHOS_NDK/sysroot"
cmake --build "$BUILD/protobuf-build" -j "$JOBS" && cmake --install "$BUILD/protobuf-build"

# Host protoc must be the same 33.5 version as the runtime.
cmake -S "$OHOS/third_party/protobuf" -B "$BUILD/host-protobuf" -G Ninja \
  -DCMAKE_BUILD_TYPE=Release -Dprotobuf_BUILD_TESTS=OFF \
  -Dprotobuf_BUILD_SHARED_LIBS=OFF -Dprotobuf_BUILD_PROTOC_BINARIES=ON \
  -Dprotobuf_BUILD_LIBUPB=ON -Dprotobuf_ABSL_PROVIDER=module
cmake --build "$BUILD/host-protobuf" --target protoc -j "$JOBS"
```

Generate the C++ messages (from the Go repo root):

```sh
mkdir -p native/server/gen
"$BUILD/host-protobuf/protoc" -I proto --cpp_out=native/server/gen proto/scrcpy.proto
```

gRPC. `thd.cc` uses `pthread_cancel`, which OHOS libc does not provide. Apply
the checked-in patch once to the source tree:

```sh
patch -d "$OHOS/third_party/grpc" -p1 \
  < native/server/patches/grpc-ohos-pthread-cancel.patch
```

The patch changes the guard from `#ifndef GPR_ANDROID` to
`#if !defined(GPR_ANDROID) && !defined(__OHOS__)`.

```sh
cmake -S "$OHOS/third_party/grpc" -B "$BUILD/grpc-build" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DCMAKE_CXX_STANDARD=17 \
  -DCMAKE_PREFIX_PATH="$PREFIX" -DCMAKE_FIND_ROOT_PATH="$PREFIX;$OHOS_NDK/sysroot" \
  -DgRPC_BUILD_TESTS=OFF -DgRPC_BUILD_CODEGEN=OFF \
  -DgRPC_BUILD_CSHARP_EXT=OFF -DgRPC_INSTALL=OFF -DBUILD_SHARED_LIBS=OFF \
  -DgRPC_DOWNLOAD_ARCHIVES=OFF -DgRPC_USE_SYSTEMD=OFF \
  -DgRPC_ABSL_PROVIDER=package -DgRPC_CARES_PROVIDER=package \
  -DgRPC_RE2_PROVIDER=package -DgRPC_SSL_PROVIDER=package \
  -DgRPC_ZLIB_PROVIDER=package -DgRPC_PROTOBUF_PROVIDER=package \
  -DOPENSSL_ROOT_DIR="$PREFIX" \
  -DOPENSSL_INCLUDE_DIR="$PREFIX/include" \
  -DOPENSSL_SSL_LIBRARY="$PREFIX/lib/libssl.a" \
  -DOPENSSL_CRYPTO_LIBRARY="$PREFIX/lib/libcrypto.a" \
  -DZLIB_LIBRARY="$PREFIX/lib/libz.a" -DZLIB_INCLUDE_DIR="$PREFIX/include" \
  -Dabsl_DIR="$PREFIX/lib/cmake/absl" \
  -DProtobuf_DIR="$PREFIX/lib/cmake/protobuf" \
  -Dre2_DIR="$PREFIX/lib/cmake/re2" \
  -Dc-ares_DIR="$PREFIX/lib/cmake/c-ares" \
  -Dutf8_range_DIR="$PREFIX/lib/cmake/utf8_range"
cmake --build "$BUILD/grpc-build" --target grpc++ -j "$JOBS"
```

`native/server/build.sh` builds only the extension. It expects the prefixes
above and does not rebuild dependencies.

## Embedded asset

The verified, stripped build is checked in as:

```
internal/assets/device/server_static.z.so
MD5 1cea8a2faf59e3af6791b47b22e313f3
```

`VariantsFor` tries `server_static.z.so` before the vendor `server_unix_*`
assets for unix-socket systems. `make dist` / `make build` therefore pick it up
without a native toolchain on the Go build host. If it cannot start on another
device, the launcher falls back to the vendor assets.

To rebuild and replace it after changing the server sources:

```sh
export OHOS_NDK=/path/to/command-line-tools/sdk/default/openharmony/native
make native-asset   # rebuild + strip + copy into internal/assets/device/
make dist           # rebuild Go binaries so go:embed picks up the new bytes
```

Or, from a clean dependency tree, one command:

```sh
export OHOS=/path/to/openharmony
export OHOS_NDK=/path/to/command-line-tools/sdk/default/openharmony/native
make dist-native    # dependency chain + native asset + all Go dist binaries
```

## Deploy and test

Build and push:

```sh
./native/server/build.sh
hdc -s <hdc-server> -t <serial> file send \
  native/server/build/hoscpy_server.z.so /data/local/tmp/hoscpy_server.z.so
hdc ... shell "chmod 755 /data/local/tmp/hoscpy_server.z.so"
```

`uitest start-daemon` without a test-server/apiCaller still stops at
`Failed to get apiCaller object from peer` / `Failed to initialize server`
before the extension runs; this environment is not a valid end-to-end uitest
test. Use the direct loader for the probe:

```sh
# Build runprobe for aarch64 OHOS
$OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang --sysroot=$OHOS_NDK/sysroot \
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
hdc -s <hdc-server> -t <serial> \
  fport tcp:39501 localabstract:scrcpy_grpc_socket
```

Then connect a gRPC client to the forwarded host/port, call
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
