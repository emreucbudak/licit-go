package config

import (
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
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
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
	return "host=" + d.Host +
		" port=" + strconv.Itoa(d.Port) +
		" user=" + d.User +
		" password=" + d.Password +
		" dbname=" + d.DBName +
		" sslmode=" + d.SSLMode
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
	return &cfg, nil
}
