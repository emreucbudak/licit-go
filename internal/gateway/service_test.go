package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/licit/licit-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchRoute(t *testing.T) {
	service, err := New(config.GatewayConfig{
		Routes: []config.GatewayRouteConfig{
			{Name: "streamer", Match: "exact", Path: "/ws", Cluster: "streamer"},
			{Name: "auth", Match: "prefix", Path: "/api/auth", Cluster: "auth"},
			{Name: "frontend", Match: "catch_all", Path: "/", Cluster: "frontend"},
		},
		Clusters: map[string]config.GatewayClusterConfig{
			"streamer": {Destinations: []string{"http://localhost:5161"}},
			"auth":     {Destinations: []string{"http://localhost:5122"}},
			"frontend": {Destinations: []string{"http://localhost:5173"}, HealthPath: "/"},
		},
	})
	require.NoError(t, err)

	tests := []struct {
		path        string
		wantCluster string
	}{
		{path: "/ws", wantCluster: "streamer"},
		{path: "/api/auth/login", wantCluster: "auth"},
		{path: "/api/auth", wantCluster: "auth"},
		{path: "/api/authentication", wantCluster: "frontend"},
		{path: "/dashboard", wantCluster: "frontend"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			route, ok := service.matchRoute(tt.path)
			require.True(t, ok)
			assert.Equal(t, tt.wantCluster, route.Cluster)
		})
	}
}

func TestClusterNextBackendRoundRobin(t *testing.T) {
	cluster, err := newCluster("auth", config.GatewayClusterConfig{
		Destinations: []string{
			"http://localhost:5122",
			"http://localhost:5123",
		},
	})
	require.NoError(t, err)

	cluster.backends[0].setHealth(true, "", 200)
	cluster.backends[1].setHealth(true, "", 200)

	first, ok := cluster.NextBackend()
	require.True(t, ok)
	second, ok := cluster.NextBackend()
	require.True(t, ok)
	third, ok := cluster.NextBackend()
	require.True(t, ok)

	assert.Equal(t, "http://localhost:5122", first.target.String())
	assert.Equal(t, "http://localhost:5123", second.target.String())
	assert.Equal(t, "http://localhost:5122", third.target.String())
}

func TestClusterNextBackendSkipsUnhealthyNodes(t *testing.T) {
	cluster, err := newCluster("wallet", config.GatewayClusterConfig{
		Destinations: []string{
			"http://localhost:5142",
			"http://localhost:5143",
		},
	})
	require.NoError(t, err)

	cluster.backends[0].setHealth(false, "down", 503)
	cluster.backends[1].setHealth(true, "", 200)

	backend, ok := cluster.NextBackend()
	require.True(t, ok)
	assert.Equal(t, "http://localhost:5143", backend.target.String())
}

func TestGatewayHandlerCORSPreflight(t *testing.T) {
	service, err := New(config.GatewayConfig{
		CORS: config.GatewayCORSConfig{
			Enabled:          true,
			AllowedOrigins:   []string{"http://localhost:5173"},
			AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodOptions},
			AllowedHeaders:   []string{"Authorization", "Content-Type"},
			AllowCredentials: true,
			MaxAge:           "10m",
		},
		Routes: []config.GatewayRouteConfig{
			{Name: "auth", Match: "prefix", Path: "/api/auth", Cluster: "auth"},
		},
		Clusters: map[string]config.GatewayClusterConfig{
			"auth": {Destinations: []string{"http://localhost:5122"}},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")

	rr := httptest.NewRecorder()
	service.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "http://localhost:5173", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", rr.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), http.MethodPost)
	assert.Equal(t, "Authorization, Content-Type", rr.Header().Get("Access-Control-Allow-Headers"))
}

func TestRateLimiterRejectsDeniedClient(t *testing.T) {
	reset := time.Now().Add(time.Minute)
	store := &fakeRateLimitStore{
		decision: rateLimitDecision{
			Allowed:    false,
			Limit:      5,
			Remaining:  0,
			Reset:      reset,
			RetryAfter: 2 * time.Second,
		},
	}

	limiter := newRateLimiterWithStore(config.GatewayRateLimitConfig{
		Enabled:        true,
		BucketSize:     5,
		RefillInterval: "2s",
		KeyPrefix:      "test:rate_limit",
	}, store)

	nextCalled := false
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.RemoteAddr = "192.0.2.10:12345"

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, "5", rr.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", rr.Header().Get("X-RateLimit-Remaining"))
	assert.Equal(t, "2", rr.Header().Get("Retry-After"))
	require.Len(t, store.keys, 1)
	assert.Contains(t, store.keys[0], "test:rate_limit:")
	assert.Equal(t, 5, store.capacity)
	assert.Equal(t, 2*time.Second, store.refillInterval)
}

type fakeRateLimitStore struct {
	decision       rateLimitDecision
	err            error
	keys           []string
	capacity       int
	refillInterval time.Duration
}

func (s *fakeRateLimitStore) Allow(ctx context.Context, key string, capacity int, refillInterval time.Duration) (rateLimitDecision, error) {
	s.keys = append(s.keys, key)
	s.capacity = capacity
	s.refillInterval = refillInterval
	return s.decision, s.err
}

func (s *fakeRateLimitStore) Close() error {
	return nil
}
