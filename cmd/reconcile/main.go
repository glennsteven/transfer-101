package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"transfer-101/internal/transfer"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func run(args []string) error {
	if len(args) < 3 || ((args[0] == "export" || args[0] == "import") && len(args) != 4) || (args[0] == "show" && len(args) != 3) || (args[0] != "export" && args[0] != "import" && args[0] != "show") {
		return errors.New("usage: reconcile export|import ACCOUNT REPORT_ID FILE.csv | reconcile show ACCOUNT IMPORT_ID")
	}
	credentials, err := transfer.LoadProviderCredentials(env("PROVIDER_CREDENTIALS_FILE", "config/provider-accounts.example.json"))
	if err != nil {
		return err
	}
	allowed := false
	for _, c := range credentials {
		if c.AccountID == args[1] {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("provider account not configured")
	}
	timeout, err := time.ParseDuration(env("PROVIDER_TIMEOUT", "2s"))
	if err != nil {
		return errors.New("invalid PROVIDER_TIMEOUT")
	}
	client, err := transfer.NewProviderClient(env("MOCK_BANK_URL", "http://127.0.0.1:8081"), credentials, timeout)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if args[0] == "export" {
		data, err := client.FetchReport(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		f, err := os.OpenFile(args[3], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(args[3])
			if readErr != nil || !bytes.Equal(existing, data) {
				return errors.New("output exists with different content; not overwritten")
			}
			fmt.Println("existing identical report retained")
			return nil
		}
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Println("report exported:", args[2])
		return nil
	}
	db, err := transfer.OpenOperatorDatabase(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	var result transfer.ReconciliationResult
	if args[0] == "show" {
		id, parseErr := strconv.ParseInt(args[2], 10, 64)
		if parseErr != nil || id <= 0 {
			return errors.New("invalid import id")
		}
		result, err = transfer.ReadReconciliation(ctx, db, args[1], id)
	} else {
		f, openErr := os.Open(args[3])
		if openErr != nil {
			return openErr
		}
		data, readErr := io.ReadAll(io.LimitReader(f, transfer.MaxReportBytes+1))
		f.Close()
		if readErr != nil || len(data) > transfer.MaxReportBytes {
			return errors.New("CSV unreadable or exceeds 8 MiB")
		}
		result, err = transfer.ReconcileReport(ctx, db, client, args[1], args[2], data)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
