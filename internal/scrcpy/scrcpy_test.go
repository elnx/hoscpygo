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
