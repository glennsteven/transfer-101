//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"transfer-101/internal/mockbank"
	"transfer-101/internal/testdb"
)

func TestMockBankChildProcess(t *testing.T) {
	if os.Getenv("TRANSFER_TEST_BANK_PROCESS") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := config{Address: "127.0.0.1:0", DatabaseURL: os.Getenv("TRANSFER_TEST_BANK_DSN"), CredentialsFile: os.Getenv("TRANSFER_TEST_BANK_CREDENTIALS"),
		Options: mockbank.Options{Scenario: os.Getenv("TRANSFER_TEST_BANK_SCENARIO"), DBTimeout: 2 * time.Second}}
	if err := run(ctx, c, slog.New(slog.NewJSONHandler(os.Stdout, nil))); err != nil {
		t.Fatal(err)
	}
}

func startBankProcess(t *testing.T, dsn, credentials, scenario string) (string, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMockBankChildProcess$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "TRANSFER_TEST_BANK_PROCESS=1", "TRANSFER_TEST_BANK_DSN="+dsn, "TRANSFER_TEST_BANK_CREDENTIALS="+credentials, "TRANSFER_TEST_BANK_SCENARIO="+scenario)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			var record struct{ Msg, Address string }
			if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Msg == "mock bank listening" {
				ready <- record.Address
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("bank process stopped with error: %v", err)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("mock bank process did not stop")
			}
			<-scanned
		})
	}
	t.Cleanup(stop)
	select {
	case address := <-ready:
		return "http://" + address, stop
	case err := <-done:
		once.Do(func() {})
		t.Fatalf("bank process failed to start: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("bank process startup deadline exceeded")
	}
	return "", stop
}

func TestPostgresMockBankSurvivesProcessRestart(t *testing.T) {
	db, dsn := testdb.MockBank(t)
	credentials := filepath.Join(t.TempDir(), "bank.json")
	if err := os.WriteFile(credentials, []byte(`[{"provider_account_id":"bank_a","token":"bank-a-secret-token"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	request := func(base, method, path, body string, want int) map[string]any {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer bank-a-secret-token")
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var value map[string]any
		if response.StatusCode != want || json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&value) != nil {
			t.Fatalf("mock HTTP status=%d wanted=%d", response.StatusCode, want)
		}
		return value
	}
	payload := `{"provider_reference":"prv_restart","amount":100,"currency":"IDR","bank_code":"001","account_number":"0012345"}`
	first, stopFirst := startBankProcess(t, dsn, credentials, "success_504")
	request(first, "POST", "/v1/transfers", payload, 504)
	before := request(first, "GET", "/v1/transfers/prv_restart", "", 200)
	if before["status"] != "SUCCEEDED" {
		t.Fatal("bank did not commit success before 504")
	}
	stopFirst()
	second, stopSecond := startBankProcess(t, dsn, credentials, "reject")
	after := request(second, "GET", "/v1/transfers/prv_restart", "", 200)
	duplicate := request(second, "POST", "/v1/transfers", payload, 200)
	if before["operation_id"] != after["operation_id"] || before["operation_id"] != duplicate["operation_id"] || duplicate["status"] != "SUCCEEDED" {
		t.Fatal("restart lost idempotency or durable result")
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM bank_operations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("effective operations=%d error=%v", count, err)
	}
	stopSecond()
	t.Log("two distinct OS processes: same durable bank operation and SUCCEEDED outcome after restart; duplicate did not execute again")
}
