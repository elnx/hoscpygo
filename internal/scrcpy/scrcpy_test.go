package scrcpy

import "testing"

func TestVariantsNewSystem(t *testing.T) {
	vs := VariantsFor("6.0.2.3")
	if len(vs) == 0 || vs[0].Asset != "server_unix_6.5.z.so" || !vs[0].Unix {
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
