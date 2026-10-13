package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
)

const corsMethods = "GET, POST, PATCH, DELETE"

// withCORS runs before authentication and does not wrap the ResponseWriter,
// preserving Flusher and write-deadline support for streamed turns.
// backendHeaders are the request headers the backends read.
func withCORS(next http.Handler, origins []string, backendHeaders []string) (http.Handler, error) {
	if len(origins) == 0 {
		return next, nil
	}
	allowed := make(map[string]bool, len(origins))
	for _, origin := range origins {
		if origin == "" || origin == "*" && len(origins) != 1 {
			return nil, fmt.Errorf("API CORS origins must be nonempty; '*' must be used alone")
		}
		allowed[origin] = true
	}
	allowAll := allowed["*"]
	names := []string{"Authorization", "Content-Type"}
	for _, header := range backendHeaders {
		if !slices.ContainsFunc(names, func(name string) bool { return strings.EqualFold(name, header) }) {
			names = append(names, header)
		}
	}
	allowedHeaders := strings.Join(names, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		values := r.Header.Values("Origin")
		if len(values) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if len(values) != 1 || values[0] == "" || (!allowAll && !allowed[values[0]]) {
			writeCORSForbidden(w)
			return
		}
		origin := values[0]
		if allowAll {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if r.Method != http.MethodOptions || len(r.Header.Values("Access-Control-Request-Method")) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		methods := r.Header.Values("Access-Control-Request-Method")
		if len(methods) != 1 || !corsMethodAllowed(methods[0]) || !corsHeadersAllowed(r.Header.Values("Access-Control-Request-Headers"), names) {
			writeCORSForbidden(w)
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", corsMethods)
		w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
		w.Header().Set("Access-Control-Max-Age", "600")
		// No cookies/automatic browser credentials are allowed. The client
		// uses credentials: "omit" and supplies its Bearer header explicitly.
		w.WriteHeader(http.StatusNoContent)
	}), nil
}

func corsMethodAllowed(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func corsHeadersAllowed(values []string, allowed []string) bool {
	for _, value := range values {
		for _, header := range strings.Split(value, ",") {
			header = strings.TrimSpace(header)
			if !slices.ContainsFunc(allowed, func(name string) bool { return strings.EqualFold(name, header) }) {
				return false
			}
		}
	}
	return true
}

func writeCORSForbidden(w http.ResponseWriter) {
	writeJSONError(w, http.StatusForbidden, "cors_forbidden", "cross-origin request is not allowed")
}
