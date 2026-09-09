package tools

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

type TLSOptions struct {
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
}

// LoadConfig loads client TLS settings. CAFile extends the system trust store.
func (o TLSOptions) LoadConfig(enabled bool) (*tls.Config, error) {
	if !enabled {
		if len(o.CAFile) > 0 || len(o.CertFile) > 0 || len(o.KeyFile) > 0 || len(o.ServerName) > 0 {
			return nil, errors.New("TLS files and server name require TLS to be enabled")
		}
		return nil, nil
	}
	return LoadTLSConfig(o.CAFile, o.CertFile, o.KeyFile, o.ServerName)
}

// LoadServerConfig requires a server certificate and key when TLS is enabled.
// Setting CAFile enables mutual TLS and trusts only clients signed by those CAs.
func (o TLSOptions) LoadServerConfig(enabled bool) (*tls.Config, error) {
	if !enabled {
		return o.LoadConfig(false)
	}
	if len(o.ServerName) > 0 {
		return nil, errors.New("server name is only supported for client TLS configuration")
	}
	if len(o.CertFile) == 0 || len(o.KeyFile) == 0 {
		return nil, errors.New("server TLS requires a certificate and key")
	}
	config, err := LoadTLSConfig("", o.CertFile, o.KeyFile, "")
	if err != nil {
		return nil, err
	}
	if len(o.CAFile) > 0 {
		clients := x509.NewCertPool()
		if err := appendTLSCAFile(clients, o.CAFile); err != nil {
			return nil, err
		}
		config.ClientCAs = clients
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return config, nil
}

// LoadTLSConfig uses system trust by default and always verifies the server certificate.
func LoadTLSConfig(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if len(caFile) > 0 {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificate pool: %w", err)
		}
		if err := appendTLSCAFile(roots, caFile); err != nil {
			return nil, err
		}
		config.RootCAs = roots
	}
	if (len(certFile) == 0) != (len(keyFile) == 0) {
		return nil, errors.New("TLS certificate and key must be configured together")
	}
	if len(certFile) > 0 {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func appendTLSCAFile(pool *x509.CertPool, caFile string) error {
	// #nosec G304 -- CA paths are set by trusted service configuration.
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("read CA certificate: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("CA file contains no valid certificates")
	}
	return nil
}

func ValidateTLSConfig(config *tls.Config) error {
	if config == nil {
		return nil
	}
	if config.InsecureSkipVerify {
		return errors.New("TLS certificate verification cannot be disabled")
	}
	if (config.MinVersion != 0 && config.MinVersion < tls.VersionTLS12) ||
		(config.MaxVersion != 0 && config.MaxVersion < tls.VersionTLS12) {
		return errors.New("TLS 1.2 or newer is required")
	}
	return nil
}
