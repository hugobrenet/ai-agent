package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListenAddress     = "127.0.0.1:8090"
	DefaultMaxConcurrentAsks = 4
	DefaultShutdownTimeout   = 30 * time.Second
	maximumMaxConcurrentAsks = 128
	minimumShutdownTimeout   = time.Second
	maximumShutdownTimeout   = 5 * time.Minute
)

type Config struct {
	ListenAddress     string
	TLSCertFile       string
	TLSKeyFile        string
	MaxConcurrentAsks int
	ShutdownTimeout   time.Duration
}

func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	if strings.TrimSpace(getenv("OPENSVC_AI_SOCKET_PATH")) != "" {
		return Config{}, fmt.Errorf("OPENSVC_AI_SOCKET_PATH is no longer supported; configure OPENSVC_AI_LISTEN_ADDR and TLS certificate/key files")
	}
	address := strings.TrimSpace(getenv("OPENSVC_AI_LISTEN_ADDR"))
	if address == "" {
		address = DefaultListenAddress
	}
	host, port, err := net.SplitHostPort(address)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("parse OPENSVC_AI_LISTEN_ADDR: expected an explicit IP address and port between 1 and 65535")
	}
	certFile, err := absoluteFile(getenv("OPENSVC_AI_TLS_CERT_FILE"))
	if err != nil {
		return Config{}, fmt.Errorf("parse OPENSVC_AI_TLS_CERT_FILE: %w", err)
	}
	keyFile, err := absoluteFile(getenv("OPENSVC_AI_TLS_KEY_FILE"))
	if err != nil {
		return Config{}, fmt.Errorf("parse OPENSVC_AI_TLS_KEY_FILE: %w", err)
	}
	maxConcurrentAsks := DefaultMaxConcurrentAsks
	if value := strings.TrimSpace(getenv("OPENSVC_AI_MAX_CONCURRENT_ASKS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maximumMaxConcurrentAsks {
			return Config{}, fmt.Errorf(
				"parse OPENSVC_AI_MAX_CONCURRENT_ASKS %q: expected an integer between 1 and %d",
				value,
				maximumMaxConcurrentAsks,
			)
		}
		maxConcurrentAsks = parsed
	}
	shutdownTimeout := DefaultShutdownTimeout
	if value := strings.TrimSpace(getenv("OPENSVC_AI_SHUTDOWN_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < minimumShutdownTimeout || parsed > maximumShutdownTimeout {
			return Config{}, fmt.Errorf(
				"parse OPENSVC_AI_SHUTDOWN_TIMEOUT %q: expected a duration between %s and %s",
				value,
				minimumShutdownTimeout,
				maximumShutdownTimeout,
			)
		}
		shutdownTimeout = parsed
	}
	return Config{
		ListenAddress:     address,
		TLSCertFile:       certFile,
		TLSKeyFile:        keyFile,
		MaxConcurrentAsks: maxConcurrentAsks,
		ShutdownTimeout:   shutdownTimeout,
	}, nil
}

func absoluteFile(value string) (string, error) {
	path := filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	if path == string(filepath.Separator) {
		return "", fmt.Errorf("path must name a file")
	}
	return path, nil
}
