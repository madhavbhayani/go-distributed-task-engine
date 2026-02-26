package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// Recovery recovers from panics, logs the stack trace, and returns a 500 JSON response.
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					stack := string(debug.Stack())
					logger.Error("PANIC recovered",
						slog.Any("error", err),
						slog.String("stack", stack),
						slog.String("path", r.URL.Path),
						slog.String("method", r.Method),
					)

					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)

					resp := models.APIResponse{
						Success: false,
						Error: &models.APIError{
							Code:    "INTERNAL_ERROR",
							Message: "An unexpected error occurred",
						},
						Timestamp: time.Now().UTC(),
					}
					json.NewEncoder(w).Encode(resp)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
