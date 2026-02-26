package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ════════════════════════════════════════════════════════════════
//  API Key Authentication Middleware
//
//  In production mode, every request to protected routes must
//  include a valid API key in the Authorization header:
//    Authorization: Bearer <api-key>
//
//  Keys are loaded from config. This is stateless — no sessions,
//  no cookies — making it horizontally scalable.
// ════════════════════════════════════════════════════════════════

// AuthConfig holds authentication settings.
type AuthConfig struct {
	Enabled   bool              // If false, auth is bypassed (dev mode)
	APIKeys   map[string]string // key → client_id mapping
	SkipPaths []string          // Paths that skip auth (e.g., /health)
}

// APIKeyAuth returns middleware that validates API keys.
func APIKeyAuth(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			// Skip if auth is disabled (dev/test mode)
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Skip for allowlisted paths (health probes, etc.)
			for _, path := range cfg.SkipPaths {
				if strings.HasPrefix(r.URL.Path, path) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Extract bearer token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeAuthError(w, "MISSING_AUTH", "Authorization header is required")
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeAuthError(w, "INVALID_AUTH_FORMAT", "Authorization header must be: Bearer <api-key>")
				return
			}

			apiKey := strings.TrimSpace(parts[1])
			if apiKey == "" {
				writeAuthError(w, "EMPTY_KEY", "API key cannot be empty")
				return
			}

			// Constant-time key comparison
			clientID := ""
			authenticated := false
			for key, cid := range cfg.APIKeys {
				if subtle.ConstantTimeCompare([]byte(apiKey), []byte(key)) == 1 {
					clientID = cid
					authenticated = true
					break
				}
			}

			if !authenticated {
				writeAuthError(w, "INVALID_KEY", "Invalid API key")
				return
			}

			// Inject client identity into headers (downstream handlers can read it)
			r.Header.Set("X-Client-ID", clientID)

			next.ServeHTTP(w, r)
		})
	}
}

func writeAuthError(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(models.APIResponse{
		Success: false,
		Error: &models.APIError{
			Code:    code,
			Message: message,
		},
		Timestamp: time.Now().UTC(),
	})
}
