// Package config holds user settings and cache locations.
package config

import (
	"encoding/json"
	"errors"
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
	Listen          string   `json:"listen"`
	HdcPath         string   `json:"hdc_path"`
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
		Listen:          "127.0.0.1:8899",
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
	c.Normalize()
	return c, nil
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
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8899"
	}
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
	for i, s := range c.RemoteServers {
		c.RemoteServers[i] = NormalizeServer(s)
	}
}

// Servers returns the local hdc server plus configured remote servers, deduped.
func (c *Config) Servers() []string {
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
