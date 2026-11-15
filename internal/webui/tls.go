package webui

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	certValidity   = 825 * 24 * time.Hour // about 27 months
	certRenewAhead = 30 * 24 * time.Hour
)

// ensureSelfSignedCert returns a persistent self-signed cert/key pair for
// addr. It generates a new pair on first use, when the files are unreadable or
// mismatched, or when the certificate is close to expiring. Keeping the pair
// stable matters: browsers remember an accepted certificate exception per
// profile, and regenerating invalidates it.
func ensureSelfSignedCert(dir, addr string) (certFile, keyFile string, err error) {
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if tlsPairUsable(certFile, keyFile) {
		return certFile, keyFile, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	dns, ips := certHosts(addr)
	certPEM, keyPEM, notAfter, err := selfSignedCert(dns, ips)
	if err != nil {
		return "", "", err
	}
	// Write the key first: if only one file is replaced before a crash, the
	// pair is still mismatched and the next start regenerates both.
	if err := writePrivate(keyFile, keyPEM); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return "", "", err
	}
	if err := os.Chmod(certFile, 0o644); err != nil {
		return "", "", err
	}
	slog.Info("generated self-signed TLS certificate", "cert", certFile,
		"dns", dns, "ips", ipStrings(ips), "not_after", notAfter.Format(time.RFC3339))
	return certFile, keyFile, nil
}

func tlsPairUsable(certFile, keyFile string) bool {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil || len(pair.Certificate) == 0 {
		return false
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Add(certRenewAhead).Before(cert.NotAfter) {
		slog.Info("TLS certificate needs renewal", "cert", certFile, "not_after", cert.NotAfter)
		return false
	}
	return true
}

func selfSignedCert(dns []string, ips []net.IP) (certPEM, keyPEM []byte, notAfter time.Time, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"hoscpygo"}, CommonName: "hoscpygo"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, tmpl.NotAfter, nil
}

func writePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// certHosts builds SANs that cover loopback, the machine hostname and every
// interface address, so a LAN browser sees a hostname-only warning that can be
// accepted instead of an outright address mismatch.
func certHosts(addr string) ([]string, []net.IP) {
	dnsSet := map[string]struct{}{"localhost": {}}
	ipSet := map[string]net.IP{
		"127.0.0.1": net.IPv4(127, 0, 0, 1).To4(),
		"::1":       net.IPv6loopback,
	}
	addIP := func(ip net.IP) {
		if ip == nil || ip.IsUnspecified() {
			return
		}
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		ipSet[ip.String()] = ip
	}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); ip != nil {
			addIP(ip)
		} else {
			dnsSet[host] = struct{}{}
		}
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		dnsSet[hostname] = struct{}{}
		dnsSet[hostname+".local"] = struct{}{}
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				switch v := a.(type) {
				case *net.IPNet:
					addIP(v.IP)
				case *net.IPAddr:
					addIP(v.IP)
				}
			}
		}
	}
	dns := make([]string, 0, len(dnsSet))
	for host := range dnsSet {
		dns = append(dns, host)
	}
	sort.Strings(dns)
	ips := make([]net.IP, 0, len(ipSet))
	for _, ip := range ipSet {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool { return bytes.Compare(ips[i], ips[j]) < 0 })
	return dns, ips
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out
}
