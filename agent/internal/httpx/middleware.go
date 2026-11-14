// Package httpx has the HTTP guards in front of the MCP handler: client address list and bearer key.
package httpx

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// writeJSON sends a small JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// clientIP returns the address of the TCP peer; forwarded-for headers are never trusted.
func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

// Allowlist rejects clients outside nets with 403; the paths in exempt skip the check.
func Allowlist(nets []*net.IPNet, exempt map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		if ip == nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: missing client address"})
			return
		}
		for _, n := range nets {
			if n.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: client IP " + ip.String() + " is not in allowlist"})
	})
}

// Auth requires "Authorization: Bearer <key>" except on the public paths; an empty key rejects every request; the comparison takes the same time
// whatever the length or content of the supplied key.
func Auth(key string, public map[string]bool, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(key))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		got := sha256.Sum256([]byte(token))
		if key == "" || !ok || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
