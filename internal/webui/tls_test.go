package webui

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"testing"
)

func TestEnsureSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := ensureSelfSignedCert(dir, "0.0.0.0:9443")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	hasIP := func(want net.IP) bool {
		for _, ip := range leaf.IPAddresses {
			if ip.Equal(want) {
				return true
			}
		}
		return false
	}
	if !hasIP(net.IPv4(127, 0, 0, 1)) || !hasIP(net.IPv6loopback) {
		t.Fatalf("missing loopback SANs: %v", leaf.IPAddresses)
	}
	var hasLocalhost bool
	for _, name := range leaf.DNSNames {
		if name == "localhost" {
			hasLocalhost = true
		}
	}
	if !hasLocalhost {
		t.Fatalf("missing localhost SAN: %v", leaf.DNSNames)
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key mode = %o, want 600", perm)
	}

	before, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureSelfSignedCert(dir, "0.0.0.0:9443"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("certificate was regenerated while still valid")
	}
}

func TestCertHostsIncludesListenIP(t *testing.T) {
	dns, ips := certHosts("192.0.2.10:9443")
	if len(dns) == 0 || len(ips) == 0 {
		t.Fatalf("empty SANs: dns=%v ips=%v", dns, ips)
	}
	var found bool
	for _, ip := range ips {
		if ip.String() == "192.0.2.10" {
			found = true
		}
	}
	if !found {
		t.Fatalf("listen IP missing from %v", ips)
	}
}
