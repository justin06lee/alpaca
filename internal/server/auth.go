package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// authed wraps a handler so it requires the gateway's API key.
//
// The key is accepted as a bearer token (what OpenAI clients already send) or
// as X-Api-Key, so tooling that only knows the latter works without a shim.
func (s *Server) authed(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.keyMatches(presentedKey(r)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="alpaca"`)
			writeError(w, http.StatusUnauthorized,
				"missing or invalid API key — pass it as `Authorization: Bearer <key>`",
				"invalid_api_key")
			return
		}
		next(w, r)
	})
}

// presentedKey extracts a credential from the request, if any.
func presentedKey(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		// The scheme is case-insensitive per RFC 7235.
		if scheme, token, found := strings.Cut(header, " "); found && strings.EqualFold(scheme, "bearer") {
			return strings.TrimSpace(token)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// keyMatches compares in constant time so response latency cannot be used to
// recover the key one byte at a time.
//
// ConstantTimeCompare returns 0 for length mismatches without inspecting
// contents, so the length check below is only to keep that fast path explicit.
func (s *Server) keyMatches(presented string) bool {
	if s.opts.APIKey == "" {
		// A server with no key configured would otherwise accept everything.
		// Refuse instead of silently running wide open.
		return false
	}
	if len(presented) != len(s.opts.APIKey) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.opts.APIKey)) == 1
}

// errorBody mirrors OpenAI's error envelope so existing clients surface the
// message instead of printing a parse failure.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func writeError(w http.ResponseWriter, status int, message, code string) {
	kind := "api_error"
	switch {
	case status == http.StatusUnauthorized:
		kind = "authentication_error"
	case status == http.StatusNotFound:
		kind = "not_found_error"
	case status >= 400 && status < 500:
		kind = "invalid_request_error"
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Message: message, Type: kind, Code: code}})
}

// clientIP reports the peer address for logging. Proxy headers are ignored on
// purpose: alpaca is normally reached directly, and trusting a spoofable header
// would only put fiction in the logs.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
