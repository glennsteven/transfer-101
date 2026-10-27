//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	"transfer-101/internal/mockbank"
	"transfer-101/internal/testdb"
	"transfer-101/internal/transfer"
)

type listeningLog struct{ address chan string }

func (l listeningLog) Write(data []byte) (int, error) {
	var record struct {
		Msg     string `json:"msg"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return 0, err
	}
	if record.Msg == "server listening" {
		l.address <- record.Address
	}
	return len(data), nil
}

func TestPostgresAPIStartupAndRoutes(t *testing.T) {
	bankDB, _ := testdb.MockBank(t)
	bank, err := mockbank.NewHandler(bankDB, []mockbank.Credential{{AccountID: "bank_a", Token: "bank-a-secret-token"}}, mockbank.Options{Scenario: "success_504", DBTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	bankServer := httptest.NewServer(bank)
	defer bankServer.Close()
	admin, dsn := testdb.Open(t)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	setupCtx, setupCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer setupCancel()
	schema := "transfer_api_test_" + strings.ToLower(rand.Text())
	if _, err := admin.ExecContext(setupCtx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE"); err != nil {
			t.Errorf("cleanup owned API schema: %v", err)
		}
	}()
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("connect_timeout", "2")
	u.RawQuery = query.Encode()
	if err := testdb.Migrate(t, u.String(), "migrate"); err != nil {
		t.Fatal(err)
	}
	fixtureFunds, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ id, source string }{{"merchant_a", "funds_a"}, {"merchant_b", "funds_b"}} {
		if _, err = transfer.InitializeFunds(t.Context(), fixtureFunds, m.id, m.source, 10000000); err != nil {
			t.Fatal(err)
		}
	}
	if err = fixtureFunds.Close(); err != nil {
		t.Fatal(err)
	}
	credentialsFile := filepath.Join(t.TempDir(), "merchants.json")
	data := `[{"token":"demo-merchant-a-token","merchant":{"id":"merchant_a","source_account_id":"funds_a","provider_account_id":"bank_a"}},
		{"token":"demo-merchant-b-token","merchant":{"id":"merchant_b","source_account_id":"funds_b","provider_account_id":"bank_a"}}]`
	if err := os.WriteFile(credentialsFile, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	providerFile := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(providerFile, []byte(`[{"provider_account_id":"bank_a","token":"bank-a-secret-token"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	address := make(chan string, 1)
	logger := slog.New(slog.NewJSONHandler(listeningLog{address: address}, nil))
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, config{HTTPAddr: "127.0.0.1:0", DatabaseURL: u.String(), CredentialsFile: credentialsFile, DBTimeout: 2 * time.Second, ShutdownTimeout: 2 * time.Second,
			BankURL: bankServer.URL, ProviderTimeout: time.Second, ProviderCredentialsFile: providerFile}, logger)
	}()
	defer func() {
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("API shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("API shutdown did not complete")
		}
	}()
	var baseURL string
	select {
	case value := <-address:
		baseURL = "http://" + value
	case <-time.After(5 * time.Second):
		t.Fatal("API did not start listening")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	call := func(method, path, token, body string, want int) []byte {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s: HTTP %d want %d, body=%s error=%v", method, path, response.StatusCode, want, data, err)
		}
		return data
	}
	call(http.MethodGet, "/livez", "", "", http.StatusOK)
	call(http.MethodGet, "/readyz", "", "", http.StatusOK)
	body := `{"reference_id":"runtime","amount":100,"bank_code":"001","account_number":"0012345"}`
	created := call(http.MethodPost, "/transfers", "demo-merchant-a-token", body, http.StatusCreated)
	var stored transfer.Transfer
	if err := json.Unmarshal(created, &stored); err != nil || stored.Status != "PENDING" {
		t.Fatal("invalid runtime transfer response")
	}
	duplicate := call(http.MethodPost, "/transfers", "demo-merchant-a-token", body, http.StatusOK)
	if string(created) != string(duplicate) {
		t.Fatal("runtime duplicate did not return existing resource")
	}
	call(http.MethodGet, "/transfers/"+stored.ID, "demo-merchant-a-token", "", http.StatusOK)
	call(http.MethodGet, "/transfers/"+stored.ID, "demo-merchant-b-token", "", http.StatusNotFound)
	call(http.MethodGet, "/transfers/"+stored.ID, "", "", http.StatusUnauthorized)
	call(http.MethodPost, "/transfers", "demo-merchant-a-token", strings.Replace(body, `"amount":100`, `"amount":101`, 1), http.StatusConflict)
	call(http.MethodPost, "/transfers/"+stored.ID+"/dispatch", "demo-merchant-b-token", "", http.StatusNotFound)
	call(http.MethodPost, "/transfers/"+stored.ID+"/dispatch", "", "", http.StatusUnauthorized)
	dispatched := call(http.MethodPost, "/transfers/"+stored.ID+"/dispatch", "demo-merchant-a-token", "", http.StatusOK)
	var after transfer.Transfer
	if err := json.Unmarshal(dispatched, &after); err != nil || after.Status != "PROCESSING" || after.ProviderReference != stored.ProviderReference {
		t.Fatal("runtime 504 incorrectly finalized transfer")
	}
	var bankStatus string
	if err := bankDB.QueryRowContext(t.Context(), "SELECT status FROM bank_operations WHERE provider_reference=$1", stored.ProviderReference).Scan(&bankStatus); err != nil || bankStatus != "SUCCEEDED" {
		t.Fatalf("runtime mock did not commit success: %v", err)
	}
	call(http.MethodPost, "/transfers/"+stored.ID+"/dispatch", "demo-merchant-a-token", "", http.StatusOK)
	var demo *exec.Cmd
	if binary := os.Getenv("PROVIDER_DEMO_BIN"); binary != "" {
		demo = exec.CommandContext(t.Context(), binary, "runtime-cli", "PROCESSING", "SUCCEEDED")
	} else {
		demo = exec.CommandContext(t.Context(), "go", "run", "./cmd/demo-provider", "runtime-cli", "PROCESSING", "SUCCEEDED")
		_, source, _, _ := runtime.Caller(0)
		demo.Dir = filepath.Join(filepath.Dir(source), "..", "..")
	}
	demo.Env = append(os.Environ(), "DEMO_API_URL="+baseURL, "DEMO_BANK_URL="+bankServer.URL, "DEMO_BANK_TOKEN=bank-a-secret-token", "DEMO_MERCHANT_TOKEN=demo-merchant-a-token")
	output, err := demo.CombinedOutput()
	if err != nil {
		t.Fatalf("demo CLI failed: %v\n%s", err, output)
	}
	t.Logf("demo CLI executed against real API + mock bank: %s", output)
	workerDB, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	providerCredentials, err := transfer.LoadProviderCredentials(providerFile)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := transfer.NewProviderClient(bankServer.URL, providerCredentials, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.CloseIdleConnections()
	options := transfer.DefaultRecoveryOptions()
	options.Poll = 10 * time.Millisecond
	worker, err := transfer.NewRecoveryWorker(workerDB, provider, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workerDB.ExecContext(t.Context(), "UPDATE transfers SET next_attempt_at=clock_timestamp() WHERE status='PROCESSING'"); err != nil {
		t.Fatal(err)
	}
	workerCtx, workerCancel := context.WithCancel(t.Context())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx, logger) }()
	defer func() {
		workerCancel()
		select {
		case err := <-workerDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker shutdown deadline")
		}
	}()
	var recoveryDemo *exec.Cmd
	if binary := os.Getenv("PROVIDER_DEMO_BIN"); binary != "" {
		recoveryDemo = exec.CommandContext(t.Context(), binary, "runtime-cli", "ANY", "SUCCEEDED", "SUCCESS")
	} else {
		recoveryDemo = exec.CommandContext(t.Context(), "go", "run", "./cmd/demo-provider", "runtime-cli", "ANY", "SUCCEEDED", "SUCCESS")
		recoveryDemo.Dir = demo.Dir
	}
	recoveryDemo.Env = demo.Env
	output, err = recoveryDemo.CombinedOutput()
	if err != nil {
		t.Fatalf("recovery CLI failed: %v\n%s", err, output)
	}
	t.Logf("recovery CLI with live worker: %s", output)
	t.Log("real HTTP API: startup, probes, create/duplicate/conflict, authenticated GET/dispatch, isolation, bank SUCCEEDED+504 -> PROCESSING, and shutdown verified")
}
