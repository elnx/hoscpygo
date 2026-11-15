# AGENTS.md

Operational instructions for coding agents working on **hoscpygo**.

## What this project is

A single-binary Go server that mirrors and controls HarmonyOS/OpenHarmony
devices from a browser. The Go process is the host only:

- device transport: hdc (`shell`, `file send`, `fport`)
- control/layout/JPEG: the Huawei-signed `uitest_agent` loaded by `uitest`
- H.264: the Huawei-signed `libscreen_casting` extension loaded by `uitest`,
  consumed over gRPC
- decoding/rendering: the browser (WebCodecs, MSE/jmuxer fallback)

There is **no** CGO, JVM, JavaCV, FFmpeg, npm, or CDN at runtime. The web UI and
device-side assets are in `embed.FS`; hdc is an external host prerequisite.
Keep `CGO_ENABLED=0` and one application binary.

## Golden rules

1. **Never reimplement `libscreen_casting*.z.so` in Go.** It is a native OHOS
   system extension using `OHOS::Rosen` virtual-screen APIs and the native media
   encoder. It is not portable to Go and cannot run in the Go process.
2. **Treat vendor device assets as signed third-party binaries.** Their files
   carry an appended Huawei CBG PKCS#7 block, but the OpenHarmony
   `testfwk_arkxtest` source shows `uitest`'s `ExecuteExtension` only calls
   `dlopen`/`dlsym`/`OnInit`/`OnRun`; no signature verifier is called there.
   The loader itself (ld-musl/XPM) still rejects the unsigned self-built
   server inside the signed `uitest` domain with `Permission denied` on
   production devices; do not confuse that with the earlier confounded
   unsigned tests (file mode, `OnInit(void*, int, char**)`, incomplete daemon).
   Do not modify or re-sign vendor assets for distribution. A missing system
   symbol such as `MapFieldBase11SetMapDirty` is an OS ABI mismatch, not a
   signature issue. The supported escape hatch for locked/rooted devices is
   the standalone loader (`native/loader/`), then vendor fallback/image mode.
   See `native/README.md`.
3. **Do not add CGO or host-side decoders.** H.264/JPEG decode belongs to the
   browser. Keep `CGO_ENABLED=0` in every build target.
4. **Keep protocol constants exact.** The agent frame magic is `1145141919`,
   not a session id. The gRPC service name is `ScrcpyService` with no package.
5. **Create an `hdc fport` only after the device abstract socket exists.**
   Otherwise the forward silently points at nothing. This applies to both
   `scrcpy_grpc_socket` and `uitest_socket`.
6. **Kill an extension before replacing its `.so`.** A running process keeps the
   file mapped; `hdc file send` then fails with `operation not permitted`
   (ETXTBSY). Also `rm -f` the remote file before sending.
7. **Use targeted process kills.** `kill -9` only processes matching the exact
   extension name/pattern; do not kill all `uitest`/Hypium sessions unless the
   user explicitly wants cleanup. Keep the device names
   `hoscpy_server.z.so` / `hoscpy_agent.so`: they avoid overwriting HOScrcpy's
   `libscreen_casting.z.so` and Hypium's default `agent.so`. Huawei signatures
   cover file content, not the filename.
8. **No secrets, no hidden network calls.** The UI is fully embedded; do not add
   CDN/external asset URLs. The server binds localhost by default and has no
   authentication.
9. **hdc is external.** Do not embed hdc/libusb. Resolve `-hdc`/config → `$HDC`
   → `PATH`; if no system hdc is found, fail with the installation hint. This
   keeps the binary small and lets hdc be updated independently.

## Architecture / file map

```
cmd/hoscpy/main.go           flags, signal handling, browser open, server start
internal/assets             embed FS + hdc extraction/cache
internal/config             JSON config, defaults, server normalization
internal/hdc                exec.Command wrapper; no shell string splitting
internal/uitest             agent bootstrap, JSON-RPC, layout, JPEG stream
internal/scrcpy             server-variant selection, .so push/start, gRPC,
                            standalone root loader (standalone.go)
internal/session            per-device state, video/image lifecycle, input,
                            layout, screenshot, fan-out hub
internal/webui              HTTP API + WebSocket + embedded UI
internal/proto              generated scrcpy proto stubs
internal/layout             layout JSON parse/bounds/search
internal/assets/web         vanilla JS UI (WebCodecs + jmuxer fallback)
proto/scrcpy.proto          source of truth; regenerate with `make proto`
native/                     loader/signature findings, minimal probe, static server PoC
```

One `Session` per `server/sn`, multiple WebSocket subscribers per session.
Frames fan out through `session.Hub`; slow clients drop binary frames, and
JSON state messages are best-effort (they may also be dropped if a client
buffer is full).

## `libscreen_casting` handling in Go (the important part)

The Go video path is orchestration, not capture. Exact flow:

