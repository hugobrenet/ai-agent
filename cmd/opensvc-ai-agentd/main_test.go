package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hugobrenet/opensvc-ai-agent/internal/config"
)

func TestNewHTTPServerHardening(t *testing.T) {
	server := newHTTPServer("127.0.0.1:8090", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if server.MaxHeaderBytes != maxHTTPHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, maxHTTPHeaderBytes)
	}
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("server timeouts are not all positive: %+v", server)
	}
}

func TestHTTPSListener(t *testing.T) {
	fixture := httptest.NewTLSServer(nil)
	defer fixture.Close()
	certFile := filepath.Join(t.TempDir(), "agent.crt")
	keyFile := filepath.Join(t.TempDir(), "agent.key")
	cert := fixture.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	listener, tlsConfig, err := listenHTTPS(config.Config{ListenAddress: "127.0.0.1:0", TLSCertFile: certFile, TLSKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("TLS minimum version not enforced")
	}
	server := newHTTPServer(listener.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("request reached handler without TLS")
		}
		_, _ = io.WriteString(w, "healthy")
	}))
	server.TLSConfig = tlsConfig
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	response, err := fixture.Client().Get("https://" + listener.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "healthy" {
		t.Fatalf("HTTPS response=%q err=%v", body, err)
	}
	if response, err := http.Get("https://" + listener.Addr().String() + "/health"); err == nil {
		response.Body.Close()
		t.Fatal("untrusted TLS certificate accepted")
	}
	response, err = http.Get("http://" + listener.Addr().String() + "/health")
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("plaintext HTTP status=%d", response.StatusCode)
		}
	}
}

func TestHTTPSListenerRejectsInvalidCertificateBeforeBind(t *testing.T) {
	listener, _, err := listenHTTPS(config.Config{ListenAddress: "127.0.0.1:0", TLSCertFile: filepath.Join(t.TempDir(), "missing.crt"), TLSKeyFile: filepath.Join(t.TempDir(), "missing.key")})
	if err == nil || listener != nil {
		t.Fatalf("listener=%v err=%v", listener, err)
	}
}

func TestShutdownHTTPServerDrainsActiveRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(response, "done")
	}))
	t.Cleanup(server.Close)

	requestDone := make(chan error, 1)
	go func() {
		response, err := server.Client().Get(server.URL)
		if err == nil {
			_, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	<-started

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- shutdownHTTPServer(server.Config, time.Second)
	}()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before active request completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)

	if err := <-requestDone; err != nil {
		t.Fatalf("active request failed during graceful shutdown: %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
}

func TestShutdownHTTPServerForcesCancellationAfterDeadline(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	}))
	t.Cleanup(server.Close)

	requestDone := make(chan error, 1)
	go func() {
		response, err := server.Client().Get(server.URL)
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	<-started

	err := shutdownHTTPServer(server.Config, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("forced shutdown error = %v, want deadline exceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active request context was not canceled")
	}
	if err := <-requestDone; err == nil {
		t.Fatal("forced connection close returned no client error")
	}
}
