package main

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const demoURL = "postgres://demo:demo@127.0.0.1:54329/demo?sslmode=disable"

func configEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", demoURL)
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("DB_TIMEOUT", "2s")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("MERCHANT_CREDENTIALS_FILE", "config/merchants.example.json")
	t.Setenv("MOCK_BANK_URL", "http://127.0.0.1:8081")
	t.Setenv("PROVIDER_TIMEOUT", "2s")
	t.Setenv("PROVIDER_CREDENTIALS_FILE", "config/provider-accounts.example.json")
}

func TestLoadConfigDefaults(t *testing.T) {
	configEnv(t)
	for _, name := range []string{"HTTP_ADDR", "DB_TIMEOUT", "SHUTDOWN_TIMEOUT"} {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" || cfg.DBTimeout != 2*time.Second || cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("unexpected defaults: address=%q db=%s shutdown=%s", cfg.HTTPAddr, cfg.DBTimeout, cfg.ShutdownTimeout)
	}
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("connect_timeout") != "2" || u.Query().Get("sslmode") != "disable" {
		t.Fatal("connection deadline or TLS option not preserved")
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"missing database", "DATABASE_URL", ""},
		{"wrong scheme", "DATABASE_URL", "https://demo:secret@localhost/demo"},
		{"missing user", "DATABASE_URL", "postgres://localhost/demo"},
		{"missing host", "DATABASE_URL", "postgres://demo:secret@/demo"},
		{"missing database name", "DATABASE_URL", "postgres://demo:secret@localhost/"},
		{"invalid escape", "DATABASE_URL", "postgres://demo:secret@localhost/%zz"},
		{"invalid query", "DATABASE_URL", "postgres://demo:secret@localhost/demo?x=%zz"},
		{"database port zero", "DATABASE_URL", "postgres://demo:secret@localhost:0/demo"},
		{"database port too large", "DATABASE_URL", "postgres://demo:secret@localhost:65536/demo"},
		{"database fragment", "DATABASE_URL", "postgres://demo:secret@localhost/demo#fragment"},
		{"address missing port", "HTTP_ADDR", "localhost"},
		{"empty address", "HTTP_ADDR", ""},
		{"http port zero", "HTTP_ADDR", "127.0.0.1:0"},
		{"http port too large", "HTTP_ADDR", "127.0.0.1:65536"},
		{"http named port", "HTTP_ADDR", "127.0.0.1:http"},
		{"invalid db timeout", "DB_TIMEOUT", "later"},
		{"empty db timeout", "DB_TIMEOUT", ""},
		{"zero db timeout", "DB_TIMEOUT", "0s"},
		{"negative db timeout", "DB_TIMEOUT", "-1s"},
		{"excessive db timeout", "DB_TIMEOUT", "31s"},
		{"zero shutdown", "SHUTDOWN_TIMEOUT", "0s"},
		{"excessive shutdown", "SHUTDOWN_TIMEOUT", "61s"},
		{"empty credentials path", "MERCHANT_CREDENTIALS_FILE", ""},
		{"empty bank URL", "MOCK_BANK_URL", ""},
		{"empty provider credentials", "PROVIDER_CREDENTIALS_FILE", ""},
		{"invalid provider timeout", "PROVIDER_TIMEOUT", "0s"},
		{"excessive provider timeout", "PROVIDER_TIMEOUT", "31s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := loadConfig()
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("configuration error exposed credential")
			}
		})
	}
}

func TestConnectTimeoutCannotBeDisabled(t *testing.T) {
	configEnv(t)
	t.Setenv("DATABASE_URL", demoURL+"&connect_timeout=0")
	t.Setenv("DB_TIMEOUT", "1500ms")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(cfg.DatabaseURL)
	if u.Query().Get("connect_timeout") != "2" {
		t.Fatal("driver timeout must be rounded up and bounded by application configuration")
	}
}
