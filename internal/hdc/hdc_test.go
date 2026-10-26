package hdc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"abc":     "'abc'",
		"a'b":     `'a'\''b'`,
		"中文 test": "'中文 test'",
		"":        "''",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Fatalf("Quote(%q)=%q want %q", in, got, want)
		}
	}
}

func TestTargetHost(t *testing.T) {
	if got := TargetHost("192.168.1.2:8710"); got != "192.168.1.2" {
		t.Fatalf("host=%q", got)
	}
	if got := TargetHost("127.0.0.1"); got != "127.0.0.1" {
		t.Fatalf("host=%q", got)
	}
}

func TestResolvePrefersPath(t *testing.T) {
	dir := t.TempDir()
	name := "hdc"
	body := []byte("#!/bin/sh\nexit 0\n")
	if runtime.GOOS == "windows" {
		name = "hdc.exe"
		body = []byte("MZ")
	}
	fake := filepath.Join(dir, name)
	if err := os.WriteFile(fake, body, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HDC", "")
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".EXE;.CMD;.BAT")
	}
	got, _, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != filepath.Clean(fake) {
		t.Fatalf("Resolve()=%q want PATH candidate %q", got, fake)
	}
}
