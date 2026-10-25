# Native device-side extension: reverse-engineering findings and the signing wall

This directory documents and probes the device-side `libscreen_casting*.z.so`
that HOScrcpy pushes to `/data/local/tmp` and loads with:

```sh
/system/bin/uitest start-daemon singeness \
  --extension-name libscreen_casting.z.so \
  -scale 2 -frameRate 60 -bitRate 8388608 -p 5000 -iFrameInterval 2000
```

## 1. reverse-engineering analysis of `server_unix_6.5-20260313.z.so`

Relevant exported functions:

| Symbol | Meaning |
|---|---|
| `UiTestExtension_OnInit` | loader entry; sets process name `xdevice_scrcpy`, parses argv pairs, calls `RpcServer::InitServer` |
| `UiTestExtension_OnRun` | `return 0` |
| `RpcServer::InitServer` | builds a `grpc::ServerBuilder`, listens on `unix-abstract:scrcpy_grpc_socket` (or `screen_record_grpc_socket` in record mode), registers `ScrcpyService`, blocks on a condition variable |
| `ScrcpyServiceImpl::onStart` | stores the server writer, calls `NativeStartScreenCopy()` |
| `ScreenEncoder::Setup` | Rosen `DisplayManager`/`ScreenManager`; disables power-off render control; resolves the display id |
| `ScreenEncoder::Run` | constructs a `VirtualScreenOption` named `scrcpy` (DPI/size), creates a virtual screen, configures the native H.264 encoder/surface |
| `ScreenRecorder` / `AudioRecorder` | optional record/audio paths; the gRPC proto only carries video `data` |

There is no Huawei-specific obfuscation in the gRPC layer: the proto is the same
one reconstructed in `../proto/scrcpy.proto`. The hard part is the platform
side: Rosen virtual-screen system APIs (`ScreenManager::CreateVirtualScreen`)
plus OHOS native media encoder, all of which are system APIs, not public NDK
APIs.

## 2. Signature requirement

The vendor `.so` files are **code-signed**. The signature is appended after the
ELF section table (not a regular section):

```
server_unix_6.5.z.so  268469 bytes, ELF section table ends at 256176;
                      +12293-byte tail containing a Huawei CBG PKCS#7 signature
uitest_agent_1.2.3.so 600246 bytes, ELF ends at 578424;
                      +21822-byte tail with the agent version marker + signature
```

The appended PKCS#7 contains the chain:

```
Huawei CBG Software Signing Service CA
Huawei CBG Root CA G2
```

Experiments on a retail HarmonyOS 7.0.0.17 device (user build) and a
rooted/eng 7.0.0.21 device:

| Artifact | Result |
|---|---|
| original signed vendor server | loads, binds `@scrcpy_grpc_socket`, streams |
| vendor server truncated to the ELF end (all code intact, signature removed) | process starts but never initializes/binds |
| minimal self-built OHOS NDK extension, unsigned | process starts but constructor/entry never runs |
| self-built extension + copied vendor signature tail | same failure |
| self-built extension on root/eng device | same failure |

The loader `uitest` itself dlsyms `UiTestExtension_OnInit` and
`UiTestExtension_OnRun` and logs `Dlopen`/`Dlsym` failures; signature rejection
is **silent** (no normal Dlopen error), which is consistent with verification
below the loader. Appending the vendor signature to a different ELF also fails,
so the check is cryptographic, not presence-only.

**Conclusion:** on HarmonyOS 6/7, the uitest extension path requires a valid
Huawei signature over the exact ELF. Root does not bypass it on the tested eng
device. Self-written C++ can be built, but cannot be deployed to a retail
device through this loader without Huawei signing (or a device with verification
disabled/patched).

Practical options:

1. **Reuse the signed vendor assets** and rewrite only the host (this project).
2. Obtain official Huawei signing/partner access for your own extension.
3. For rooted OpenHarmony devices, install a self-built server as a system
   binary (the `OpenHarmony_Scrcpy` approach) instead of using the uitest
   extension loader.
4. Do not plan a pure-Go device-side replacement: Go cannot produce a signed
   OHOS uitest extension, and the capture APIs are native.

## 3. Minimal probe

`minimal_ext.c` builds an aarch64 OHOS shared library exporting the two loader
symbols and a constructor, writing markers to `/data/local/tmp`. It proves
whether an unsigned extension is accepted.

```sh
./native/build.sh
hdc file send native/build/libhoscpy_test.z.so /data/local/tmp/libhoscpy_test.z.so
hdc shell "/system/bin/uitest start-daemon singeness \
  --extension-name libhoscpy_test.z.so >/dev/null 2>&1 &"
hdc shell "ls -l /data/local/tmp/hoscpy_probe_*.log"
```

On the tested devices no probe log and no abstract socket appear, while the
original signed vendor asset works. The source remains useful as a build/loader
probe if you obtain signing.

## 4. What a rewrite would require

A full C++ reimplementation (if signing is available) would need:

- OHOS full-source headers for `OHOS::Rosen::ScreenManager`,
  `VirtualScreenOption`, `DisplayManager` (not in the public NDK), or the public
  `OH_AVScreenCapture` API with appropriate system permissions;
- native media encoder (`libnative_media_venc`, `libnative_media_core`,
  `libsurface`) and a surface from the virtual screen;
- the gRPC/protobuf service from `../proto/scrcpy.proto`;
- the same argv parameter parsing and abstract-socket naming;
- Huawei code signing and per-OS-version ABI testing.

The open-source OpenHarmony `OpenHarmony_Scrcpy` server is a useful reference for
the capture/encoder plumbing, but it installs to `/system/bin` and requires a
root/OpenHarmony image.
