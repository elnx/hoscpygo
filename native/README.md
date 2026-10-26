# Native device-side extension: reverse-engineering findings and signature observations

This directory documents and probes the device-side `libscreen_casting*.z.so`
that HOScrcpy pushes to `/data/local/tmp` and loads with:

```sh
/system/bin/uitest start-daemon singleness \
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

The gRPC layer is not obfuscated: the proto is the same one reconstructed in
`../proto/scrcpy.proto`. The hard part is the platform side: Rosen
virtual-screen system APIs (`ScreenManager::CreateVirtualScreen`) plus the OHOS
native media encoder, both system APIs rather than public NDK APIs.

## 2. Signature block: present, but not verified by the loader

### 2.1 The file does carry a Huawei signing block

`server_unix_6.5.z.so` is 268,469 bytes. Its ELF section table ends at 256,176;
after a 1,872-byte zero pad there is a 12,293-byte trailer. `openssl` parses the
main trailer object as a PKCS#7 `SignedData` (`sha256`, `pkcs7-data`), with:

```
signer subject: C=CN, O=Huawei, OU=Huawei CBG, CN=DevEcoTesting Release
issuer:         Huawei CBG Software Signing Service CA
root chain:     Huawei CBG Root CA G2
signingTime:    2026-03-13 09:36:42Z
digest:         SHA-256
signature:      ECDSA-SHA256
custom attr:    1.3.6.1.4.1.2011.2.376.1.4.1 = DEBUG_LIB_ID
```

So **yes, this is a Huawei code-signing container, not just a random certificate
bundle**. The agent `.so` has a similar signed trailer. We did not complete a
cryptographic verification of the detached manifest against the ELF in this
repository; the PKCS#7 structure and signer chain are confirmed, but do not read
this as "we verified every signed byte".

### 2.2 `uitest` itself does not call a verifier

analysis based on the OpenHarmony open-source implementation of `OHOS::uitest::ExecuteExtension` in `/system/bin/uitest`
shows the complete path:

```c
path = "/data/local/tmp/" + extension_name;
if (!FileExists(path)) { log("Client nativeCode not exist"); return false; }
handle = dlopen(path, RTLD_LAZY);          // (1)
if (!handle) { log("Dlopen %s failed: %s", dlerror()); return false; }
onInit = dlsym(handle, "UiTestExtension_OnInit");
onRun  = dlsym(handle, "UiTestExtension_OnRun");
if (onInit(...lowLevelFunctions, argc, argv) == 0) {
    onRun();
}
dlclose(handle);
```

There is **no signature/certificate/hash check in `ExecuteExtension`**. The
`EXTENSION_API_CHECK failed` string belongs to `PrintLog`,
`InitLowLevelFunctions`, and `WriteToBuffer` argument validation, not to code
signing. `uitest` has a `NEEDED libsec_shared.z.so`, but `ExecuteExtension` does
not call it. `/lib/ld-musl-aarch64.so.1` also has no code-signing strings in the
versions inspected; we have not found a verifier in the loader path.

**Therefore, the current OpenHarmony open-source implementation indicates that the uitest daemon does not
verify the extension signature.** The signature trailer is present, but the
previously claimed "signature is required and unsigned .so is rejected" is
**not supported** by the loader code.

### 2.3 Why the earlier unsigned/truncated experiments were inconclusive

Earlier notes in this directory claimed that unsigned/truncated `.so` files
failed because of signature verification. That conclusion was too strong. The
tests had confounders:

- A file pushed with mode 0644 produced
  `Dlopen ... failed: Permission denied`; `chmod 755` changes the result.
- The first minimal probe used the wrong entry ABI. The loader calls
  `OnInit(void *lowLevelFunctions, int argc, char **argv)`, not
  `OnInit(int argc, char **argv)`. The current `minimal_ext.c` uses the correct
  3-argument form.
- A standalone `uitest start-daemon singeness --extension-name ...` without a
  test server/apiCaller can log `StartDaemon: Failed to initialize server` and
  exit before a meaningful extension test. The vendor server happens to block
  inside `RpcServer::InitServer`, which can mask this ordering.
- The earlier truncated/patch tests were run in that incomplete environment, so
  they do not prove a signature check.

A proper unsigned-extension test needs mode 0755, the 3-argument `OnInit`, and a
clean `@uitest_socket` state. A direct `dlopen` probe (which follows the same
loader path minus the uitest apiCaller setup) now proves the unsigned library
loads and its symbols resolve:

```
/data/local/tmp/dlprobe /data/local/tmp/hoscpy_server.z.so
DLOK 0x...
INIT 0x... RUN 0x...
rc=0
```

A full self-built server (static protobuf/gRPC + public NDK screen capture) also
starts from `runprobe` and serves live frames over the abstract socket. See
[`server/README.md`](server/README.md). The standalone `uitest start-daemon`
path still needs a valid test-server/apiCaller environment, so that particular
daemon failure remains inconclusive, not a signature result.

The signature trailer's existence is confirmed. Its enforcement by a lower
layer was not found, and the direct loader is positive evidence that this
`uitest`/`ld-musl` path does not reject an unsigned file.

### 2.4 The HarmonyOS PC error is unrelated to signing

The PC failure:

```
Error relocating /data/local/tmp/hoscpy_server.z.so:
_ZN6google8protobuf8internal12MapFieldBase11SetMapDirtyEv: symbol not found
```

happens **after** `dlopen` has accepted the file, during ELF relocation. It is a
`libprotobuf.z.so` ABI mismatch, not a signature rejection. All bundled server
variants import that symbol. A self-built or newer compatible server would need
to match the PC's system gRPC/protobuf ABI regardless of signing.

## 3. Minimal probe

`minimal_ext.c` builds an aarch64 OHOS shared library exporting the two loader
symbols and a constructor, writing markers to `/data/local/tmp` and setting a
distinct process name. It uses the correct loader ABI:

```c
int UiTestExtension_OnInit(void *lowLevelFunctions, int argc, char **argv);
void UiTestExtension_OnRun(void);
```

Build:

```sh
./native/build.sh
```

Push and start only under a test environment that has the expected uitest
test-server/agent caller:

```sh
hdc file send native/build/libhoscpy_test.z.so /data/local/tmp/libhoscpy_test.z.so
hdc shell "chmod 755 /data/local/tmp/libhoscpy_test.z.so"
hdc shell "/system/bin/uitest start-daemon singeness \
  --extension-name libhoscpy_test.z.so >/dev/null 2>&1 &"
