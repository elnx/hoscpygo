// Package config holds user settings and cache locations.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	ModeAuto  = "auto"
	ModeVideo = "video"
	ModeImage = "image"
)

// Config is persisted as JSON in the user's config directory.
type Config struct {
	HTTPListen  string `json:"http_listen"`
	HTTPSListen string `json:"https_listen"`
	HdcPath     string `json:"hdc_path"`
	// Server, when set, makes this process use only that hdc server for
	// discovery and device commands. It is a runtime -s override, not persisted.
	Server          string   `json:"-"`
	RemoteServers   []string `json:"remote_servers"`
	Mode            string   `json:"mode"`
	Scale           int      `json:"scale"`
	FrameRate       int      `json:"frame_rate"`
	BitRateMbps     int      `json:"bit_rate_mbps"`
	IFrameInterval  int      `json:"i_frame_interval"`
	ImageScale      float64  `json:"image_scale"`
	AutoOpenBrowser bool     `json:"auto_open_browser"`
	KeepAgent       bool     `json:"keep_agent"`
	Debug           bool     `json:"debug"`
}

func Default() *Config {
	return &Config{
		HTTPListen:      "127.0.0.1:8899",
		Mode:            ModeAuto,
		Scale:           2,
		FrameRate:       60,
		BitRateMbps:     8,
		IFrameInterval:  2000,
		ImageScale:      0.5,
		AutoOpenBrowser: true,
		KeepAgent:       true,
	}
}

// Path returns the config file path. HOSCPYGO_CONFIG overrides it.
func Path() string {
	if p := os.Getenv("HOSCPYGO_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "hoscpygo", "config.json")
}

// CacheDir returns a writable cache directory for extracted binaries/assets.
func CacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "hoscpygo")
}

func Load(path string) (*Config, error) {
	c := Default()
	if path == "" {
		path = Path()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	// Migrate the pre-split config schema: {"listen": "..."} was the single
	// HTTP address. A newer file always wins over the legacy key.
	if !hasJSONKey(b, "http_listen") {
		var legacy struct {
			Listen string `json:"listen"`
		}
		if err := json.Unmarshal(b, &legacy); err != nil {
			return nil, err
		}
		if legacy.Listen != "" {
			c.HTTPListen = legacy.Listen
		}
	}
	c.Normalize()
	return c, nil
}

func hasJSONKey(b []byte, key string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

func (c *Config) Save(path string) error {
	c.Normalize()
	if path == "" {
		path = Path()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func (c *Config) Normalize() {
	c.HTTPListen = strings.TrimSpace(c.HTTPListen)
	c.HTTPSListen = strings.TrimSpace(c.HTTPSListen)
	switch c.Mode {
	case ModeVideo, ModeImage, ModeAuto:
	default:
		c.Mode = ModeAuto
	}
	if c.Scale < 1 || c.Scale > 5 {
		c.Scale = 2
	}
	if c.FrameRate < 1 || c.FrameRate > 120 {
		c.FrameRate = 60
	}
	if c.BitRateMbps < 1 || c.BitRateMbps > 100 {
		c.BitRateMbps = 8
	}
	if c.IFrameInterval < 100 {
		c.IFrameInterval = 2000
	}
	if c.ImageScale <= 0 || c.ImageScale > 1 {
		c.ImageScale = 0.5
	}
	c.Server = NormalizeServer(c.Server)
	for i, s := range c.RemoteServers {
		c.RemoteServers[i] = NormalizeServer(s)
	}
}

// Validate checks the browser listener addresses. At least one must be set;
// an empty address disables that listener.
func (c *Config) Validate() error {
	if c.HTTPListen == "" && c.HTTPSListen == "" {
		return errors.New("http_listen and https_listen are both empty")
	}
	for name, addr := range map[string]string{"http_listen": c.HTTPListen, "https_listen": c.HTTPSListen} {
		if addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("%s %q: %w", name, addr, err)
		}
	}
	return nil
}

// Servers returns the effective hdc servers. A -s override wins and suppresses
// the local/configured list; otherwise it is the local hdc server plus
// configured remote servers, deduped.
func (c *Config) Servers() []string {
	if c.Server != "" {
		return []string{c.Server}
	}
	out := []string{"127.0.0.1:8710"}
	seen := map[string]bool{out[0]: true}
	for _, s := range c.RemoteServers {
		s = NormalizeServer(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// NormalizeServer ensures a server address has a port, defaulting to 8710.
func NormalizeServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s + ":8710"
	}
	return net.JoinHostPort(s, "8710")
}

// Host returns the host part of a hdc server address.
func Host(server string) string {
	h, _, err := net.SplitHostPort(server)
	if err != nil {
		return server
	}
	return h
}
