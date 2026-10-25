package hdc

import "testing"

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
