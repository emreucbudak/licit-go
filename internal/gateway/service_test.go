package gateway

import (
	"testing"

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
