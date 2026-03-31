package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	NATS    NATSConfig    `yaml:"nats"`
	DB      DBConfig      `yaml:"database"`
	Redis   RedisConfig   `yaml:"redis"`
	DotNet  DotNetConfig  `yaml:"dotnet"`
	JWT     JWTConfig     `yaml:"jwt"`
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

func (d DBConfig) DSN() string {
	return "host=" + d.Host +
		" port=" + itoa(d.Port) +
		" user=" + d.User +
		" password=" + d.Password +
		" dbname=" + d.DBName +
		" sslmode=" + d.SSLMode
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
