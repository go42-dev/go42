package events

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBrokerTLSVerifiesTrustAndServerIdentity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
	}), 0600))
	config, err := (TLSOptions{CAFile: caFile, ServerName: "example.com"}).LoadConfig(true)
	require.NoError(t, err)
	require.Equal(t, uint16(tls.VersionTLS12), config.MinVersion)
	require.False(t, config.InsecureSkipVerify)
	transport := &http.Transport{TLSClientConfig: config}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	config, err = (TLSOptions{CAFile: caFile, ServerName: "wrong.example"}).LoadConfig(true)
	require.NoError(t, err)
	wrongHost := &http.Transport{TLSClientConfig: config}
	defer wrongHost.CloseIdleConnections()
	response, err = (&http.Client{Transport: wrongHost}).Get(server.URL)
	if response != nil {
		_ = response.Body.Close()
	}
	require.ErrorContains(t, err, "certificate")
	config, err = (TLSOptions{ServerName: "example.com"}).LoadConfig(true)
	require.NoError(t, err)
	untrusted := &http.Transport{TLSClientConfig: config}
	defer untrusted.CloseIdleConnections()
	response, err = (&http.Client{Transport: untrusted}).Get(server.URL)
	if response != nil {
		_ = response.Body.Close()
	}
	require.ErrorContains(t, err, "certificate")
}

func TestBrokerTLSRejectsIncompleteOrUnsafeConfiguration(t *testing.T) {
	config, err := (TLSOptions{}).LoadConfig(false)
	require.NoError(t, err)
	require.Nil(t, config)
	for _, options := range []TLSOptions{
		{CAFile: "ca.pem"}, {CertFile: "client.pem"}, {KeyFile: "client.key"}, {ServerName: "example.com"},
	} {
		_, err := options.LoadConfig(false)
		require.ErrorContains(t, err, "require TLS to be enabled")
	}
	_, err = (TLSOptions{CertFile: "client.pem"}).LoadConfig(true)
	require.ErrorContains(t, err, "together")
	invalidCA := filepath.Join(t.TempDir(), "bad-ca.pem")
	require.NoError(t, os.WriteFile(invalidCA, []byte("invalid"), 0600))
	_, err = (TLSOptions{CAFile: invalidCA}).LoadConfig(true)
	require.ErrorContains(t, err, "no valid certificates")
	require.Error(t, ValidateTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	require.Error(t, ValidateTLSConfig(&tls.Config{MinVersion: tls.VersionTLS10}))
}

func TestBrokerTLSLoadsClientIdentityForMutualTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1),
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600))
	config, err := (TLSOptions{CAFile: certFile, CertFile: certFile, KeyFile: keyFile}).LoadConfig(true)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: config.Certificates,
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: config.RootCAs}
	server.StartTLS()
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: config}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Get(server.URL)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
}
