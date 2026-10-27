package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	HTTPAddr                string
	DatabaseURL             string
	DBTimeout               time.Duration
	ShutdownTimeout         time.Duration
	CredentialsFile         string
	BankURL                 string
	ProviderCredentialsFile string
	ProviderTimeout         time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		HTTPAddr:                envOrDefault("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL:             os.Getenv("DATABASE_URL"),
		CredentialsFile:         envOrDefault("MERCHANT_CREDENTIALS_FILE", "config/merchants.example.json"),
		BankURL:                 envOrDefault("MOCK_BANK_URL", "http://127.0.0.1:8081"),
		ProviderCredentialsFile: envOrDefault("PROVIDER_CREDENTIALS_FILE", "config/provider-accounts.example.json"),
	}
	if cfg.CredentialsFile == "" {
		return config{}, errors.New("MERCHANT_CREDENTIALS_FILE must not be empty")
	}
	if cfg.ProviderCredentialsFile == "" {
		return config{}, errors.New("PROVIDER_CREDENTIALS_FILE must not be empty")
	}
	if cfg.BankURL == "" {
		return config{}, errors.New("MOCK_BANK_URL must not be empty")
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return config{}, errors.New("HTTP_ADDR must be host:port")
	}
	if err := validatePort(port); err != nil {
		return config{}, errors.New("HTTP_ADDR port must be between 1 and 65535")
	}
	if cfg.DBTimeout, err = durationFromEnv("DB_TIMEOUT", "2s", 30*time.Second); err != nil {
		return config{}, err
	}
	if cfg.ShutdownTimeout, err = durationFromEnv("SHUTDOWN_TIMEOUT", "5s", time.Minute); err != nil {
		return config{}, err
	}
	if cfg.ProviderTimeout, err = durationFromEnv("PROVIDER_TIMEOUT", "2s", 30*time.Second); err != nil {
		return config{}, err
	}
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() == "" || u.User == nil || u.User.Username() == "" ||
		u.Path == "" || u.Path == "/" || strings.Contains(strings.TrimPrefix(u.Path, "/"), "/") ||
		u.Fragment != "" {
		return config{}, errors.New("DATABASE_URL must be a PostgreSQL TCP URL with user, host, and database")
	}
	if u.Port() != "" {
		if err := validatePort(u.Port()); err != nil {
			return config{}, errors.New("DATABASE_URL port must be between 1 and 65535")
		}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return config{}, errors.New("DATABASE_URL query is invalid")
	}
	// Bound connections opened by database/sql outside the caller's context too.
	seconds := (cfg.DBTimeout + time.Second - 1) / time.Second
	query.Set("connect_timeout", strconv.FormatInt(int64(seconds), 10))
	u.RawQuery = query.Encode()
	cfg.DatabaseURL = u.String()
	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value, exists := os.LookupEnv(name); exists {
		return value
	}
	return fallback
}

func durationFromEnv(name, fallback string, maximum time.Duration) (time.Duration, error) {
	value, err := time.ParseDuration(envOrDefault(name, fallback))
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("%s must be a positive duration no greater than %s", name, maximum)
	}
	return value, nil
}

func validatePort(value string) error {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid port")
	}
	return nil
}
