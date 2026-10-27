package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/lib/pq"
	"transfer-101/internal/health"
	"transfer-101/internal/mockbank"
)

type config struct {
	Address         string
	DatabaseURL     string
	CredentialsFile string
	Options         mockbank.Options
}

func loadConfig() (config, error) {
	c := config{Address: env("MOCK_BANK_HTTP_ADDR", "127.0.0.1:8081"), DatabaseURL: os.Getenv("MOCK_BANK_DATABASE_URL"),
		CredentialsFile: env("MOCK_BANK_CREDENTIALS_FILE", "config/provider-accounts.example.json"),
		Options:         mockbank.Options{Scenario: env("MOCK_BANK_SCENARIO", "success"), DBTimeout: 2 * time.Second}}
	host, port, err := net.SplitHostPort(c.Address)
	n, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || n < 1 || n > 65535 || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return config{}, errors.New("MOCK_BANK_HTTP_ADDR must be loopback host:port")
	}
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() != "mock_bank" || u.Path != "/mock_bank" || u.Fragment != "" {
		return config{}, errors.New("MOCK_BANK_DATABASE_URL must target database mock_bank as role mock_bank")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return config{}, errors.New("invalid mock bank database options")
	}
	for key := range q {
		if key != "sslmode" && key != "connect_timeout" {
			return config{}, errors.New("mock bank database URL only allows sslmode and connect_timeout")
		}
	}
	q.Set("connect_timeout", "3")
	u.RawQuery = q.Encode()
	c.DatabaseURL = u.String()
	c.Options.ResponseDelay, err = time.ParseDuration(env("MOCK_BANK_RESPONSE_DELAY", "5s"))
	if err != nil {
		return config{}, errors.New("invalid MOCK_BANK_RESPONSE_DELAY")
	}
	c.Options.InquiryNotFound, err = strconv.ParseBool(env("MOCK_BANK_INQUIRY_NOT_FOUND", "false"))
	if err != nil {
		return config{}, errors.New("invalid MOCK_BANK_INQUIRY_NOT_FOUND")
	}
	if c.CredentialsFile == "" {
		return config{}, errors.New("mock bank credential path is empty")
	}
	if err := mockbank.ValidateOptions(c.Options); err != nil {
		return config{}, err
	}
	return c, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid mock bank configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("mock bank failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	credentials, err := mockbank.LoadCredentials(cfg.CredentialsFile)
	if err != nil {
		return err
	}
	connector, err := pq.NewConnector(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid mock bank PostgreSQL options")
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	checkCtx, cancel := context.WithTimeout(ctx, cfg.Options.DBTimeout)
	err = db.PingContext(checkCtx)
	if err == nil {
		err = mockbank.CheckSchema(checkCtx, db)
	}
	cancel()
	if err != nil {
		return errors.New("mock bank database/schema startup check failed; apply mock bank migrations first")
	}
	bank, err := mockbank.NewHandler(db, credentials, cfg.Options)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/", bank)
	healthHandler := health.NewHandler(db.PingContext, cfg.Options.DBTimeout)
	mux.Handle("GET /livez", healthHandler)
	mux.Handle("GET /readyz", healthHandler)
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return errors.New("cannot listen for mock bank HTTP")
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: cfg.Options.ResponseDelay + 5*time.Second, IdleTimeout: 60 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError)}
	defer server.Close()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	logger.Info("mock bank listening", "address", listener.Addr().String(), "scenario", cfg.Options.Scenario)
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("mock bank HTTP serve failed: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	err = server.Shutdown(shutdownCtx)
	<-result
	if err != nil {
		return errors.New("mock bank shutdown deadline exceeded")
	}
	logger.Info("mock bank stopped")
	return nil
}
