// Package hdc wraps the HarmonyOS Device Connector command line tool.
package hdc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client runs hdc commands for one or more devices.
type Client struct {
	Bin            string
	Env            []string
	DefaultTimeout time.Duration
	Debug          bool
	mu             sync.Mutex
}

func New(bin string, env []string) *Client {
	return &Client{Bin: bin, Env: env, DefaultTimeout: 12 * time.Second}
}

// Resolve returns the system hdc executable. Precedence:
// explicit -hdc/config path, $HDC, then PATH.
//
// hdc is intentionally not embedded: the binaries are large, platform
// specific, and easier to update from the HarmonyOS command-line tools.
func Resolve(configured string) (bin string, env []string, err error) {
	if configured != "" {
		if st, statErr := os.Stat(configured); statErr == nil && !st.IsDir() {
			return configured, os.Environ(), nil
		}
		return "", nil, fmt.Errorf("hdc path %q is not a file", configured)
	}
	if p := os.Getenv("HDC"); p != "" {
		if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() {
			return p, os.Environ(), nil
		}
		return "", nil, fmt.Errorf("HDC=%q is not a file", p)
	}
	if p, lookErr := exec.LookPath("hdc"); lookErr == nil {
		return p, os.Environ(), nil
	}
	return "", nil, fmt.Errorf("hdc not found. Install the HarmonyOS/OpenHarmony command-line tools and add hdc to PATH, or pass -hdc /path/to/hdc (or set HDC=/path/to/hdc). Typical DevEco location: <command-line-tools>/sdk/default/openharmony/toolchains/hdc")
}

func (c *Client) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = c.DefaultTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	out, err := cmd.CombinedOutput()
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	text = strings.TrimSpace(text)
	if c.Debug {
		slog.Debug("hdc", "args", args, "out", truncate(text, 800), "err", err)
	}
	if err != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("hdc timed out after %s: %w", timeout, ctx.Err())
		}
		return text, fmt.Errorf("hdc %s: %w: %s", strings.Join(args, " "), err, truncate(text, 300))
	}
	return text, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (c *Client) withServer(server string, args ...string) []string {
	if server == "" {
		return args
	}
	return append([]string{"-s", server}, args...)
}

// ListTargets returns device serials visible to the given hdc server.
func (c *Client) ListTargets(ctx context.Context, server string) ([]string, error) {
	out, err := c.run(ctx, 8*time.Second, c.withServer(server, "list", "targets")...)
	if err != nil {
		return nil, err
	}
	if strings.Contains(strings.ToLower(out), "failed") || strings.Contains(out, "[Fail]") {
		return nil, errors.New(out)
	}
	var outSns []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "empty") || strings.HasPrefix(line, "[") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		sn := fields[0]
		if sn == "" || strings.EqualFold(sn, "empty") {
			continue
		}
		outSns = append(outSns, sn)
	}
	return outSns, nil
}

func (c *Client) Shell(ctx context.Context, server, sn, command string, timeout time.Duration) (string, error) {
	args := c.withServer(server, "-t", sn, "shell", command)
	return c.run(ctx, timeout, args...)
}

// ShellNoTarget runs a server-level command with a target selector.
func (c *Client) ShellRaw(ctx context.Context, args []string, timeout time.Duration) (string, error) {
	return c.run(ctx, timeout, args...)
}

func (c *Client) FileSend(ctx context.Context, server, sn, local, remote string) error {
	args := c.withServer(server, "-t", sn, "file", "send", local, remote)
	out, err := c.run(ctx, 60*time.Second, args...)
	if err != nil {
		return err
	}
	low := strings.ToLower(out)
	if strings.Contains(out, "[Fail]") || strings.Contains(low, "error") || strings.Contains(low, "fail") {
		return fmt.Errorf("hdc file send failed: %s", truncate(out, 300))
	}
	return nil
}

func (c *Client) FileRecv(ctx context.Context, server, sn, remote, local string) error {
	args := c.withServer(server, "-t", sn, "file", "recv", remote, local)
	out, err := c.run(ctx, 60*time.Second, args...)
	if err != nil {
		return err
	}
	low := strings.ToLower(out)
	if strings.Contains(out, "[Fail]") || strings.Contains(low, "error") || strings.Contains(low, "fail") {
		return fmt.Errorf("hdc file recv failed: %s", truncate(out, 300))
	}
	return nil
}

func (c *Client) Fport(ctx context.Context, server, sn, local, remote string) error {
	args := c.withServer(server, "-t", sn, "fport", local, remote)
	out, err := c.run(ctx, 10*time.Second, args...)
	if err != nil {
		return err
	}
	if strings.Contains(out, "[Fail]") || strings.Contains(strings.ToLower(out), "failed") {
		return fmt.Errorf("hdc fport failed: %s", out)
	}
	return nil
}

func (c *Client) RemoveFport(ctx context.Context, server, sn, local, remote string) error {
	args := c.withServer(server, "-t", sn, "fport", "rm", local, remote)
	out, err := c.run(ctx, 8*time.Second, args...)
	if err != nil {
		return err
	}
	if strings.Contains(out, "[Fail]") {
		return fmt.Errorf("hdc fport rm failed: %s", out)
	}
	return nil
}

// MD5 returns the device-side md5 of path, or "" when it does not exist.
func (c *Client) MD5(ctx context.Context, server, sn, path string) string {
	out, err := c.Shell(ctx, server, sn, "md5sum "+Quote(path), 8*time.Second)
	if err != nil || strings.Contains(out, "No such file") || strings.Contains(out, "not found") {
		return ""
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSpace(fields[0])
}

// PIDs returns process ids whose full command line contains pattern.
func (c *Client) PIDs(ctx context.Context, server, sn, pattern string) []int {
	out, err := c.Shell(ctx, server, sn, "ps -ef", 8*time.Second)
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, pattern) {
			continue
		}
		if strings.Contains(line, "grep") || strings.Contains(line, "ps -ef") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 1 {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

func (c *Client) KillPIDs(ctx context.Context, server, sn string, pids []int) {
	if len(pids) == 0 {
		return
	}
	var parts []string
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}
	_, _ = c.Shell(ctx, server, sn, "kill -9 "+strings.Join(parts, " "), 8*time.Second)
}

// StartBackground starts a device command in the background and returns after
// the shell has accepted it.
func (c *Client) StartBackground(ctx context.Context, server, sn, command string) error {
	command = strings.TrimSpace(command)
	command = strings.TrimSpace(strings.TrimSuffix(command, "&"))
	// Redirect stdout/stderr: hdc keeps the shell channel open while a child
	// inherits the pipes, which would make this call block until the daemon
	// exits. The daemon itself must outlive the shell.
	command += " >/dev/null 2>&1 &"
	_, err := c.Shell(ctx, server, sn, command, 10*time.Second)
	return err
}

func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.run(ctx, 8*time.Second, "-v")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// FreeLocalPort asks the kernel for a free loopback port.
func FreeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// RemotePortFree reports whether a TCP connect to host:port fails (the same
// heuristic used by the Java SDK for remote hdc servers).
func RemotePortFree(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

// Quote single-quotes a shell argument for the device shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TargetHost returns the host part of a hdc server address.
func TargetHost(server string) string {
	if h, _, err := net.SplitHostPort(server); err == nil {
		return h
	}
	return server
}
