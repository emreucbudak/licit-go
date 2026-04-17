package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBConfig_DSN(t *testing.T) {
	tests := []struct {
		name     string
		cfg      DBConfig
		expected string
	}{
		{
			name: "standard config",
			cfg: DBConfig{
				Host:     "localhost",
				Port:     5432,
				User:     "licit",
				Password: "secret",
				DBName:   "licitdb",
				SSLMode:  "disable",
			},
			expected: "host=localhost port=5432 user=licit password=secret dbname=licitdb sslmode=disable",
		},
		{
			name: "remote host with SSL",
			cfg: DBConfig{
				Host:     "db.example.com",
				Port:     5433,
				User:     "admin",
				Password: "p@ssw0rd!",
				DBName:   "production",
				SSLMode:  "require",
			},
			expected: "host=db.example.com port=5433 user=admin password=p@ssw0rd! dbname=production sslmode=require",
		},
		{
			name: "zero port",
			cfg: DBConfig{
				Host:     "127.0.0.1",
				Port:     0,
				User:     "test",
				Password: "",
				DBName:   "testdb",
				SSLMode:  "disable",
			},
			expected: "host=127.0.0.1 port=0 user=test password= dbname=testdb sslmode=disable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cfg.DSN()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// writeTestYAML creates a temporary YAML config file and returns its path.
func writeTestYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
	return path
}

func TestLoad_ValidYAML(t *testing.T) {
	yaml := `
server:
  bidding_port: 5160
  streamer_port: 5161
  payment_port: 5162

nats:
  url: nats://localhost:4222

database:
  host: localhost
  port: 5432
  user: licit
  password: devpass
  dbname: LicitBiddingDb
  sslmode: disable

redis:
  addr: localhost:6379
  password: redispass

dotnet:
  wallet_service_url: http://localhost:5142
  tendering_service_url: http://localhost:5132
  auth_service_url: http://localhost:5122

jwt:
  secret: supersecretkey
  issuer: TestIssuer
  audience: TestAudience

gateway:
  port: 5100
  health_check_interval: 15s
  health_check_timeout: 2s
  routes:
    - name: auth
      match: prefix
      path: /api/auth
      cluster: auth
    - name: frontend
      match: catch_all
      path: /
      cluster: frontend
  clusters:
    auth:
      load_balancing_policy: round_robin
      health_path: /health
      destinations:
        - http://localhost:5122
    frontend:
      load_balancing_policy: round_robin
      health_path: /
      destinations:
        - http://localhost:5173
`
	path := writeTestYAML(t, yaml)
	cfg, err := Load(path)

	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Server
	assert.Equal(t, 5160, cfg.Server.BiddingPort)
	assert.Equal(t, 5161, cfg.Server.StreamerPort)
	assert.Equal(t, 5162, cfg.Server.PaymentPort)

	// NATS
	assert.Equal(t, "nats://localhost:4222", cfg.NATS.URL)

	// Database
	assert.Equal(t, "localhost", cfg.DB.Host)
	assert.Equal(t, 5432, cfg.DB.Port)
	assert.Equal(t, "licit", cfg.DB.User)
	assert.Equal(t, "devpass", cfg.DB.Password)
	assert.Equal(t, "LicitBiddingDb", cfg.DB.DBName)
	assert.Equal(t, "disable", cfg.DB.SSLMode)

	// Redis
	assert.Equal(t, "localhost:6379", cfg.Redis.Addr)
	assert.Equal(t, "redispass", cfg.Redis.Password)

	// DotNet
	assert.Equal(t, "http://localhost:5142", cfg.DotNet.WalletServiceURL)
	assert.Equal(t, "http://localhost:5132", cfg.DotNet.TenderingServiceURL)
	assert.Equal(t, "http://localhost:5122", cfg.DotNet.AuthServiceURL)

	// JWT
	assert.Equal(t, "supersecretkey", cfg.JWT.Secret)
	assert.Equal(t, "TestIssuer", cfg.JWT.Issuer)
	assert.Equal(t, "TestAudience", cfg.JWT.Audience)

	// Gateway
	assert.Equal(t, 5100, cfg.Gateway.ListenPort())
	assert.Equal(t, "15s", cfg.Gateway.HealthCheckInterval)
	assert.Equal(t, "2s", cfg.Gateway.HealthCheckTimeout)
	require.Len(t, cfg.Gateway.Routes, 2)
	assert.Equal(t, "auth", cfg.Gateway.Routes[0].Cluster)
	assert.Equal(t, "round_robin", cfg.Gateway.Clusters["auth"].LoadBalancingPolicy)
	assert.Equal(t, []string{"http://localhost:5173"}, cfg.Gateway.Clusters["frontend"].Destinations)
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.yaml")

	assert.Nil(t, cfg)
	assert.Error(t, err)
}

func TestLoad_InvalidYAML(t *testing.T) {
	content := `
server:
  bidding_port: [[[invalid yaml
  this is not: valid: yaml: at: all
`
	path := writeTestYAML(t, content)
	cfg, err := Load(path)

	assert.Nil(t, cfg)
	assert.Error(t, err)
}

func TestLoad_EmptyFile(t *testing.T) {
	path := writeTestYAML(t, "")
	cfg, err := Load(path)

	// Empty YAML is valid, it just produces a zero-value Config
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, 0, cfg.Server.BiddingPort)
	assert.Empty(t, cfg.NATS.URL)
	assert.Empty(t, cfg.DB.Host)
}

func TestLoad_PartialConfig(t *testing.T) {
	yaml := `
server:
  bidding_port: 8080
database:
  host: db.local
  port: 5432
`
	path := writeTestYAML(t, yaml)
	cfg, err := Load(path)

	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 8080, cfg.Server.BiddingPort)
	assert.Equal(t, 0, cfg.Server.StreamerPort) // zero value for unset int
	assert.Equal(t, "db.local", cfg.DB.Host)
	assert.Equal(t, 5432, cfg.DB.Port)
	assert.Empty(t, cfg.DB.User) // unset fields are zero-value
}

func TestGatewayConfig_Defaults(t *testing.T) {
	cfg := GatewayConfig{}

	assert.Equal(t, 5100, cfg.ListenPort())
	assert.Equal(t, 10*time.Second, cfg.CheckInterval())
	assert.Equal(t, 3*time.Second, cfg.CheckTimeout())
}
