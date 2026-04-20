package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	NATS    NATSConfig    `yaml:"nats"`
	DB      DBConfig      `yaml:"database"`
	Redis   RedisConfig   `yaml:"redis"`
	DotNet  DotNetConfig  `yaml:"dotnet"`
	JWT     JWTConfig     `yaml:"jwt"`
	Gateway GatewayConfig `yaml:"gateway"`
}

type ServerConfig struct {
	BiddingPort  int `yaml:"bidding_port"`
	StreamerPort int `yaml:"streamer_port"`
	PaymentPort  int `yaml:"payment_port"`
}

type NATSConfig struct {
	URL string `yaml:"url"`
}

type DBConfig struct {
	ConnectionString string `yaml:"connection_string"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

type RedisConfig struct {
	ConnectionString string `yaml:"connection_string"`
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	TLS      bool   `yaml:"tls"`
}

type ResolvedRedisConfig struct {
	Addr     string
	Password string
	TLS      bool
}

type DotNetConfig struct {
	WalletServiceURL    string `yaml:"wallet_service_url"`
	TenderingServiceURL string `yaml:"tendering_service_url"`
	AuthServiceURL      string `yaml:"auth_service_url"`
}

type JWTConfig struct {
	Secret   string `yaml:"secret"`
	Issuer   string `yaml:"issuer"`
	Audience string `yaml:"audience"`
}

type GatewayConfig struct {
	Port                int                             `yaml:"port"`
	HealthCheckInterval string                          `yaml:"health_check_interval"`
	HealthCheckTimeout  string                          `yaml:"health_check_timeout"`
	CORS                GatewayCORSConfig               `yaml:"cors"`
	RateLimit           GatewayRateLimitConfig          `yaml:"rate_limit"`
	Routes              []GatewayRouteConfig            `yaml:"routes"`
	Clusters            map[string]GatewayClusterConfig `yaml:"clusters"`
}

type GatewayCORSConfig struct {
	Enabled          bool     `yaml:"enabled"`
	AllowedOrigins   []string `yaml:"allowed_origins"`
	AllowedMethods   []string `yaml:"allowed_methods"`
	AllowedHeaders   []string `yaml:"allowed_headers"`
	ExposedHeaders   []string `yaml:"exposed_headers"`
	AllowCredentials bool     `yaml:"allow_credentials"`
	MaxAge           string   `yaml:"max_age"`
}

type GatewayRateLimitConfig struct {
	Enabled        bool   `yaml:"enabled"`
	BucketSize     int    `yaml:"bucket_size"`
	RefillInterval string `yaml:"refill_interval"`
	KeyPrefix      string `yaml:"key_prefix"`
}

type GatewayRouteConfig struct {
	Name    string `yaml:"name"`
	Match   string `yaml:"match"`
	Path    string `yaml:"path"`
	Cluster string `yaml:"cluster"`
}

type GatewayClusterConfig struct {
	LoadBalancingPolicy string   `yaml:"load_balancing_policy"`
	HealthPath          string   `yaml:"health_path"`
	Destinations        []string `yaml:"destinations"`
}

func (d DBConfig) DSN() string {
	if raw := strings.TrimSpace(d.ConnectionString); raw != "" {
		return raw
	}

	return "host=" + d.Host +
		" port=" + strconv.Itoa(d.Port) +
		" user=" + d.User +
		" password=" + d.Password +
		" dbname=" + d.DBName +
		" sslmode=" + d.SSLMode
}

func (r RedisConfig) Resolve() (ResolvedRedisConfig, error) {
	resolved := ResolvedRedisConfig{
		Addr:     strings.TrimSpace(r.Addr),
		Password: r.Password,
		TLS:      r.TLS,
	}

	if raw := strings.TrimSpace(r.ConnectionString); raw != "" {
		addr, password, tlsEnabled, err := parseRedisConnectionString(raw)
		if err != nil {
			return ResolvedRedisConfig{}, err
		}

		if addr != "" {
			resolved.Addr = addr
		}

		if password != "" {
			resolved.Password = password
		}

		resolved.TLS = resolved.TLS || tlsEnabled
	}

	if resolved.Addr == "" {
		return ResolvedRedisConfig{}, fmt.Errorf("redis address is required")
	}

	return resolved, nil
}

func (g GatewayConfig) ListenPort() int {
	if g.Port == 0 {
		return 5100
	}

	return g.Port
}

func (g GatewayConfig) CheckInterval() time.Duration {
	return parseDurationOrDefault(g.HealthCheckInterval, 10*time.Second)
}

func (g GatewayConfig) CheckTimeout() time.Duration {
	return parseDurationOrDefault(g.HealthCheckTimeout, 3*time.Second)
}

func (c GatewayCORSConfig) MaxAgeDuration() time.Duration {
	duration := parseDurationOrDefault(c.MaxAge, 10*time.Minute)
	if duration < 0 {
		return 10 * time.Minute
	}

	return duration
}

func (r GatewayRateLimitConfig) Capacity() int {
	if r.BucketSize <= 0 {
		return 5
	}

	return r.BucketSize
}

func (r GatewayRateLimitConfig) RefillIntervalDuration() time.Duration {
	duration := parseDurationOrDefault(r.RefillInterval, 2*time.Second)
	if duration <= 0 {
		return 2 * time.Second
	}

	return duration
}

func (r GatewayRateLimitConfig) RedisKeyPrefix() string {
	if strings.TrimSpace(r.KeyPrefix) == "" {
		return "licit:gateway:rate_limit"
	}

	return r.KeyPrefix
}

func parseDurationOrDefault(raw string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}

	duration, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}

	return duration
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	applyEnvOverrides(&cfg)
	return &cfg, nil
}

func parseRedisConnectionString(raw string) (string, string, bool, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return "", "", false, fmt.Errorf("redis connection string is empty")
	}

	addr := strings.TrimSpace(parts[0])
	if addr == "" {
		return "", "", false, fmt.Errorf("redis connection string address is required")
	}

	password := ""
	tlsEnabled := false

	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		keyValue := strings.SplitN(part, "=", 2)
		if len(keyValue) != 2 {
			continue
		}

		key := strings.ToLower(strings.TrimSpace(keyValue[0]))
		value := strings.TrimSpace(keyValue[1])

		switch key {
		case "password":
			password = value
		case "ssl":
			tlsEnabled = strings.EqualFold(value, "true")
		}
	}

	return addr, password, tlsEnabled, nil
}

func applyEnvOverrides(cfg *Config) {
	cfg.NATS.URL = envOrDefault("LICIT_GO_NATS_URL", cfg.NATS.URL)

	cfg.DB.ConnectionString = envOrDefault("LICIT_GO_BIDDING_DB_CONNECTION_STRING", cfg.DB.ConnectionString)
	cfg.DB.Host = envOrDefault("LICIT_GO_BIDDING_DB_HOST", cfg.DB.Host)
	cfg.DB.Port = envIntOrDefault("LICIT_GO_BIDDING_DB_PORT", cfg.DB.Port)
	cfg.DB.User = envOrDefault("LICIT_GO_BIDDING_DB_USER", cfg.DB.User)
	cfg.DB.Password = envOrDefault("LICIT_GO_BIDDING_DB_PASSWORD", cfg.DB.Password)
	cfg.DB.DBName = envOrDefault("LICIT_GO_BIDDING_DB_NAME", cfg.DB.DBName)
	cfg.DB.SSLMode = envOrDefault("LICIT_GO_BIDDING_DB_SSLMODE", cfg.DB.SSLMode)

	cfg.Redis.ConnectionString = envOrDefault("LICIT_GO_REDIS_CONNECTION_STRING", cfg.Redis.ConnectionString)
	cfg.Redis.Addr = envOrDefault("LICIT_GO_REDIS_ADDR", cfg.Redis.Addr)
	cfg.Redis.Password = envOrDefault("LICIT_GO_REDIS_PASSWORD", cfg.Redis.Password)
	if raw := strings.TrimSpace(os.Getenv("LICIT_GO_REDIS_TLS")); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			cfg.Redis.TLS = parsed
		}
	}

	cfg.DotNet.WalletServiceURL = envOrDefault("LICIT_GO_WALLET_SERVICE_URL", cfg.DotNet.WalletServiceURL)
	cfg.DotNet.TenderingServiceURL = envOrDefault("LICIT_GO_TENDERING_SERVICE_URL", cfg.DotNet.TenderingServiceURL)
	cfg.DotNet.AuthServiceURL = envOrDefault("LICIT_GO_AUTH_SERVICE_URL", cfg.DotNet.AuthServiceURL)

	cfg.JWT.Secret = envOrDefault("LICIT_GO_JWT_SECRET", cfg.JWT.Secret)
	cfg.JWT.Issuer = envOrDefault("LICIT_GO_JWT_ISSUER", cfg.JWT.Issuer)
	cfg.JWT.Audience = envOrDefault("LICIT_GO_JWT_AUDIENCE", cfg.JWT.Audience)

	if origins := splitCSVEnv("LICIT_GO_GATEWAY_ALLOWED_ORIGINS"); len(origins) > 0 {
		cfg.Gateway.CORS.AllowedOrigins = origins
	}
}

func envOrDefault(key, current string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}

	return current
}

func envIntOrDefault(key string, current int) int {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}

	return current
}

func splitCSVEnv(key string) []string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return trimNonEmpty(strings.Split(value, ","))
	}

	return nil
}

func trimNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}

	return out
}
