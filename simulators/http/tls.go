package simhttp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const CertLifetime = 30 * 24 * time.Hour

func SplitNames(list string) ([]string, []net.IP, error) {
	var dns []string
	var ips []net.IP
	for _, n := range strings.Split(list, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if ip := net.ParseIP(n); ip != nil {
			ips = append(ips, ip)
			continue
		}
		for _, label := range strings.Split(strings.TrimPrefix(n, "*."), ".") {
			if label == "" || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
				return nil, nil, fmt.Errorf("--tls-name: %q is neither a host name nor an IP address", n)
			}
		}
		dns = append(dns, n)
	}
	if len(dns)+len(ips) == 0 {
		return nil, nil, errors.New("--tls-name: name at least one host name or IP address")
	}
	return dns, ips, nil
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
}

func NewCA(dns []string, ips []net.IP) (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tag := make([]byte, 6)
	if _, err := rand.Read(tag); err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	sn, err := serial()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: SimName + " CA " + hex.EncodeToString(tag)},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(CertLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	sn, err = serial()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	cn := ""
	if len(dns) > 0 {
		cn = dns[0]
	} else {
		cn = ips[0].String()
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(CertLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	cert := tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

func WriteCA(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("--ca-out %s is a symbolic link; name a plain file", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("--ca-out %s is not a regular file", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func ServerTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}
}