0. If `hdc shell "id -u; uname -m"` reports `0` and `aarch64`, try the
   standalone backend first (`internal/scrcpy/standalone.go`): push
   `server_static.z.so` and the embedded `hoscpy_loader`, then start detached
   `/data/local/tmp/hoscpy_loader /data/local/tmp/hoscpy_server.z.so <args>`
   with stdout/stderr in `/data/local/tmp/hoscpy_loader.log`. It uses the same
   `scrcpy_grpc_socket`; on failure continue with the variants below. The
   loader (`native/loader/loader.c`) is an ordinary unsigned process, so it
   avoids XPM rejection and the vendor protobuf/Rosen ABI mismatch. Do not
   replace it with LD_PRELOAD: the OHOS loader ignores preloads for `uitest`.
1. `/system/bin/uitest --version` is read.
2. `scrcpy.VariantsFor` supports current unix-socket systems:
   - `server_static.z.so` first, then `server_unix_6.5` → `6.4` → `6.3.1`
   - legacy TCP and old/x86 agent assets are not embedded
   - unsigned static is rejected by XPM in the uitest domain on some H7
     production devices; the standalone step above, the vendor fallback, or
     image mode is used there
3. Kill any existing `hoscpy_server.z.so` process (this also matches the
   standalone loader because its argv contains the server path).
4. Extract the embedded asset to the host cache; compare md5 with
   `/data/local/tmp/hoscpy_server.z.so`; if different, `rm -f` remote then
   `hdc file send`.
5. `power-shell wakeup`.
6. Start detached:
   `/system/bin/uitest start-daemon singleness --extension-name hoscpy_server.z.so -scale S -frameRate F -bitRate B -p 5000 -iFrameInterval I`
   (`hdc.StartBackground` redirects stdout/stderr and appends `&`).
7. Wait for the process (`ps -ef`), then wait for the abstract socket in
   `/proc/net/unix`.
8. Allocate a host port and `hdc fport tcp:P localabstract:scrcpy_grpc_socket`.
9. gRPC plaintext to `host:P`, max recv 100 MiB:
   `ScrcpyService/onStart`, read `ReplyMessage.payload["data"].val_bytes`
   (Annex-B H.264). The service has no audio.
10. After 400 ms, call `onRequestIDRFrame`. On every new browser subscriber,
    send `{"type":"video-reset"}` and call `onRequestIDRFrame` again; the
    browser must not feed pre-keyframe deltas to the decoder.
11. Stop: `onEnd` (2 s), close the connection, kill the process, remove the
    fport. On any failure, try the next variant. `auto` mode then falls back to
    the JPEG agent path; `video` mode reports the error.

The `.so` itself is opaque. Do not add host-side parsing of its internals;
only the wire protocol is public here. If a new OS version breaks the asset,
the correct fix is a compatible vendor asset (or the self-built static server
probe in `native/server/`) plus a variant entry, not a Go rewrite.

## uitest agent path (control/layout/image)

- Agent asset is always `agent_1.2.3.so` (`uitest.chooseAgent`), pushed as
  `/data/local/tmp/hoscpy_agent.so` (fallback `agent.so`). Old/x86 agents are
  not embedded.
- Start detached with `--extension-name hoscpy_agent.so`; wait for
  `@uitest_socket` before creating three fports (control/layout/capture).
- Control socket: raw JSON, no framing.
- Layout socket: `_uitestkit_rpc_message_head_` + magic `1145141919` +
  u32 length + JSON + `_uitestkit_rpc_message_tail_`; unwrap `result`.
- Capture socket: raw `Captures.startCaptureScreen`; skip the JSON ack and parse
  JPEG SOI/EOI. The stream is change-driven; a static screen may never paint.
  Seed the UI with `/screenshot` when image mode starts.
- Touch semantics: Hypium `callHypiumApi`/`Driver.click|swipe|longClick`.
  `Gestures.touchDown/Move/Up` is a no-op on H7; do not switch back to it.
- Mouse buttons: `Gestures` `ButtonLeft/Right/MiddleDown/Move/Up`. The UI maps
  pointer buttons 0/1/2 and forwards right-click; in mouse mode `contextmenu`
  must not send Back. Wheel: `uinput -M -m x y -s ±500`.
- Keys: `uinput -K -d/-u`; text: `uitest uiInput text` with shell quoting
  (`hdc.Quote`).
- Keep the agent running by default (`KeepAgent=true`); remove fports on session
  close. Do not kill a shared agent unless `KeepAgent=false`/explicit cleanup.

## Config and runtime

- Config: `os.UserConfigDir()/hoscpygo/config.json`; override with
  `HOSCPYGO_CONFIG` or `-config`.
- Cache for extracted device assets: `os.UserCacheDir()/hoscpygo` (hdc is not cached).
- `hdc` resolution order: `-hdc`/config → `$HDC` → `PATH`. There is no
  embedded fallback: a missing hdc is a startup error with installation
  guidance.
- `-s host[:port]` is a runtime-only hdc server override. It suppresses the
  local `127.0.0.1:8710` plus configured `remote_servers` list, so
  `hdc list targets` and every subsequent command use only that server. It is
  not saved to the config file.
- Defaults: mode `auto`, scale 2, 60 fps, 8 Mbps, I-frame 2000 ms, image scale
  0.5, listen `127.0.0.1:8899`.
