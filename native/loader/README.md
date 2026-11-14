# Standalone root loader

`loader.c` is a 6 KB aarch64 launcher for the self-built
`server_static.z.so`. hoscpygo pushes it to `/data/local/tmp/hoscpy_loader`
and runs it as root when the hdc shell reports `uid=0` and `aarch64`. It is
embedded in the Go binary as `internal/assets/device/hoscpy_loader`.

## Why it exists

There are two independent blockers on newer devices:

1. `uitest`'s extension loader applies the platform XPM check. Our unsigned
   `server_static.z.so` is rejected with
   `Dlopen ...: Error loading shared library ...: Permission denied`, while
   signed vendor servers get past that check.
2. The signed vendor servers are built against older system ABIs. On
   OpenHarmony 7.1 every bundled variant dies in relocation:
   `google::protobuf::internal::MapFieldBase::SetMapDirty` is gone, and the
   `OHOS::Rosen::ScreenManager::{Create,Destroy}VirtualScreen` API family was
   replaced by `RSInterfaces::{Create,Remove}VirtualScreen`.

`server_static.z.so` links none of `libprotobuf`, `libgrpc`, or
`librender_service_base`; it captures through the public
`OH_AVScreenCapture`/`OH_VideoEncoder` NDK APIs. The only thing standing
between it and a working video stream is the XPM check on the signed `uitest`
process.

Starting our own process sidesteps both problems: the loader is an ordinary
unsigned executable, and the server has no version-sensitive system library
dependencies.

LD_PRELOAD is **not** an alternative. Experiments on OH 7.1 showed that the
loader honors `LD_PRELOAD` for `/bin/toybox` (the constructor runs and the
library appears in `/proc/<pid>/maps`) but ignores it for
`/system/bin/uitest` and `/system/bin/hilog`. Even if injection worked, the
missing symbols are not a shim-sized set: 14 protobuf internals (including
data symbols and the new `UntypedMapBase` implementation), the removed Rosen
virtual-screen family, and — for DevEco's recorder server — 21
`absl::lts_20220623` symbols. Interposing those would mean reimplementing the
old C++ ABIs on top of the new system libraries.

## What it does

The loader reproduces the only extension ABI `uitest` uses:

```c
void *handle = dlopen(argv[1], RTLD_NOW | RTLD_GLOBAL);
init = dlsym(handle, "UiTestExtension_OnInit");   // int (void *, int, char **)
run  = dlsym(handle, "UiTestExtension_OnRun");    // void (void)
init(NULL, argc - 2, argv + 2);                   // lowLevelFunctions = NULL
run();                                            // blocks in the server
```

The server's `UiTestExtension_OnInit` parses the same
`-scale/-frameRate/-bitRate/-p/-iFrameInterval` arguments as the extension
path, starts a detached gRPC thread on
`unix-abstract:scrcpy_grpc_socket`, and `UiTestExtension_OnRun` sleeps until
the process is killed. The process command line keeps
`/data/local/tmp/hoscpy_server.z.so`, so the host's existing ps-based cleanup
(pattern `hoscpy_server.z.so`) also kills the loader.

The loader refuses to run without `euid=0`; hoscpygo only selects this path
after the `id -u; uname -m` check.

## Build and install

The committed asset can be rebuilt with the OpenHarmony NDK (no submodules
required):

```sh
OHOS_NDK=$HOME/command-line-tools/sdk/default/openharmony/native make native-loader
```

Equivalent manual command:

```sh
$OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang \
  --target=aarch64-linux-ohos --sysroot=$OHOS_NDK/sysroot \
  -O2 -s -o internal/assets/device/hoscpy_loader native/loader/loader.c -ldl
```

Runtime layout on the device:

```text
/data/local/tmp/hoscpy_loader          <- this loader (0755)
/data/local/tmp/hoscpy_server.z.so     <- self-built server (0755)
/data/local/tmp/hoscpy_loader.log      <- loader stdout/stderr, truncated per start
```

Start command (what `startStandalone` runs):

```sh
/data/local/tmp/hoscpy_loader /data/local/tmp/hoscpy_server.z.so \
  -scale 2 -frameRate 60 -bitRate 8388608 -p 5000 -iFrameInterval 2000
```

## Verification

On a rooted OH 7.1 device:

```sh
hdc shell "rm -f /data/local/tmp/hoscpy_loader.log"
hdc shell "/data/local/tmp/hoscpy_loader /data/local/tmp/hoscpy_server.z.so \
  -scale 2 -frameRate 60 -bitRate 8388608 -iFrameInterval 2000 \
  >/data/local/tmp/hoscpy_loader.log 2>&1 &"
hdc shell "cat /data/local/tmp/hoscpy_loader.log"
hdc shell "cat /proc/net/unix | grep scrcpy_grpc_socket"
```

Expected log:

```text
init argc=8 argv=-scale 2 -frameRate 60 ... uid=0 size=1440x960
run
listening on unix-abstract:scrcpy_grpc_socket
```

`hdc fport tcp:<port> localabstract:scrcpy_grpc_socket` plus a gRPC
`ScrcpyService/onStart` call then yields Annex-B H.264 payloads.

## Limitations

- Requires `uid=0` in the hdc shell and an aarch64 device. Unrooted devices
  keep the uitest vendor path or image mode.
- On a non-root shell (`uid=2000(shell)`, `u:r:sh:s0`) the loader cannot even
  be executed: files in `/data/local/tmp` carry
  `u:object_r:data_local_tmp:s0` and the `sh` domain lacks execute permission.
  A copied signed `/system/bin/toybox` is denied the same way, as is invoking
  `/lib/ld-musl-aarch64.so.1` directly, so this is SELinux domain policy, not
  XPM or the binary's bytes. Such devices have no `su`; hoscpygo skips the
  standalone path via the `id -u` check and never reaches `dlopen`.
- SELinux enforcing builds may deny display capture from the root process
  context; the host then falls back to the extension variants.
- `server_static.z.so` is still the prototype described in
  [`../server/README.md`](../server/README.md): fixed capture geometry,
  change-driven frames, one stream.
- No secrets or network listener are added by the loader; it only executes an
  embedded local file.
