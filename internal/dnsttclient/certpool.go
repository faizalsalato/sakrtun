package dnsttclient

import (
	"crypto/tls"
	"crypto/x509"
)

func loadTLSConfig() (*tls.Config, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = nil
	}

	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if pool != nil {
		config.RootCAs = pool
	}

	return config, nil
}
