package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// parseCORSOrigins accepts exact HTTP(S) origins or a standalone wildcard.
// Empty configuration leaves browser cross-origin access disabled.
func parseCORSOrigins(value string) ([]string, error) {
	if len(value) > 64<<10 {
		return nil, fmt.Errorf("origin list exceeds 64 KiB")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if value == "*" {
		return []string{"*"}, nil
	}
	var origins []string
	seen := make(map[string]bool)
	for _, entry := range strings.Split(value, ",") {
		origin, err := corsOrigin(strings.TrimSpace(entry))
		if err != nil {
			return nil, err
		}
		if !seen[origin] {
			origins = append(origins, origin)
			seen[origin] = true
		}
	}
	return origins, nil
}

func corsOrigin(value string) (string, error) {
	invalid := fmt.Errorf("expected comma-separated HTTP(S) origins without credentials, paths, queries, fragments or partial wildcards; '*' must be used alone")
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(value, "*\\ \t\r\n#") || strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.Contains(host, "%") {
		return "", invalid
	}
	if strings.HasPrefix(u.Host, "[") {
		if net.ParseIP(host) == nil {
			return "", invalid
		}
	} else {
		for _, r := range host {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
				return "", invalid
			}
		}
	}
	port := u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", invalid
		}
		port = strconv.Itoa(number)
		if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
			port = ""
		}
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}
