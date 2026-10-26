// Package scrcpy drives the device-side H.264 capture extension and consumes
// its gRPC stream.
package scrcpy

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"hoscpygo/internal/assets"
	"hoscpygo/internal/config"
	"hoscpygo/internal/hdc"
	pb "hoscpygo/internal/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// ExtensionName is the filename we push to /data/local/tmp. It is
	// deliberately different from HOScrcpy's libscreen_casting.z.so so the two
	// tools do not overwrite or kill each other's extension. The vendor bytes
	// are unchanged; Huawei's signature covers file content, not the filename.
	ExtensionName = "hoscpy_server.z.so"
	devicePort    = 5000
)

var versionRe = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){1,3}`)

// Variant is one device-side server binary.
type Variant struct {
	Asset string
	Unix  bool
}

// VariantsFor selects the server family from the device uitest version.
//
// Asset names are normalized from the vendor's dated filenames, e.g.
// libscrcpy_server_unix_6.5-20260313.z.so -> server_unix_6.5.z.so, so the
// selection table and cache keys stay stable across SDK rebuilds. Use the MD5
// in native/README.md / git history for provenance.
func VariantsFor(uitestVersion string) []Variant {
	unixSet := []Variant{
		{Asset: "server_unix_6.5.z.so", Unix: true},
		{Asset: "server_unix_6.4.z.so", Unix: true},
		{Asset: "server_unix_6.3.1.z.so", Unix: true},
	}
	legacySet := []Variant{
		{Asset: "server_5.10.z.so", Unix: false},
		{Asset: "server3.z.so", Unix: false},
		{Asset: "server2.z.so", Unix: false},
		{Asset: "server1.z.so", Unix: false},
	}
	if uitestVersion == "" {
		return append(unixSet, legacySet...)
	}
	if cmpVersion(uitestVersion, "6.0.2.1") > 0 {
		return append(unixSet, legacySet...)
	}
	return append(legacySet, unixSet...)
}

func cmpVersion(a, b string) int {
	pa, pb := parseVersion(a), parseVersion(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x > y {
			return 1
		}
		if x < y {
			return -1
		}
	}
	return 0
}

func parseVersion(s string) []int {
	raw := versionRe.FindString(s)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		v, _ := strconv.Atoi(p)
		out = append(out, v)
	}
	return out
}

// Options configures one video session.
type Options struct {
	HDC            *hdc.Client
	Server         string
	SN             string
	Scale          int
	FrameRate      int
	BitRateMbps    int
	IFrameInterval int
	Debug          bool
}

// Meta describes the negotiated stream.
type Meta struct {
	Variant string
	Port    int
	Unix    bool
}

// Stream is a live H.264 stream.
type Stream struct {
	Frames <-chan []byte
	Errors <-chan error
	Meta   Meta

	client    pb.ScrcpyServiceClient
	closeOnce sync.Once
	closeFn   func()
}

// RequestIDR asks the device encoder for an immediate keyframe. A browser that
// joins an existing stream needs this because the initial IDR may already have
// been consumed.
func (s *Stream) RequestIDR(ctx context.Context) error {
	if s.client == nil {
		return errors.New("stream has no client")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.client.OnRequestIDRFrame(ctx, &pb.Empty{})
	return err
}

func (s *Stream) Close() {
	s.closeOnce.Do(func() {
		if s.closeFn != nil {
			s.closeFn()
		}
	})
}

// Start starts the best available device server and waits for the first frame.
func Start(ctx context.Context, opts Options) (*Stream, error) {
	if opts.Scale < 1 || opts.Scale > 5 {
		opts.Scale = 2
	}
	if opts.FrameRate <= 0 {
		opts.FrameRate = 60
	}
	if opts.BitRateMbps <= 0 {
		opts.BitRateMbps = 8
	}
	if opts.IFrameInterval <= 0 {
		opts.IFrameInterval = 2000
	}
	uitestVer := detectUitestVersion(ctx, opts.HDC, opts.Server, opts.SN)
	slog.Info("selecting video server", "sn", opts.SN, "uitest", uitestVer)
	var errs []string
	for _, v := range VariantsFor(uitestVer) {
		s, err := startVariant(ctx, opts, v)
		if err == nil {
			return s, nil
		}
		slog.Warn("video server variant failed", "sn", opts.SN, "asset", v.Asset, "err", err)
		errs = append(errs, v.Asset+": "+err.Error())
	}
	return nil, fmt.Errorf("all video server variants failed: %s%s", strings.Join(errs, " | "), videoFailureHint(errs))
}

// videoFailureHint turns the common device-side loader failure into an
// actionable message. A signed server .so built against a different system
// protobuf/grpc ABI cannot be fixed on the Go side.
func videoFailureHint(errs []string) string {
	blob := strings.ToLower(strings.Join(errs, "\n"))
	if strings.Contains(blob, "error relocating") && strings.Contains(blob, "symbol not found") {
		return " (device-side video server is incompatible with this device OS/ABI; use image mode or obtain a compatible HOScrcpy server asset)"
	}
	if strings.Contains(blob, "dlopen") && strings.Contains(blob, "failed") {
		return " (device-side video server failed to load; check the OS version and signed asset compatibility)"
	}
	return ""
}

func detectUitestVersion(ctx context.Context, h *hdc.Client, server, sn string) string {
	out, err := h.Shell(ctx, server, sn, "/system/bin/uitest --version", 6*time.Second)
	if err != nil {
		return ""
	}
	m := versionRe.FindAllString(out, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

func startVariant(ctx context.Context, opts Options, v Variant) (*Stream, error) {
	// Stop any previous extension before replacing the .so: a running process
	// keeps the file mapped, and hdc file send then fails with ETXTBSY.
	killServer(ctx, opts)
	if err := pushServer(ctx, opts, v); err != nil {
		return nil, err
	}
	_, _ = opts.HDC.Shell(ctx, opts.Server, opts.SN, "power-shell wakeup", 5*time.Second)
	cmd := fmt.Sprintf("/system/bin/uitest start-daemon singleness --extension-name %s -scale %d -frameRate %d -bitRate %d -p %d -iFrameInterval %d &",
		ExtensionName, opts.Scale, opts.FrameRate, opts.BitRateMbps<<20, devicePort, opts.IFrameInterval)
	if err := opts.HDC.StartBackground(ctx, opts.Server, opts.SN, cmd); err != nil {
		return nil, fmt.Errorf("start extension: %w", err)
	}
	if !waitForProcess(ctx, opts.HDC, opts.Server, opts.SN, ExtensionName, 6*time.Second) {
		hilog := recentLoadError(ctx, opts.HDC, opts.Server, opts.SN, ExtensionName)
		killServer(ctx, opts)
		return nil, fmt.Errorf("extension did not start%s", hilog)
	}
	// The server binds its socket a little after the process appears. An hdc
	// fport created before the target exists can end up permanently pointing at
	// nothing, so wait for the socket itself.
	socketName := "scrcpy_grpc_socket"
	if !v.Unix {
		socketName = "tcp:5000"
	}
	if !waitForServerSocket(ctx, opts.HDC, opts.Server, opts.SN, socketName, v.Unix, 8*time.Second) {
		hilog := recentLoadError(ctx, opts.HDC, opts.Server, opts.SN, ExtensionName)
		killServer(ctx, opts)
		return nil, fmt.Errorf("server socket did not appear%s", hilog)
	}
	port, err := allocPort(opts.Server)
	if err != nil {
		killServer(ctx, opts)
		return nil, err
	}
	target := "tcp:5000"
	if v.Unix {
		target = "localabstract:scrcpy_grpc_socket"
	}
	if err := opts.HDC.Fport(ctx, opts.Server, opts.SN, fmt.Sprintf("tcp:%d", port), target); err != nil {
		killServer(ctx, opts)
		return nil, fmt.Errorf("fport: %w", err)
	}
	cleanupPort := func() {
		_ = opts.HDC.RemoveFport(context.Background(), opts.Server, opts.SN, fmt.Sprintf("tcp:%d", port), target)
	}
	host := hdc.TargetHost(opts.Server)
	if host == "" {
		host = "127.0.0.1"
	}
	targetAddr := net.JoinHostPort(host, strconv.Itoa(port))
	stream, client, conn, err := openStream(ctx, targetAddr, v, port)
	if err != nil {
		killServer(ctx, opts)
		cleanupPort()
		return nil, err
	}
	// Ask for an immediate keyframe; static screens otherwise stay blank.
	go func() {
		select {
		case <-time.After(400 * time.Millisecond):
			_, _ = client.OnRequestIDRFrame(context.Background(), &pb.Empty{})
		case <-ctx.Done():
		}
	}()
	stream.closeFn = func() {
		ctxEnd, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = client.OnEnd(ctxEnd, &pb.Empty{})
		cancel()
		_ = conn.Close()
		killServer(context.Background(), opts)
		cleanupPort()
	}
	return stream, nil
}

func pushServer(ctx context.Context, opts Options, v Variant) error {
	data, err := assets.Device(v.Asset)
	if err != nil {
		return fmt.Errorf("embedded %s: %w", v.Asset, err)
	}
	local, err := writeCache(v.Asset, data)
	if err != nil {
		return err
	}
	remote := "/data/local/tmp/" + ExtensionName
	localMD5 := md5Hex(data)
	if opts.HDC.MD5(ctx, opts.Server, opts.SN, remote) == localMD5 {
		return nil
	}
	_, _ = opts.HDC.Shell(ctx, opts.Server, opts.SN, "rm -f "+remote, 8*time.Second)
	if err := opts.HDC.FileSend(ctx, opts.Server, opts.SN, local, remote); err != nil {
		return fmt.Errorf("push server: %w", err)
	}
	return nil
}

func writeCache(name string, data []byte) (string, error) {
	dir := filepath.Join(config.CacheDir(), "device")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if st, err := os.Stat(p); err == nil && st.Size() == int64(len(data)) {
		return p, nil
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func openStream(ctx context.Context, addr string, v Variant, port int) (*Stream, pb.ScrcpyServiceClient, *grpc.ClientConn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(100<<20)),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("grpc dial %s: %w", addr, err)
	}
	client := pb.NewScrcpyServiceClient(conn)
	streamCtx, streamCancel := context.WithCancel(context.Background())
	stream, err := client.OnStart(streamCtx, &pb.Empty{})
	if err != nil {
		_ = conn.Close()
		streamCancel()
		return nil, nil, nil, fmt.Errorf("grpc onStart: %w", err)
	}
	frames := make(chan []byte, 8)
	errs := make(chan error, 1)
	first := make(chan struct{})
	var once sync.Once
	closeFirst := func() { once.Do(func() { close(first) }) }
	go func() {
		defer streamCancel()
		for {
			msg, err := stream.Recv()
			if err != nil {
				if streamCtx.Err() == nil {
					select {
					case errs <- err:
					default:
					}
				}
				closeFirst()
				return
			}
			pv := msg.GetPayload()["data"]
			if pv == nil {
				continue
			}
			b := pv.GetValBytes()
			if len(b) == 0 {
				continue
			}
			closeFirst()
			select {
			case frames <- b:
			default:
				// Drop a stale frame; the next IDR will resync the decoder.
			}
		}
	}()
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		_ = conn.Close()
		streamCancel()
		return nil, nil, nil, errors.New("no video frame within 10s")
	case <-ctx.Done():
		_ = conn.Close()
		streamCancel()
		return nil, nil, nil, ctx.Err()
	}
	// The first frame may have been dropped if frames was full; refill it by
	// waiting for the next message is handled by the caller. In practice the
	// channel is empty at this point.
	s := &Stream{Frames: frames, Errors: errs, Meta: Meta{Variant: v.Asset, Port: port, Unix: v.Unix}, client: client}
	// Close must also cancel the stream context, so wrap the existing closeFn.
	prevClose := s.closeFn
	s.closeFn = func() {
		streamCancel()
		if prevClose != nil {
			prevClose()
		}
	}
	return s, client, conn, nil
}

func allocPort(server string) (int, error) {
	host := hdc.TargetHost(server)
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		return hdc.FreeLocalPort()
	}
	for range 30 {
		p := 36000 + rand.IntN(1000)
		if hdc.RemotePortFree(host, p) {
			return p, nil
		}
	}
	return 0, errors.New("no free remote port")
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

func recentLoadError(ctx context.Context, h *hdc.Client, server, sn, pattern string) string {
	out, err := h.Shell(ctx, server, sn, "hilog -x 2>/dev/null | grep -a 'Dlopen /data/local/tmp' | tail -1", 6*time.Second)
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	return ": " + strings.TrimSpace(out)
}

func killServer(ctx context.Context, opts Options) {
	pids := opts.HDC.PIDs(ctx, opts.Server, opts.SN, ExtensionName)
	opts.HDC.KillPIDs(ctx, opts.Server, opts.SN, pids)
}

func waitForServerSocket(ctx context.Context, h *hdc.Client, server, sn, name string, unix bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	cmd := "cat /proc/net/unix 2>/dev/null"
	if !unix {
		cmd = "netstat -ltn 2>/dev/null"
	}
	for time.Now().Before(deadline) {
		out, err := h.Shell(ctx, server, sn, cmd, 5*time.Second)
		if err == nil {
			if unix {
				if strings.Contains(out, "@"+name) || strings.Contains(out, name) {
					return true
				}
			} else if strings.Contains(out, ":"+strings.TrimPrefix(name, "tcp:")) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}