hdc shell "P=\$(pidof uitest); cat /proc/\$P/comm; grep hoscpy_test /proc/\$P/maps"
hdc shell "cat /data/local/tmp/hoscpy_probe_*.log"
```

A loaded probe changes its process name to `hoscpy_probe`, appears in
`/proc/<pid>/maps`, and writes the probe logs. If the daemon reports
`Failed to initialize server`, the test is **not** a valid signature test.

## 4. Self-built server: what is proven

A working probe now exists in [`server/`](server/README.md) and is embedded as
`internal/assets/device/server_static.z.so`; `VariantsFor` tries it before the
vendor unix-socket assets. It does **not** need the Rosen virtual-screen/system
headers:

- public `OH_AVScreenCapture` (`libnative_avscreen_capture.so`) captures the home
  screen into an encoder input surface;
- public `OH_VideoEncoder` (`libnative_media_venc.so`) emits H.264;
- protobuf 33.5 and gRPC 1.73 C++ are statically linked, so the `.so` has no
  `libprotobuf.z.so`/`libgrpc*.z.so` dependency and avoids the PC ABI error;
- the generic gRPC callback service implements the same
  `/ScrcpyService/onStart`, `/ScrcpyService/onEnd`, and
  `/ScrcpyService/onRequestIDRFrame` methods without generated service code;
- SPS/PPS are collected and prepended to the first IDR; live frames decode on
  the host (`h264`, Main, 2880x1920).

The remaining gaps are practical, not loader policy: display geometry is
hard-coded to the probed PC, the capture path is change-driven, one stream is
supported, and the full `uitest start-daemon` path needs a valid
test-server/apiCaller environment. The open-source `OpenHarmony_Scrcpy` server
remains a useful reference for capture/encoder details.
