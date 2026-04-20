package gateway

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/licit/licit-go/internal/config"
)

var defaultCORSMethods = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodOptions,
}

var defaultCORSHeaders = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"Origin",
	"X-Request-ID",
	"X-Requested-With",
}

var defaultCORSExposedHeaders = []string{
	"X-RateLimit-Limit",
	"X-RateLimit-Remaining",
	"X-RateLimit-Reset",
	"Retry-After",
}

type corsPolicy struct {
	allowAllOrigins bool
	origins         map[string]struct{}
	methods         []string
	headers         []string
	exposedHeaders  []string
	credentials     bool
	maxAgeSeconds   int
}

func corsMiddleware(cfg config.GatewayCORSConfig, next http.Handler) http.Handler {
	if !cfg.Enabled {
		return next
	}

	policy := newCORSPolicy(cfg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		allowedOrigin, ok := policy.allowedOrigin(origin)
		if !ok {
			if isPreflight(r) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cors origin not allowed"})
				return
			}

			next.ServeHTTP(w, r)
			return
		}

		policy.writeHeaders(w.Header(), allowedOrigin, r)
		if isPreflight(r) {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func newCORSPolicy(cfg config.GatewayCORSConfig) corsPolicy {
	origins := trimStrings(cfg.AllowedOrigins)
	if len(origins) == 0 {
		origins = []string{"*"}
	}

	originSet := make(map[string]struct{}, len(origins))
	allowAllOrigins := false
	for _, origin := range origins {
		if origin == "*" {
			allowAllOrigins = true
			continue
		}

		originSet[origin] = struct{}{}
	}

	methods := upperStrings(trimStrings(cfg.AllowedMethods))
	if len(methods) == 0 {
		methods = defaultCORSMethods
	}

	headers := trimStrings(cfg.AllowedHeaders)
	if len(headers) == 0 {
		headers = defaultCORSHeaders
	}

	exposedHeaders := trimStrings(cfg.ExposedHeaders)
	if len(exposedHeaders) == 0 {
		exposedHeaders = defaultCORSExposedHeaders
	}

	return corsPolicy{
		allowAllOrigins: allowAllOrigins,
		origins:         originSet,
		methods:         methods,
		headers:         headers,
		exposedHeaders:  exposedHeaders,
		credentials:     cfg.AllowCredentials,
		maxAgeSeconds:   int(cfg.MaxAgeDuration().Seconds()),
	}
}

func (p corsPolicy) allowedOrigin(origin string) (string, bool) {
	if p.allowAllOrigins {
		if p.credentials {
			return origin, true
		}

		return "*", true
	}

	if _, ok := p.origins[origin]; ok {
		return origin, true
	}

	return "", false
}

func (p corsPolicy) writeHeaders(header http.Header, allowedOrigin string, r *http.Request) {
	header.Set("Access-Control-Allow-Origin", allowedOrigin)
	addVary(header, "Origin")

	if p.credentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}

	if len(p.exposedHeaders) > 0 {
		header.Set("Access-Control-Expose-Headers", strings.Join(p.exposedHeaders, ", "))
	}

	if !isPreflight(r) {
		return
	}

	header.Set("Access-Control-Allow-Methods", strings.Join(p.methods, ", "))
	header.Set("Access-Control-Allow-Headers", p.allowedHeadersForPreflight(r))
	header.Set("Access-Control-Max-Age", strconv.Itoa(p.maxAgeSeconds))
	addVary(header, "Access-Control-Request-Method")
	addVary(header, "Access-Control-Request-Headers")
}

func (p corsPolicy) allowedHeadersForPreflight(r *http.Request) string {
	if len(p.headers) == 1 && p.headers[0] == "*" {
		requested := r.Header.Get("Access-Control-Request-Headers")
		if requested != "" {
			return requested
		}
	}

	return strings.Join(p.headers, ", ")
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func trimStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}

	return out
}

func upperStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.ToUpper(value))
	}

	return out
}

func addVary(header http.Header, value string) {
	existing := header.Get("Vary")
	if existing == "" {
		header.Set("Vary", value)
		return
	}

	for _, part := range strings.Split(existing, ",") {
		if strings.EqualFold(strings.TrimSpace(part), value) {
			return
		}
	}

	header.Set("Vary", existing+", "+value)
}
