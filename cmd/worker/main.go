package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lib/pq"
	"transfer-101/internal/transfer"
)

func run(ctx context.Context, logger *slog.Logger) error {
	u, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.Path == "" || u.Path == "/" || u.Fragment != "" {
		return errors.New("DATABASE_URL must be a PostgreSQL TCP URL")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("invalid DATABASE_URL options")
	}
	q.Set("connect_timeout", "3")
	u.RawQuery = q.Encode()
	credentials, err := transfer.LoadProviderCredentials(env("PROVIDER_CREDENTIALS_FILE", "config/provider-accounts.example.json"))
	if err != nil {
		return err
	}
	providerTimeout, err := time.ParseDuration(env("PROVIDER_TIMEOUT", "2s"))
	if err != nil {
		return errors.New("invalid PROVIDER_TIMEOUT")
	}
	client, err := transfer.NewProviderClient(env("MOCK_BANK_URL", "http://127.0.0.1:8081"), credentials, providerTimeout)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	opts := transfer.DefaultRecoveryOptions()
	for _, setting := range []struct {
		name  string
		value *time.Duration
	}{{"DB_TIMEOUT", &opts.DBTimeout}, {"RECOVERY_LEASE", &opts.Lease}, {"RECOVERY_POLL", &opts.Poll}, {"RECOVERY_BASE_BACKOFF", &opts.BaseBackoff}, {"RECOVERY_MAX_BACKOFF", &opts.MaxBackoff}} {
		*setting.value, err = time.ParseDuration(env(setting.name, setting.value.String()))
		if err != nil {
			return errors.New("invalid " + setting.name)
		}
	}
	connector, err := pq.NewConnector(u.String())
	if err != nil {
		return errors.New("invalid PostgreSQL options")
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	worker, err := transfer.NewRecoveryWorker(db, client, opts)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, opts.DBTimeout)
	err = db.PingContext(checkCtx)
	if err == nil {
		err = transfer.CheckSchema(checkCtx, db)
	}
	cancel()
	if err != nil {
		return errors.New("worker database/schema startup check failed; apply transfer migrations first")
	}
	logger.Info("recovery worker started")
	err = worker.Run(ctx, logger)
	logger.Info("recovery worker stopped")
	return err
}

func env(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("recovery worker failed", "error", err)
		os.Exit(1)
	}
}
