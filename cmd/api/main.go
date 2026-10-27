package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lib/pq"

	"transfer-101/internal/health"
	"transfer-101/internal/transfer"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	credentials, err := transfer.LoadCredentials(cfg.CredentialsFile)
	if err != nil {
		return err
	}
	providerCredentials, err := transfer.LoadProviderCredentials(cfg.ProviderCredentialsFile)
	if err != nil {
		return err
	}
	provider, err := transfer.NewProviderClient(cfg.BankURL, providerCredentials, cfg.ProviderTimeout)
	if err != nil {
		return err
	}
	defer provider.CloseIdleConnections()
	connector, err := pq.NewConnector(cfg.DatabaseURL)
	if err != nil {
		return errors.New("DATABASE_URL contains unsupported PostgreSQL options")
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.DBTimeout)
	err = db.PingContext(pingCtx)
	if err == nil {
		err = transfer.CheckSchema(pingCtx, db)
	}
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("PostgreSQL startup check failed: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", health.NewHandler(db.PingContext, cfg.DBTimeout))
	transfer.Register(mux, db, credentials, cfg.DBTimeout, logger)
	transfer.RegisterDispatch(mux, db, credentials, provider, cfg.DBTimeout, logger)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      2*cfg.ProviderTimeout + 3*cfg.DBTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	logger.Info("server listening", "address", listener.Addr().String())
	return serve(ctx, server, listener, cfg.ShutdownTimeout, logger)
}

func serve(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration, logger *slog.Logger) error {
	defer server.Close()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutdown started")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	<-result
	if err != nil {
		return fmt.Errorf("shutdown HTTP: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}
