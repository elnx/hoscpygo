package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"127.0.0.1":         "127.0.0.1:8710",
		"192.168.1.10:9000": "192.168.1.10:9000",
		"[::1]":             "[::1]:8710",
	}
	for in, want := range cases {
		if got := NormalizeServer(in); got != want {
			t.Fatalf("NormalizeServer(%q)=%q want %q", in, got, want)
		}
	}
}

func TestServersDedup(t *testing.T) {
	c := Default()
	c.RemoteServers = []string{"192.168.1.10", "192.168.1.10:8710", "  "}
	got := c.Servers()
	if len(got) != 2 {
		t.Fatalf("servers=%v", got)
	}
	if got[0] != "127.0.0.1:8710" || got[1] != "192.168.1.10:8710" {
		t.Fatalf("servers=%v", got)
	}
}

func TestServersOverride(t *testing.T) {
	c := Default()
	c.RemoteServers = []string{"192.168.1.10"}
	c.Server = " 192.0.2.10 "
	c.Normalize()
	got := c.Servers()
	if len(got) != 1 || got[0] != "192.0.2.10:8710" {
		t.Fatalf("servers=%v", got)
	}
}

func TestNormalizeDefaults(t *testing.T) {
	c := &Config{Scale: 99, FrameRate: 999, BitRateMbps: 999, ImageScale: 2}
	c.Normalize()
	if c.Scale != 2 || c.FrameRate != 60 || c.BitRateMbps != 8 || c.ImageScale != 0.5 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadLegacyListen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":"0.0.0.0:9000","scale":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPListen != "0.0.0.0:9000" || c.HTTPSListen != "" || c.Scale != 3 {
		t.Fatalf("legacy migration: %+v", c)
	}
}

func TestLoadListenerKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"http_listen":"127.0.0.1:9001","https_listen":"0.0.0.0:9443"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPListen != "127.0.0.1:9001" || c.HTTPSListen != "0.0.0.0:9443" {
		t.Fatalf("listener keys: %+v", c)
	}
}

func TestLoadExplicitEmptyHTTPStaysDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"http_listen":"","https_listen":"0.0.0.0:9443"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPListen != "" || c.HTTPSListen != "0.0.0.0:9443" {
		t.Fatalf("explicit empty http: %+v", c)
	}
}

func TestValidateListen(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default: %v", err)
	}
	c.HTTPListen, c.HTTPSListen = "", ""
	if err := c.Validate(); err == nil {
		t.Fatal("both empty must fail")
	}
	c.HTTPSListen = "not-an-address"
	if err := c.Validate(); err == nil {
		t.Fatal("bad address must fail")
	}
	c.HTTPSListen = "0.0.0.0:9443"
	if err := c.Validate(); err != nil {
		t.Fatalf("https only: %v", err)
	}
}
