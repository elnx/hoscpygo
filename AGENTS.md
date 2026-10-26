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
   carry an appended Huawei CBG PKCS#7 block, but OpenHarmony source shows `uitest`'s
   `ExecuteExtension` only calls `dlopen`/`dlsym`/`OnInit`/`OnRun`; no signature
   verifier is called. Do **not** assume signing is required or is the blocker:
   earlier unsigned/truncated tests were confounded by file mode, the
   `OnInit(void*, int, char**)` ABI, and a daemon that failed before extension
   load. Do not modify/re-sign vendor assets for distribution. A missing system
   symbol such as `MapFieldBase11SetMapDirty` is an OS ABI mismatch: use image
   mode or a compatible server asset. See `native/README.md`.
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
cmd/hoscpygo/main.go        flags, signal handling, browser open, server start
internal/assets             embed FS + hdc extraction/cache
internal/config             JSON config, defaults, server normalization
internal/hdc                exec.Command wrapper; no shell string splitting
internal/uitest             agent bootstrap, JSON-RPC, layout, JPEG stream
internal/scrcpy             server-variant selection, .so push/start, gRPC
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

1. `/system/bin/uitest --version` is read.
2. `scrcpy.VariantsFor` selects the asset family:
   - `> 6.0.2.1`: `server_unix_6.5` → `6.4` → `6.3.1`
     (abstract socket `scrcpy_grpc_socket`)
   - `<= 6.0.2.1`: `server_5.10` → `server3` → `server2` → `server1`
     (TCP `:5000`)
3. Kill any existing `hoscpy_server.z.so` process.
4. Extract the embedded asset to the host cache; compare md5 with
   `/data/local/tmp/hoscpy_server.z.so`; if different, `rm -f` remote then
   `hdc file send`.
5. `power-shell wakeup`.
6. Start detached:
   `/system/bin/uitest start-daemon singleness --extension-name hoscpy_server.z.so -scale S -frameRate F -bitRate B -p 5000 -iFrameInterval I`
   (`hdc.StartBackground` redirects stdout/stderr and appends `&`).
7. Wait for the process (`ps -ef`), then wait for the abstract socket in
   `/proc/net/unix` (or TCP `:5000` for legacy assets).
8. Allocate a host port and `hdc fport tcp:P localabstract:scrcpy_grpc_socket`
   (or `tcp:P tcp:5000`).
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

- Agent asset selected by `uitest --version`/x86 (`uitest.chooseAgent`), pushed
  as `/data/local/tmp/hoscpy_agent.so` (fallback `agent.so`).
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
- Mouse drag: `Gestures` `ButtonLeftDown/Move/Up`. Wheel: `uinput -M -m x y -s ±500`.
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
make build                 # bin/hoscpygo, CGO_ENABLED=0
make test                  # Go unit tests
make vet
make fmt
make proto                 # protoc + protoc-gen-go + protoc-gen-go-grpc
make dist                  # linux/amd64, darwin/amd64+arm64, windows/amd64
```

Before yielding any non-trivial change:

- `go test ./...`, `go vet ./...`, `gofmt -l .` empty.
- Run `./bin/hoscpygo -no-open -debug` and exercise the changed path.
- For video changes: connect with `mode:"video"`, wait for `streaming`, open
  `/ws`, confirm SPS/PPS + IDR, fetch `/layout` and `/screenshot`.
- For image changes: `mode:"image"`, confirm JPEG frames and layout/input.
- For hdc-resolution changes: put a fake `hdc` in a temp directory on `PATH`
  and verify `/api/health` reports it; with an empty `PATH`/`HDC`, startup must
  fail and print the installation hint.
- `strings -a bin/hoscpygo | grep -E 'hdc/(linux|darwin|windows)_'` must be empty;
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

Known timing constraints: variants can take ~10 s each; agent/layout first call
~2 s; do not "fix" slowness by removing socket/process waits.

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
- `server/`: unsigned static gRPC/protobuf server probe with public
  `OH_AVScreenCapture` + `OH_VideoEncoder`; its stripped build is embedded as
  `internal/assets/device/server_static.z.so` and selected first on unix-socket
  devices. See `server/README.md` for the verified build and limitations.
- `README.md`: reverse-engineering analysis, appended Huawei CBG signature evidence, and the
  unsigned/truncated/copied-signature experiments.

The embedded static server is still a prototype: display geometry is fixed to
the probed PC, capture is change-driven, and one stream is supported. The direct
loader proves an unsigned self-built `.so` can be loaded by the same `dlopen`
path; the full `uitest start-daemon` path was not demonstrated in the remote
probe environment, so keep the vendor assets as fallbacks.

## Licensing

MIT covers original Go/JS only. `internal/assets/device/*` are Huawei SDK
binaries; `jmuxer` is MIT. hdc is not bundled and is installed separately. Read `NOTICE` before changing distribution or embedding
additional binaries. Prefer runtime extraction from a user-provided SDK jar if
redistribution rights are unclear.
