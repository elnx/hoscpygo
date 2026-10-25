// Package uitest implements the on-device uitest agent JSON-RPC protocol used
// for input injection, layout dumps and JPEG screen capture.
package uitest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hoscpygo/internal/assets"
	"hoscpygo/internal/config"
	"hoscpygo/internal/hdc"
)

const (
	headMarker = "_uitestkit_rpc_message_head_"
	tailMarker = "_uitestkit_rpc_message_tail_"
	frameMagic = 1145141919
	moduleName = "com.ohos.devicetest.hypiumApiHelper"
)

var versionRe = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){1,3}`)

// Client talks to one device's uitest agent.
type Client struct {
	HDC      *hdc.Client
	Server   string
	SN       string
	Debug    bool
	Agent    string
	Version  string
	Abstract bool

	ctrlPort    int
	layoutPort  int
	capturePort int

	ctrl   net.Conn
	ctrlR  *bufio.Reader
	layout net.Conn

	mu       sync.Mutex // control socket
	lmu      sync.Mutex // layout socket
	ensureMu sync.Mutex
	ready    bool

	imgMu     sync.Mutex
	imgCancel context.CancelFunc
	imgConn   net.Conn

	id atomic.Uint32
}

func New(client *hdc.Client, server, sn string) *Client {
	return &Client{HDC: client, Server: server, SN: sn, Agent: "hoscpy_agent.so"}
}

// Ensure pushes the agent if needed, starts the uitest daemon and opens the
// control/layout sockets. It is safe to call multiple times.
func (c *Client) Ensure(ctx context.Context) error {
	c.ensureMu.Lock()
	defer c.ensureMu.Unlock()
	if c.ready {
		return nil
	}
	if err := c.ensureAgent(ctx); err != nil {
		return err
	}
	sock := "uitest_socket"
	if !c.Abstract {
		sock = "tcp:8012"
	}
	if !c.waitForAgentSocket(ctx, sock, 6*time.Second) {
		return errors.New("uitest agent socket did not appear")
	}
	if c.ctrlPort == 0 || c.layoutPort == 0 || c.capturePort == 0 {
		c.ctrlPort, _ = hdc.FreeLocalPort()
		c.layoutPort, _ = hdc.FreeLocalPort()
		c.capturePort, _ = hdc.FreeLocalPort()
		if c.ctrlPort == 0 || c.layoutPort == 0 || c.capturePort == 0 {
			c.ctrlPort, c.layoutPort, c.capturePort = 0, 0, 0
			return errors.New("no free local port")
		}
		target := "tcp:8012"
		if c.Abstract {
			target = "localabstract:uitest_socket"
		}
		ports := []int{c.ctrlPort, c.layoutPort, c.capturePort}
		for i, p := range ports {
			if err := c.HDC.Fport(ctx, c.Server, c.SN, fmt.Sprintf("tcp:%d", p), target); err != nil {
				for _, q := range ports[:i] {
					_ = c.HDC.RemoveFport(ctx, c.Server, c.SN, fmt.Sprintf("tcp:%d", q), target)
				}
				c.ctrlPort, c.layoutPort, c.capturePort = 0, 0, 0
				return err
			}
		}
	}
	var err error
	c.ctrl, err = dialRetry(ctx, c.ctrlPort, 6*time.Second)
	if err != nil {
		return fmt.Errorf("connect uitest control socket: %w", err)
	}
	c.ctrlR = bufio.NewReaderSize(c.ctrl, 64*1024)
	c.layout, err = dialRetry(ctx, c.layoutPort, 6*time.Second)
	if err != nil {
		_ = c.ctrl.Close()
		return fmt.Errorf("connect uitest layout socket: %w", err)
	}
	c.ready = true
	// Create the Hypium driver if possible; older agents may already expose it.
	if _, err := c.call(ctx, "callHypiumApi", "Driver.create", nil, "hypium", nil); err != nil {
		slog.Debug("Driver.create failed (continuing)", "sn", c.SN, "err", err)
	}
	return nil
}

func (c *Client) ensureAgent(ctx context.Context) error {
	uitestVer, isX86 := c.detectUitest(ctx)
	assetName, agentVer, abstract := chooseAgent(uitestVer, isX86)
	c.Version = agentVer
	c.Abstract = abstract
	slog.Info("uitest agent", "sn", c.SN, "uitest", uitestVer, "agent", assetName, "abstract", abstract)
	asset, err := assets.Device(assetName)
	if err != nil {
		return fmt.Errorf("embedded agent %s: %w", assetName, err)
	}
	local, err := writeCacheAsset(assetName, asset)
	if err != nil {
		return err
	}
	remote := "/data/local/tmp/" + c.Agent
	deviceMD5 := c.HDC.MD5(ctx, c.Server, c.SN, remote)
	localMD5 := md5Hex(asset)
	if deviceMD5 != localMD5 {
		pids := c.HDC.PIDs(ctx, c.Server, c.SN, c.Agent)
		c.HDC.KillPIDs(ctx, c.Server, c.SN, pids)
		_, _ = c.HDC.Shell(ctx, c.Server, c.SN, "rm -f "+remote, 8*time.Second)
		if err := c.HDC.FileSend(ctx, c.Server, c.SN, local, remote); err != nil {
			return fmt.Errorf("push agent: %w", err)
		}
	}
	running := c.HDC.PIDs(ctx, c.Server, c.SN, c.Agent)
	if len(running) == 0 {
		cmd := "/system/bin/uitest start-daemon singleness --extension-name " + c.Agent + " &"
		if err := c.HDC.StartBackground(ctx, c.Server, c.SN, cmd); err != nil {
			return fmt.Errorf("start uitest agent: %w", err)
		}
		if !waitPIDs(ctx, c.HDC, c.Server, c.SN, c.Agent, 6*time.Second) {
			// Older agents may ignore a custom extension name.
			if c.Agent != "agent.so" {
				c.Agent = "agent.so"
				if md5 := c.HDC.MD5(ctx, c.Server, c.SN, remote); md5 != localMD5 {
					if err := c.HDC.FileSend(ctx, c.Server, c.SN, local, "/data/local/tmp/agent.so"); err != nil {
						return fmt.Errorf("push fallback agent: %w", err)
					}
				}
				if err := c.HDC.StartBackground(ctx, c.Server, c.SN, "/system/bin/uitest start-daemon singleness &"); err != nil {
					return fmt.Errorf("start default agent: %w", err)
				}
				if !waitPIDs(ctx, c.HDC, c.Server, c.SN, "singleness", 6*time.Second) {
					return errors.New("uitest agent did not start")
				}
			} else {
				return errors.New("uitest agent did not start")
			}
		}
	}
	return nil
}

func (c *Client) detectUitest(ctx context.Context) (string, bool) {
	ver := ""
	if out, err := c.HDC.Shell(ctx, c.Server, c.SN, "/system/bin/uitest --version", 6*time.Second); err == nil {
		if m := versionRe.FindAllString(out, -1); len(m) > 0 {
			ver = m[len(m)-1]
		}
	}
	x86 := false
	if out, err := c.HDC.Shell(ctx, c.Server, c.SN, "file /system/bin/uitest", 5*time.Second); err == nil {
		x86 = strings.Contains(out, "x86_64")
	}
	return ver, x86
}

func chooseAgent(uitestVer string, x86 bool) (asset, version string, abstract bool) {
	if x86 {
		return "agent_x86_1.1.9.so", "1.1.9", false
	}
	switch {
	case uitestVer == "":
		return "agent_1.2.3.so", "1.2.3", true
	case cmpVersion("5.1.1.2", uitestVer) >= 0:
		return "agent_1.1.3.so", "1.1.3", false
	case cmpVersion("5.1.1.3", uitestVer) == 0:
		return "agent_1.1.5.so", "1.1.5", false
	case cmpVersion("6.0.2.1", uitestVer) >= 0:
		return "agent_1.1.12.so", "1.1.12", false
	default:
		return "agent_1.2.3.so", "1.2.3", true
	}
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

func writeCacheAsset(name string, data []byte) (string, error) {
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

func waitPIDs(ctx context.Context, h *hdc.Client, server, sn, pattern string, timeout time.Duration) bool {
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

func dialRetry(ctx context.Context, port int, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 800 * time.Millisecond}
		conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			setNoDelay(conn)
			return conn, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return nil, last
}

// Call sends a raw JSON request on the control socket and returns its result.
func (c *Client) Call(ctx context.Context, method, api string, args any) (any, error) {
	if err := c.Ensure(ctx); err != nil {
		return nil, err
	}
	messageType := ""
	if method == "callHypiumApi" {
		messageType = "hypium"
	}
	return c.call(ctx, method, api, args, messageType, nil)
}

func (c *Client) call(ctx context.Context, method, api string, args any, messageType string, extra map[string]any) (any, error) {
	params := map[string]any{"api": api}
	if args != nil {
		params["args"] = args
	}
	if messageType != "" {
		params["message_type"] = messageType
		params["this"] = "Driver#0"
	}
	for k, v := range extra {
		params[k] = v
	}
	msg := map[string]any{"module": moduleName, "method": method, "params": params}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctrl == nil {
		return nil, errors.New("uitest control socket is closed")
	}
	_ = c.ctrl.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.ctrl.Write(body); err != nil {
		return nil, err
	}
	_ = c.ctrl.SetReadDeadline(time.Now().Add(15 * time.Second))
	var resp map[string]json.RawMessage
	if err := json.NewDecoder(c.ctrlR).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read uitest response: %w", err)
	}
	_ = c.ctrl.SetReadDeadline(time.Time{})
	if ex, ok := resp["exception"]; ok && string(ex) != "null" && string(ex) != `""` {
		return nil, fmt.Errorf("uitest exception: %s", string(ex))
	}
	res, ok := resp["result"]
	if !ok || string(res) == "null" {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Framed sends a head/id/length/tail framed request and returns the payload.
func (c *Client) Framed(ctx context.Context, method, api string, args any) ([]byte, error) {
	if err := c.Ensure(ctx); err != nil {
		return nil, err
	}
	msg := map[string]any{"module": moduleName, "method": method, "params": map[string]any{"api": api}}
	if args != nil {
		msg["params"].(map[string]any)["args"] = args
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(headMarker)
	// The 4-byte word after the head is a protocol magic, not a session id:
	// the vendor agent echoes 1145141919 and rejects other values.
	_ = binary.Write(&buf, binary.BigEndian, uint32(frameMagic))
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(body)))
	buf.Write(body)
	buf.WriteString(tailMarker)

	c.lmu.Lock()
	defer c.lmu.Unlock()
	if c.layout == nil {
		return nil, errors.New("uitest layout socket is closed")
	}
	_ = c.layout.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.layout.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	return readFramed(c.layout, 20*time.Second)
}

func readFramed(conn net.Conn, timeout time.Duration) ([]byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})
	var acc []byte
	tmp := make([]byte, 256*1024)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			acc = append(acc, tmp[:n]...)
			if payload, ok := parseFrame(acc); ok {
				return payload, nil
			}
			if len(acc) > 32*1024*1024 {
				return nil, errors.New("uitest frame too large")
			}
		}
		if err != nil {
			return nil, fmt.Errorf("read uitest frame: %w", err)
		}
	}
}

func parseFrame(acc []byte) ([]byte, bool) {
	i := bytes.Index(acc, []byte(headMarker))
	if i < 0 {
		return nil, false
	}
	p := i + len(headMarker)
	if len(acc) < p+8 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint32(acc[p+4 : p+8]))
	if n < 0 || n > 32*1024*1024 {
		return nil, false
	}
	start := p + 8
	end := start + n
	if len(acc) < end+len(tailMarker) {
		return nil, false
	}
	if string(acc[end:end+len(tailMarker)]) != tailMarker {
		// Try to recover: find the tail after start.
		j := bytes.Index(acc[start:], []byte(tailMarker))
		if j < 0 {
			return nil, false
		}
		return acc[start : start+j], true
	}
	return acc[start:end], true
}

// Layout returns the current view hierarchy as raw JSON. It first uses the
// agent RPC and falls back to `uitest dumpLayout` + hdc file recv.
func (c *Client) Layout(ctx context.Context) (json.RawMessage, error) {
	payload, err := c.Framed(ctx, "Captures", "captureLayout", nil)
	if err == nil {
		var wrap struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(payload, &wrap) == nil && len(wrap.Result) > 0 {
			return wrap.Result, nil
		}
		return payload, nil
	}
	slog.Warn("agent captureLayout failed, falling back to uitest dumpLayout", "sn", c.SN, "err", err)
	return c.layoutViaShell(ctx)
}

var dumpPathRe = regexp.MustCompile(`DumpLayout saved to:\s*(\S+\.json)`)

func (c *Client) layoutViaShell(ctx context.Context) (json.RawMessage, error) {
	out, err := c.HDC.Shell(ctx, c.Server, c.SN, "uitest dumpLayout", 20*time.Second)
	if err != nil {
		return nil, err
	}
	m := dumpPathRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return nil, fmt.Errorf("dumpLayout did not report a path: %s", strings.TrimSpace(out))
	}
	remote := m[1]
	local := filepath.Join(os.TempDir(), fmt.Sprintf("hoscpygo-layout-%d.json", time.Now().UnixNano()))
	defer os.Remove(local)
	if err := c.HDC.FileRecv(ctx, c.Server, c.SN, remote, local); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(b, &wrap) == nil && len(wrap.Result) > 0 {
		return wrap.Result, nil
	}
	return json.RawMessage(b), nil
}

// TouchDown/Move/Up inject raw touch events.
func (c *Client) TouchDown(ctx context.Context, x, y int) error {
	return c.touch(ctx, "touchDown", x, y)
}
func (c *Client) TouchMove(ctx context.Context, x, y int) error {
	return c.touch(ctx, "touchMove", x, y)
}
func (c *Client) TouchUp(ctx context.Context, x, y int) error {
	return c.touch(ctx, "touchUp", x, y)
}

func (c *Client) touch(ctx context.Context, api string, x, y int) error {
	_, err := c.Call(ctx, "Gestures", api, map[string]int{"x": x, "y": y})
	return err
}

// MouseDown/Up/Move inject mouse events. button is left, middle or right.
func (c *Client) MouseDown(ctx context.Context, button string, x, y int) error {
	_, err := c.Call(ctx, "Gestures", mouseAPI("Button", button, "Down"), map[string]int{"x": x, "y": y})
	return err
}
func (c *Client) MouseUp(ctx context.Context, button string, x, y int) error {
	_, err := c.Call(ctx, "Gestures", mouseAPI("Button", button, "Up"), map[string]int{"x": x, "y": y})
	return err
}
func (c *Client) MouseMove(ctx context.Context, button string, x, y int) error {
	api := "MouseMove"
	if button != "" {
		api = mouseAPI("Button", button, "Move")
	}
	_, err := c.Call(ctx, "Gestures", api, map[string]int{"x": x, "y": y})
	return err
}
func (c *Client) WheelUp(ctx context.Context, x, y int) error {
	_, err := c.Call(ctx, "Gestures", "AxisUp", map[string]int{"x": x, "y": y})
	return err
}
func (c *Client) WheelDown(ctx context.Context, x, y int) error {
	_, err := c.Call(ctx, "Gestures", "AxisDown", map[string]int{"x": x, "y": y})
	return err
}
func (c *Client) WheelStop(ctx context.Context, x, y int) error {
	_, err := c.Call(ctx, "Gestures", "AxisStop", map[string]int{"x": x, "y": y})
	return err
}

func mouseAPI(prefix, button, suffix string) string {
	switch button {
	case "middle":
		return prefix + "Middle" + suffix
	case "right":
		return prefix + "Right" + suffix
	default:
		return prefix + "Left" + suffix
	}
}

// Rotate sets display rotation: horizontal=true rotates to 1, else 0.
func (c *Client) Rotate(ctx context.Context, horizontal bool) error {
	v := 0
	if horizontal {
		v = 1
	}
	_, err := c.Call(ctx, "callHypiumApi", "Driver.setDisplayRotation", []any{v})
	return err
}

// DisplaySize returns the current display resolution via the Hypium driver.
func (c *Client) DisplaySize(ctx context.Context) (int, int, error) {
	res, err := c.Call(ctx, "callHypiumApi", "Driver.getDisplaySize", []any{})
	if err != nil {
		return 0, 0, err
	}
	m, ok := res.(map[string]any)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected display size: %v", res)
	}
	w, _ := m["x"].(float64)
	h, _ := m["y"].(float64)
	return int(w), int(h), nil
}

// StartImage starts the JPEG capture stream and calls onFrame for every JPEG.
// The returned channel is closed after the first frame or the first error.
func (c *Client) StartImage(ctx context.Context, scale float64, onFrame func([]byte), onError func(error)) (<-chan struct{}, error) {
	if err := c.Ensure(ctx); err != nil {
		return nil, err
	}
	c.imgMu.Lock()
	if c.imgCancel != nil {
		c.imgCancel()
		c.imgCancel = nil
	}
	if c.imgConn != nil {
		_ = c.imgConn.Close()
		c.imgConn = nil
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.capturePort), 5*time.Second)
	if err != nil {
		c.imgMu.Unlock()
		return nil, err
	}
	setNoDelay(conn)
	started := make(chan struct{})
	var once sync.Once
	markStarted := func() { once.Do(func() { close(started) }) }
	imgCtx, cancel := context.WithCancel(ctx)
	c.imgCancel = cancel
	c.imgConn = conn
	c.imgMu.Unlock()

	msg := map[string]any{
		"module": moduleName,
		"method": "Captures",
		"params": map[string]any{
			"api":  "startCaptureScreen",
			"args": map[string]any{"options": map[string]any{"displayId": 0, "scale": scale}},
		},
	}
	body, _ := json.Marshal(msg)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(body); err != nil {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	go func() {
		defer conn.Close()
		err := readJPEGStream(imgCtx, conn, func(jpg []byte) {
			markStarted()
			onFrame(jpg)
		})
		cancel()
		if err != nil && imgCtx.Err() == nil {
			onError(err)
		}
	}()
	// Wait for the first image or an error from the reader. The session also
	// has its own timeout; this just avoids returning before the ack is read.
	go func() {
		select {
		case <-time.After(8 * time.Second):
			markStarted()
		case <-started:
		case <-imgCtx.Done():
		}
	}()
	return started, nil
}

func readJPEGStream(ctx context.Context, conn net.Conn, onFrame func([]byte)) error {
	buf := make([]byte, 0, 512*1024)
	tmp := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				i := bytes.Index(buf, []byte{0xff, 0xd8})
				if i < 0 {
					if len(buf) > 4096 {
						// Keep a small tail in case the SOI is split.
						buf = buf[len(buf)-2:]
					}
					break
				}
				if i > 0 {
					buf = buf[i:]
				}
				j := bytes.Index(buf[2:], []byte{0xff, 0xd9})
				if j < 0 {
					if len(buf) > 16*1024*1024 {
						return errors.New("jpeg frame too large")
					}
					break
				}
				end := 2 + j + 2
				frame := append([]byte(nil), buf[:end]...)
				buf = buf[end:]
				onFrame(frame)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// StopImage stops the JPEG stream. It sends the stop command on the control
// socket and closes the capture connection.
func (c *Client) StopImage(ctx context.Context) {
	c.imgMu.Lock()
	cancel := c.imgCancel
	conn := c.imgConn
	c.imgCancel = nil
	c.imgConn = nil
	c.imgMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	c.mu.Lock()
	ready := c.ready && c.ctrl != nil
	c.mu.Unlock()
	if ready {
		_, _ = c.call(ctx, "Captures", "stopCaptureScreen", nil, "", nil)
	}
}

// Close releases sockets and fports. The agent process is left running unless
// StopAgent is called.
func (c *Client) Close(ctx context.Context) {
	c.StopImage(ctx)
	c.ensureMu.Lock()
	defer c.ensureMu.Unlock()
	c.mu.Lock()
	if c.ctrl != nil {
		_ = c.ctrl.Close()
		c.ctrl = nil
	}
	c.mu.Unlock()
	c.lmu.Lock()
	if c.layout != nil {
		_ = c.layout.Close()
		c.layout = nil
	}
	c.lmu.Unlock()
	target := "tcp:8012"
	if c.Abstract {
		target = "localabstract:uitest_socket"
	}
	for _, p := range []int{c.ctrlPort, c.layoutPort, c.capturePort} {
		if p != 0 {
			_ = c.HDC.RemoveFport(ctx, c.Server, c.SN, fmt.Sprintf("tcp:%d", p), target)
		}
	}
	c.ctrlPort, c.layoutPort, c.capturePort = 0, 0, 0
	c.ready = false
}

// StopAgent kills the agent process we started.
func (c *Client) StopAgent(ctx context.Context) {
	pids := c.HDC.PIDs(ctx, c.Server, c.SN, c.Agent)
	c.HDC.KillPIDs(ctx, c.Server, c.SN, pids)
}

func setNoDelay(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.SetNoDelay(true)
	}
}

// Tap injects a touch-style click through the Hypium Driver API. The raw
// Gestures touchDown/touchUp API is a no-op on some HarmonyOS 6/7 builds.
func (c *Client) Tap(ctx context.Context, x, y int) error {
	_, err := c.Call(ctx, "callHypiumApi", "Driver.click", []any{x, y})
	return err
}

// Swipe injects a touch-style swipe. velocity is in the range 200..40000.
func (c *Client) Swipe(ctx context.Context, x1, y1, x2, y2, velocity int) error {
	if velocity < 200 {
		velocity = 200
	}
	if velocity > 40000 {
		velocity = 40000
	}
	_, err := c.Call(ctx, "callHypiumApi", "Driver.swipe", []any{x1, y1, x2, y2, velocity})
	return err
}

// LongClick injects a long press.
func (c *Client) LongClick(ctx context.Context, x, y int) error {
	_, err := c.Call(ctx, "callHypiumApi", "Driver.longClick", []any{x, y})
	return err
}

func (c *Client) waitForAgentSocket(ctx context.Context, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	unix := name == "uitest_socket"
	cmd := "cat /proc/net/unix 2>/dev/null"
	if !unix {
		cmd = "netstat -ltn 2>/dev/null"
	}
	for time.Now().Before(deadline) {
		out, err := c.HDC.Shell(ctx, c.Server, c.SN, cmd, 5*time.Second)
		if err == nil {
			if unix {
				if strings.Contains(out, "@"+name) || strings.Contains(out, name) {
					return true
				}
			} else if strings.Contains(out, ":8012") {
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
