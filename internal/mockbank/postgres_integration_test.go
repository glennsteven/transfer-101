//go:build integration

package mockbank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"transfer-101/internal/testdb"
)

func bankRequest(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func bankPayload(ref string, amount int64, bank, number string) string {
	return fmt.Sprintf(`{"provider_reference":%q,"amount":%d,"currency":"IDR","bank_code":%q,"account_number":%q}`, ref, amount, bank, number)
}

func decodeOperation(t *testing.T, w *httptest.ResponseRecorder) Operation {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("mock HTTP %d: %s", w.Code, w.Body)
	}
	var o Operation
	if json.Unmarshal(w.Body.Bytes(), &o) != nil {
		t.Fatal("invalid bank response")
	}
	return o
}

func TestPostgresProviderConcurrentIdempotencyAndScope(t *testing.T) {
	db, _ := testdb.MockBank(t)
	h, err := NewHandler(db, testCredentials(), Options{Scenario: "success", DBTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 24)
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			<-start
			results <- bankRequest(h, "POST", "/v1/transfers", "bank-a-secret-token", bankPayload("prv_same", 100, "001", "0012345"))
		})
	}
	close(start)
	wg.Wait()
	close(results)
	id := ""
	for w := range results {
		o := decodeOperation(t, w)
		if id == "" {
			id = o.OperationID
		}
		if o.OperationID != id || o.Status != "SUCCEEDED" {
			t.Fatal("duplicate produced another operation")
		}
	}
	for _, p := range []string{bankPayload("prv_same", 101, "001", "0012345"), bankPayload("prv_same", 100, "002", "0012345"), bankPayload("prv_same", 100, "001", "9912345")} {
		if w := bankRequest(h, "POST", "/v1/transfers", "bank-a-secret-token", p); w.Code != 409 {
			t.Fatalf("payload conflict got %d", w.Code)
		}
	}
	if w := bankRequest(h, "GET", "/v1/transfers/prv_same", "bank-b-secret-token", ""); w.Code != 404 {
		t.Fatal("inquiry leaked other account")
	}
	other := decodeOperation(t, bankRequest(h, "POST", "/v1/transfers", "bank-b-secret-token", bankPayload("prv_same", 100, "001", "0012345")))
	if other.OperationID == id || other.AccountID != "bank_b" {
		t.Fatal("account scope not isolated")
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM bank_operations WHERE provider_account_id='bank_a' AND status='SUCCEEDED'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("effective operations=%d error=%v", count, err)
	}
	t.Log("24 concurrent provider submits: one effective bank operation; amount/bank/account conflicts rejected")
}

func TestPostgresMockBankScenariosAndInquiry(t *testing.T) {
	for _, tc := range []struct {
		scenario, status string
		code             int
	}{{"success", "SUCCEEDED", 200}, {"reject", "REJECTED", 200}, {"accepted", "ACCEPTED", 200}, {"success_504", "SUCCEEDED", 504}, {"accepted_500", "ACCEPTED", 500}} {
		t.Run(tc.scenario, func(t *testing.T) {
			db, _ := testdb.MockBank(t)
			h, err := NewHandler(db, testCredentials(), Options{Scenario: tc.scenario, DBTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			w := bankRequest(h, "POST", "/v1/transfers", "bank-a-secret-token", bankPayload("prv_scenario", 100, "001", "0012345"))
			if w.Code != tc.code {
				t.Fatalf("HTTP=%d wanted=%d", w.Code, tc.code)
			}
			o := decodeOperation(t, bankRequest(h, "GET", "/v1/transfers/prv_scenario", "bank-a-secret-token", ""))
			if o.Status != tc.status {
				t.Fatalf("inquiry status=%s wanted=%s", o.Status, tc.status)
			}
			duplicate := decodeOperation(t, bankRequest(h, "POST", "/v1/transfers", "bank-a-secret-token", bankPayload("prv_scenario", 100, "001", "0012345")))
			if duplicate != o {
				t.Fatal("duplicate did not return durable operation")
			}
			if w := bankRequest(h, "GET", "/v1/transfers/prv_missing", "bank-a-secret-token", ""); w.Code != 404 {
				t.Fatal("missing inquiry should be 404")
			}
		})
	}
}

func TestPostgresDelayedMockStopsOnCancellationAfterCommit(t *testing.T) {
	db, _ := testdb.MockBank(t)
	committed, finished := make(chan struct{}), make(chan struct{})
	h, err := NewHandler(db, testCredentials(), Options{Scenario: "success_timeout", DBTimeout: 5 * time.Second, ResponseDelay: time.Minute, afterCommit: func() { close(committed) }})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(finished); h.ServeHTTP(w, r) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/transfers", strings.NewReader(bankPayload("prv_delayed", 100, "001", "0012345")))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer bank-a-secret-token")
	r.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	result := make(chan error, 1)
	go func() {
		response, err := client.Do(r)
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	select {
	case <-committed:
	case <-time.After(10 * time.Second):
		t.Fatal("bank commit deadline exceeded")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("client did not observe cancellation: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("mock ignored cancellation while delaying response")
	}
	o := decodeOperation(t, bankRequest(h, "GET", "/v1/transfers/prv_delayed", "bank-a-secret-token", ""))
	if o.Status != "SUCCEEDED" {
		t.Fatal("cancellation lost committed outcome")
	}
	if duplicate := decodeOperation(t, bankRequest(h, "POST", "/v1/transfers", "bank-a-secret-token", bankPayload("prv_delayed", 100, "001", "0012345"))); duplicate.OperationID != o.OperationID {
		t.Fatal("duplicate re-executed delayed operation")
	}
}

func TestPostgresMockBankRetentionGuards(t *testing.T) {
	db, _ := testdb.MockBank(t)
	h, err := NewHandler(db, testCredentials(), Options{Scenario: "success", DBTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	missing := bankRequest(h, "GET", "/v1/transfers/prv_guard", "bank-a-secret-token", "")
	epoch := missing.Header().Get("X-Mock-Retention-Epoch")
	if epoch == "" {
		t.Fatal("inquiry has no durable retention epoch")
	}
	var before time.Time
	if err := db.QueryRowContext(t.Context(), "SELECT clock_timestamp()+interval '1 minute'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	post := func(ref, epoch string, before time.Time, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/transfers", strings.NewReader(bankPayload(ref, 100, "001", "0012345")))
		r.Header.Set("Authorization", "Bearer bank-a-secret-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Mock-Retention-Epoch", epoch)
		r.Header.Set("X-Mock-Resubmit-Before", before.Format(time.RFC3339Nano))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("retention guard HTTP=%d wanted=%d", w.Code, want)
		}
	}
	post("prv_guard", epoch, before, 200)
	if _, err := db.ExecContext(t.Context(), "UPDATE bank_retention SET epoch=gen_random_uuid()::text"); err != nil {
		t.Fatal(err)
	}
	post("prv_guard_late", epoch, before, 412)
	var current string
	if err := db.QueryRowContext(t.Context(), "SELECT epoch FROM bank_retention").Scan(&current); err != nil {
		t.Fatal(err)
	}
	post("prv_expired", current, before.Add(-time.Hour), 412)
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM bank_operations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("guard allowed extra bank operation: %d %v", count, err)
	}
}
