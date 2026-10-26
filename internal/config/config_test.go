package config

import "testing"

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
