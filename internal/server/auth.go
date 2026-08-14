package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// healthPath answers monitoring and stays reachable without a token. It reports
// only that the process is alive, and a tunnel or uptime check that had to hold
// the token would spread the secret further than the MCP client that needs it.
const healthPath = "/healthz"

// authMiddleware requires a bearer token on everything but the health check.
//
// Without a token this server is a remote shell for whoever finds the URL, and
// a public hostname is found: crawlers and scanners reach a tunnel within
// minutes. Configuring one is still optional, because a loopback-only run is
// already protected by the machine it runs on, and forcing a token there would
// break every existing local setup.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	want := []byte(strings.TrimSpace(s.cfg.AuthToken))
	if len(want) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}

		// Constant time: a plain == leaks the token one byte at a time to
		// anyone who can measure the reply.
		got := bearerToken(r)
		if subtle.ConstantTimeCompare(got, want) != 1 {
			log.Warn().
				Str("path", r.URL.Path).
				Str("remote", r.RemoteAddr).
				Bool("token_sent", len(got) > 0).
				Msg("rejected a request without a valid token")

			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-webcoder"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// bearerToken reads the credential out of an Authorization header.
//
// The scheme is matched case-insensitively, as RFC 7235 requires: clients do
// send "bearer", and refusing them would look like a wrong token.
func bearerToken(r *http.Request) []byte {
	const scheme = "bearer "

	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return nil
	}
	return []byte(strings.TrimSpace(header[len(scheme):]))
}
