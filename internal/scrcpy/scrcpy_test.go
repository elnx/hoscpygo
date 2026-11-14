package scrcpy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"hoscpygo/internal/hdc"
)

const fakeHDCEnv = "HOSCPY_FAKE_HDC"

// TestMain doubles as a fake hdc binary: the readiness wait tests run the test
// executable instead of hdc, so the loader-race logic is exercised without a
// device.
func TestMain(m *testing.M) {
	if os.Getenv(fakeHDCEnv) == "1" {
		args := strings.Join(os.Args[1:], " ")
		switch {
		case strings.Contains(args, "ps -ef"):
			// The daemon died before the first poll; only hilog saw it.
		case strings.Contains(args, "hilog"):
			fmt.Print("11-14 11:29:56.832 25948 25948 E C03100/uitest/UiTestKit_Addon: [extension_executor.cpp:(ExecuteExtension)] Dlopen /data/local/tmp/hoscpy_server.z.so failed: Error relocating /data/local/tmp/hoscpy_server.z.so: symbol not found\n")
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeHDC(t *testing.T) *hdc.Client {
	t.Helper()
	return hdc.New(os.Args[0], append(os.Environ(), fakeHDCEnv+"=1"))
}

func TestVariantsNewSystem(t *testing.T) {
	vs := VariantsFor("6.0.2.3")
	if len(vs) == 0 || vs[0].Asset != "server_static.z.so" || !vs[0].Unix {
		t.Fatalf("unexpected first variant: %+v", vs)
	}
}

func TestVariantsFallback(t *testing.T) {
	vs := VariantsFor("5.0.0.1")
	if len(vs) != 4 || vs[0].Asset != "server_static.z.so" || vs[1].Asset != "server_unix_6.5.z.so" ||
		vs[2].Asset != "server_unix_6.4.z.so" || vs[3].Asset != "server_unix_6.3.1.z.so" {
		t.Fatalf("unexpected variants: %+v", vs)
	}
}

func TestVideoFailureHintXPM(t *testing.T) {
	hint := videoFailureHint([]string{"Dlopen /data/local/tmp/x.z.so failed: Permission denied; Xpm check failed"})
	if !strings.Contains(hint, "XPM") || !strings.Contains(hint, "signed") || !strings.Contains(hint, "image mode") {
		t.Fatalf("unexpected XPM hint: %q", hint)
	}
}

func TestVideoFailureHint(t *testing.T) {
	hint := videoFailureHint([]string{"Dlopen /data/local/tmp/x.z.so failed: Error relocating x: _ZN...: symbol not found"})
	if !strings.Contains(hint, "image mode") || !strings.Contains(hint, "incompatible") {
		t.Fatalf("unexpected hint: %q", hint)
	}
	if got := videoFailureHint([]string{"some other error"}); got != "" {
		t.Fatalf("unexpected non-empty hint: %q", got)
	}
}

// A loader rejection can outlive the ps poll window. The hilog line must end
// the wait well before processStartTimeout, and must be reported as this
// attempt's failure rather than left as a bare timeout.
func TestWaitForServerReadyDetectsMissedDaemon(t *testing.T) {
	start := time.Now()
	load, err := waitForServerReady(context.Background(), fakeHDC(t), "", "SN",
		ExtensionName, "scrcpy_grpc_socket", true, "older loader line", true)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(err.Error(), "extension failed to load") || !strings.Contains(err.Error(), "symbol not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(load, "symbol not found") {
		t.Fatalf("unexpected load line: %q", load)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("loader failure took %s, want < 3s", d)
	}
}

func TestLoaderErrorIgnoresBaseline(t *testing.T) {
	if err := loaderError("extension did not start", "same line", "same line"); err.Error() != "extension did not start" {
		t.Fatalf("baseline line leaked into error: %v", err)
	}
	err := loaderError("extension did not start", "new line", "old line")
	if !strings.Contains(err.Error(), "new line") || strings.Contains(err.Error(), "old line") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStandaloneTarget(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"0\naarch64\n", true},
		{"0\narm64\n", true},
		{"0\nAArch64\n", true},
		{"2000\naarch64\n", false},
		{"0\nx86_64\n", false},
		{"2000\nx86_64\n", false},
		{"", false},
		{"0\n", false},
	}
	for _, c := range cases {
		if got := standaloneTarget(c.out); got != c.want {
			t.Fatalf("standaloneTarget(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}
