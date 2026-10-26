package scrcpy

import (
	"strings"
	"testing"
)

func TestVariantsNewSystem(t *testing.T) {
	vs := VariantsFor("6.0.2.3")
	if len(vs) == 0 || vs[0].Asset != "server_static.z.so" || !vs[0].Unix {
		t.Fatalf("unexpected first variant: %+v", vs)
	}
}

func TestVariantsOldSystem(t *testing.T) {
	vs := VariantsFor("5.0.0.1")
	if len(vs) == 0 || vs[0].Asset != "server_5.10.z.so" || vs[0].Unix {
		t.Fatalf("unexpected first variant: %+v", vs)
	}
}

func TestCmpVersion(t *testing.T) {
	if cmpVersion("6.0.2.3", "6.0.2.1") <= 0 {
		t.Fatal("expected newer")
	}
	if cmpVersion("6.0.2.1", "6.0.2.3") >= 0 {
		t.Fatal("expected older")
	}
	if cmpVersion("6.0.2.3", "6.0.2.3") != 0 {
		t.Fatal("expected equal")
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
