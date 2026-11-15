package main

import (
	"testing"

	"hoscpygo/internal/config"
)

func TestApplyListenerFlags(t *testing.T) {
	cases := []struct {
		name      string
		httpAddr  string
		httpsAddr string
		httpSet   bool
		httpsSet  bool
		wantHTTP  string
		wantHTTPS string
	}{
		{"no flags keep config", "", "", false, false, "127.0.0.1:8899", "0.0.0.0:9443"},
		{"https only disables http", "127.0.0.1:8899", "0.0.0.0:8899", false, true, "", "0.0.0.0:8899"},
		{"http only disables https", "0.0.0.0:8899", "0.0.0.0:9443", true, false, "0.0.0.0:8899", ""},
		{"both flags", "127.0.0.1:8899", "0.0.0.0:9443", true, true, "127.0.0.1:8899", "0.0.0.0:9443"},
		{"explicit empty disables", "", "", true, true, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.HTTPSListen = "0.0.0.0:9443"
			applyListenerFlags(cfg, tc.httpAddr, tc.httpsAddr, tc.httpSet, tc.httpsSet)
			if cfg.HTTPListen != tc.wantHTTP || cfg.HTTPSListen != tc.wantHTTPS {
				t.Fatalf("got http=%q https=%q, want http=%q https=%q",
					cfg.HTTPListen, cfg.HTTPSListen, tc.wantHTTP, tc.wantHTTPS)
			}
		})
	}
}