- Bitrate on the device command is **bits/s**: `BitRateMbps << 20`.

## Build / test

```bash
make build                 # bin/hoscpy, CGO_ENABLED=0
make test                  # Go unit tests
make vet
make fmt
make proto                 # protoc + protoc-gen-go + protoc-gen-go-grpc
make dist                  # linux/amd64, darwin/amd64+arm64, windows/amd64
OHOS_NDK=... make native-loader  # rebuild the committed loader asset
```

Before yielding any non-trivial change:

- `go test ./...`, `go vet ./...`, `gofmt -l .` empty.
- Run `./bin/hoscpy -no-open -debug` and exercise the changed path.
- For video changes: connect with `mode:"video"`, wait for `streaming`, open
  `/ws`, confirm SPS/PPS + IDR, fetch `/layout` and `/screenshot`.
- For root/standalone changes: on a device whose `hdc shell "id -u; uname -m"`
  is `0`/`aarch64`, `mode:"video"` must log
  `variant=standalone:server_static.z.so` and reach `streaming`; after
  disconnect there must be no `hoscpy_loader` process and no scrcpy fport.
- For image changes: `mode:"image"`, confirm JPEG frames and layout/input.
- For hdc-resolution changes: put a fake `hdc` in a temp directory on `PATH`
  and verify `/api/health` reports it; with an empty `PATH`/`HDC`, startup must
  fail and print the installation hint.
- `strings -a bin/hoscpy | grep -E 'hdc/(linux|darwin|windows)_'` must be empty;
  no hdc binaries are embedded.
- Cross-compile after changing imports/OS-specific code.

Device smoke helpers (use only on test devices; destructive cleanup is opt-in):

```bash
hdc list targets
hdc -t <sn> shell "/system/bin/uitest --version"
hdc -t <sn> shell "cat /proc/net/unix | grep -E 'scrcpy|uitest'"
# destructive cleanup: stops every uitest daemon, including Hypium
pids=$(hdc -t <sn> shell "pidof uitest" | tr -d '\r')
hdc -t <sn> shell "kill -9 $pids"
hdc -t <sn> fport ls
hdc -t <sn> fport rm tcp:<port> localabstract:<socket>
```

Known timing constraints: a loader rejection aborts a variant in ~1-2 s
(the daemon exits within ~0.5 s and its hilog line is checked in parallel with
the process poll), a loaded server that never paints is cut off after
`firstFrameTimeout` (3 s), and the agent/layout first call takes ~2 s; do not
"fix" slowness by removing socket/process waits. The loader check must stay
tag-filtered (`hilog -x -T UiTestKit_Addon`): an unfiltered `hilog -x` dump
costs ~3.5 s and hilog's `-e` regex ~13 s on a device with a large buffer.

## Frontend

- Vanilla ES modules-ish script, no bundler, no external imports. Keep it
  embedded.
- WebCodecs is primary; `jmuxer`/MSE is fallback. Feed AVCC chunks with an
  avcC `description`; never feed a delta before the first keyframe.
- Canvas fit/letterbox and pointer→device mapping must use the **full device
  size** (`state.width/height`), not the scaled video width, or input is off by
  the scale factor.
- Keep the last frame on static screens; request an IDR on subscription/resize.
- Preserve `?session=` and `?sn=` attach shortcuts.

## Native directory

`native/` is research only:

- `minimal_ext.c` / `build.sh`: OHOS NDK aarch64 extension probe.
- `dlprobe.c`: direct `dlopen`/`dlsym` probe for the loader ABI.
- `loader/`: standalone root launcher (`loader.c`, `README.md`) embedded as
  `internal/assets/device/hoscpy_loader`; it runs the static server outside
  the signed `uitest` domain, where XPM does not reject it.
- `server/`: unsigned static gRPC/protobuf server probe with public
  `OH_AVScreenCapture` + `OH_VideoEncoder`; its stripped build is embedded as
  `internal/assets/device/server_static.z.so` and selected first on unix-socket
  devices. See `server/README.md` for the verified build and limitations.
- `server/ohos-src/`: seven pinned OpenHarmony third_party submodules. Only the
  native Make targets initialize/build them; `make dist` uses the embedded
  `server_static.z.so` and does not require submodules.
- `README.md`: reverse-engineering findings, appended Huawei CBG signature
  evidence, the unsigned/truncated/copied-signature experiments, and the
  standalone loader rationale.

The embedded static server is still a prototype: the host passes the probed
display geometry to it (portrait phones included), mid-stream rotation needs a
restart, capture is change-driven, and one stream is supported. The standalone
loader is what makes it usable on rooted OH 7.1 devices; keep the vendor assets
as fallbacks for unrooted devices.

## Licensing

MIT covers original Go/JS only. `internal/assets/device/*` are Huawei SDK
binaries; `jmuxer` is MIT. hdc is not bundled and is installed separately. Read `NOTICE` before changing distribution or embedding
additional binaries. Prefer runtime extraction from a user-provided SDK jar if
redistribution rights are unclear.
