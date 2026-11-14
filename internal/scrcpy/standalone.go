package scrcpy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"hoscpygo/internal/hdc"
)

const (
	// LoaderAsset is the embedded aarch64 launcher built from
	// native/loader/loader.c. It is executed as root, dlopens the unsigned
	// self-built server and calls its uitest entry points.
	LoaderAsset = "hoscpy_loader"
	// loaderName is the on-device filename used by ps-based cleanup and the
	// start command.
	loaderName  = "hoscpy_loader"
	loaderPath  = "/data/local/tmp/" + loaderName
	loaderLog   = "/data/local/tmp/hoscpy_loader.log"
	loaderStart = 3 * time.Second
)

// standaloneAvailable reports whether the hdc shell runs as root on an aarch64
// device. Both are required: the loader asset is aarch64-only, and bypassing
// uitest's XPM check relies on running our own process as root.
func standaloneAvailable(ctx context.Context, h *hdc.Client, server, sn string) bool {
	out, err := h.Shell(ctx, server, sn, "id -u; uname -m", 5*time.Second)
	if err != nil {
		return false
	}
	return standaloneTarget(out)
}

// standaloneTarget parses the "id -u; uname -m" output.
func standaloneTarget(out string) bool {
	fields := strings.Fields(out)
	if len(fields) < 2 || fields[0] != "0" {
		return false
	}
	arch := strings.ToLower(fields[1])
	return arch == "arm64" || strings.Contains(arch, "aarch64")
}

// startStandalone pushes the self-built server and the loader, starts the
// loader as root, and connects to the same gRPC socket the extension path
// uses.
func startStandalone(ctx context.Context, opts Options) (*Stream, error) {
	// Stop any previous server/loader before replacing the files: a running
	// process keeps the .so mapped and hdc file send then fails with ETXTBSY.
	killServer(ctx, opts)
	if err := pushFile(ctx, opts, staticAsset, "/data/local/tmp/"+ExtensionName); err != nil {
		return nil, err
	}
	if err := pushFile(ctx, opts, LoaderAsset, loaderPath); err != nil {
		return nil, err
	}
	_, _ = opts.HDC.Shell(ctx, opts.Server, opts.SN, "power-shell wakeup", 5*time.Second)
	args := fmt.Sprintf("-scale %d -frameRate %d -bitRate %d -p %d -iFrameInterval %d",
		opts.Scale, opts.FrameRate, opts.BitRateMbps<<20, devicePort, opts.IFrameInterval)
	cmd := fmt.Sprintf("%s /data/local/tmp/%s %s", loaderPath, ExtensionName, args)
	// Keep the loader output: dlopen/entry failures happen before the server
	// logs anything to hilog or its own file.
	if err := opts.HDC.StartBackgroundTo(ctx, opts.Server, opts.SN, cmd, loaderLog); err != nil {
		killLoader(ctx, opts)
		return nil, fmt.Errorf("start loader: %w", err)
	}
	if !waitForProcess(ctx, opts.HDC, opts.Server, opts.SN, loaderName, loaderStart) {
		detail := recentLoaderLog(ctx, opts)
		killLoader(ctx, opts)
		return nil, fmt.Errorf("loader did not start%s", detail)
	}
	// The loader does not log through hilog, so only the process and socket are
	// watched here. Its own log supplies the failure detail.
	if _, err := waitForServerReady(ctx, opts.HDC, opts.Server, opts.SN, loaderName, "scrcpy_grpc_socket", true, "", false); err != nil {
		detail := recentLoaderLog(ctx, opts)
		killLoader(ctx, opts)
		return nil, fmt.Errorf("%w%s", err, detail)
	}
	return connectStream(ctx, opts, Meta{Variant: "standalone:" + staticAsset, Unix: true})
}

// killLoader targets only the standalone loader by its on-device name.
func killLoader(ctx context.Context, opts Options) {
	pids := opts.HDC.PIDs(ctx, opts.Server, opts.SN, loaderName)
	opts.HDC.KillPIDs(ctx, opts.Server, opts.SN, pids)
}

// recentLoaderLog returns the last lines of the loader output as a
// ": line | line" suffix, or "" when the log is empty or unavailable.
func recentLoaderLog(ctx context.Context, opts Options) string {
	out, err := opts.HDC.Shell(ctx, opts.Server, opts.SN, "tail -n 3 "+loaderLog+" 2>/dev/null", 5*time.Second)
	lines := strings.TrimSpace(out)
	if err != nil || lines == "" {
		return ""
	}
	return ": " + strings.Join(strings.Split(lines, "\n"), " | ")
}

func waitForProcess(ctx context.Context, h *hdc.Client, server, sn, pattern string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(h.PIDs(ctx, server, sn, pattern)) > 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}
