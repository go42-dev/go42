package events

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

func (o TLSOptions) LoadConfig(enabled bool) (*tls.Config, error) {
	if !enabled {
		if len(o.CAFile) > 0 || len(o.CertFile) > 0 || len(o.KeyFile) > 0 || len(o.ServerName) > 0 {
			return nil, errors.New("broker TLS files and server name require TLS to be enabled")
		}
		return nil, nil
	}
	return LoadTLSConfig(o.CAFile, o.CertFile, o.KeyFile, o.ServerName)
}

// LoadTLSConfig uses system trust by default and always verifies the server certificate.
func LoadTLSConfig(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if len(caFile) > 0 {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read broker CA certificate: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificate pool: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("broker CA file contains no valid certificates")
		}
		config.RootCAs = roots
	}
	if (len(certFile) == 0) != (len(keyFile) == 0) {
		return nil, errors.New("broker client certificate and key must be configured together")
	}
	if len(certFile) > 0 {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load broker client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func ValidateTLSConfig(config *tls.Config) error {
	if config == nil {
		return nil
	}
	if config.InsecureSkipVerify {
		return errors.New("broker TLS certificate verification cannot be disabled")
	}
	if (config.MinVersion != 0 && config.MinVersion < tls.VersionTLS12) ||
		(config.MaxVersion != 0 && config.MaxVersion < tls.VersionTLS12) {
		return errors.New("broker TLS requires TLS 1.2 or newer")
	}
	return nil
}
