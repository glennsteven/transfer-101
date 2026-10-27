//go:build integration

package transfer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transfer-101/internal/mockbank"
	"transfer-101/internal/testdb"
)

func mockHandler(t *testing.T, db *sql.DB, scenario string) http.Handler {
	t.Helper()
	h, err := mockbank.NewHandler(db, []mockbank.Credential{{AccountID: "bank_a", Token: "bank-a-secret-token"}}, mockbank.Options{Scenario: scenario, DBTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func networkProvider(t *testing.T, h http.Handler) *ProviderClient {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := NewProviderClient(s.URL, []ProviderCredential{{"bank_a", "bank-a-secret-token"}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func dispatchHandler(f postgresFixture, c *ProviderClient) http.Handler {
	mux := http.NewServeMux()
	Register(mux, f.db, testCredentials(), 5*time.Second, testLogger())
	RegisterDispatch(mux, f.db, testCredentials(), c, 5*time.Second, testLogger())
	return mux
}

func newDispatchTransfer(t *testing.T, f postgresFixture) Transfer {
	t.Helper()
	return decodeTransfer(t, perform(f.handler(), "POST", "/transfers", testCredentials()[0].Token, payload("dispatch", 100, "001", "0012345")), 201)
}

func bankInquiry(t *testing.T, c *ProviderClient, reference string) providerOperation {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), "GET", c.baseURL+"/v1/transfers/"+reference, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer bank-a-secret-token")
	response, err := c.http.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var o providerOperation
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(&o) != nil {
		t.Fatal("bank inquiry failed")
	}
	return o
}

func TestPostgresDispatchOutcomes(t *testing.T) {
	for _, tc := range []struct {
		scenario, app, bank, outcome string
		http                         int
	}{{"success", "SUCCESS", "SUCCEEDED", "FINAL_SUCCESS", 200}, {"reject", "FAILED", "REJECTED", "FINAL_REJECTION", 200}, {"accepted", "PROCESSING", "ACCEPTED", "AMBIGUOUS", 200}, {"success_504", "PROCESSING", "SUCCEEDED", "AMBIGUOUS", 504}, {"accepted_500", "PROCESSING", "ACCEPTED", "AMBIGUOUS", 500}} {
		t.Run(tc.scenario, func(t *testing.T) {
			f := postgres(t, true)
			bankDB, _ := testdb.MockBank(t)
			c := networkProvider(t, mockHandler(t, bankDB, tc.scenario))
			h := dispatchHandler(f, c)
			created := newDispatchTransfer(t, f)
			got := decodeTransfer(t, perform(h, "POST", "/transfers/"+created.ID+"/dispatch", testCredentials()[0].Token, ""), 200)
			if got.Status != tc.app || got.ID != created.ID || got.ProviderReference != created.ProviderReference {
				t.Fatalf("dispatch replaced reference or mapped status incorrectly: %s", got.Status)
			}
			if o := bankInquiry(t, c, created.ProviderReference); o.Status != tc.bank {
				t.Fatalf("bank=%s wanted=%s", o.Status, tc.bank)
			}
			var outcome string
			var httpStatus int
			var proof bool
			if err := f.db.QueryRowContext(t.Context(), "SELECT outcome,http_status,evidence IS NOT NULL FROM transfer_attempts WHERE transfer_id=$1", created.ID).Scan(&outcome, &httpStatus, &proof); err != nil || outcome != tc.outcome || httpStatus != tc.http || ((tc.app == "SUCCESS" || tc.app == "FAILED") && !proof) {
				t.Fatalf("attempt evidence missing: outcome=%s http=%d proof=%v error=%v", outcome, httpStatus, proof, err)
			}
			duplicate := decodeTransfer(t, perform(h, "POST", "/transfers", testCredentials()[0].Token, payload("dispatch", 100, "001", "0012345")), 200)
			again := decodeTransfer(t, perform(h, "POST", "/transfers/"+created.ID+"/dispatch", testCredentials()[0].Token, ""), 200)
			if duplicate.ProviderReference != created.ProviderReference || again.Status != got.Status || rowCount(t, f.db) != 1 {
				t.Fatal("duplicate changed original transaction")
			}
			var operations, attempts int
			if err := bankDB.QueryRowContext(t.Context(), "SELECT count(*) FROM bank_operations").Scan(&operations); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRowContext(t.Context(), "SELECT count(*) FROM transfer_attempts").Scan(&attempts); err != nil || operations != 1 || attempts != 1 {
				t.Fatalf("operations=%d attempts=%d error=%v", operations, attempts, err)
			}
			t.Logf("bank=%s submit HTTP=%d app=%s; operations=1 attempts=1 reference retained", tc.bank, tc.http, tc.app)
		})
	}
}

func TestPostgresStructuredTransferLogsAreUsefulAndRedacted(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "success"))
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	mux := http.NewServeMux()
	Register(mux, f.db, testCredentials(), 5*time.Second, logger)
	w := perform(mux, http.MethodPost, "/transfers", testCredentials()[0].Token, payload("log-fields", 100, "001", "001234567890"))
	created := decodeTransfer(t, w, http.StatusCreated)
	if _, err := dispatchWithLogger(t.Context(), f.db, c, "merchant_a", created.ID, 5*time.Second, logger); err != nil {
		t.Fatal(err)
	}
	if output.String() == "" || strings.Contains(output.String(), "001234567890") || strings.Contains(output.String(), "bank-a-secret-token") || strings.Contains(output.String(), "demo-merchant-a-token") {
		t.Fatalf("log absent or contains sensitive value: %s", output.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected create and provider attempt records, got %d: %s", len(lines), output.String())
	}
	wantedOperations := []string{"create", "SUBMIT"}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"transaction_id", "provider_reference", "operation", "attempt", "duration_ms", "outcome"} {
			if _, ok := record[key]; !ok {
				t.Fatalf("missing %s in log: %s", key, line)
			}
		}
		if record["transaction_id"] != created.ID || record["provider_reference"] != created.ProviderReference || record["operation"] != wantedOperations[i] {
			t.Fatalf("unexpected diagnostic identifiers: %s", line)
		}
	}
}

func TestPostgresDispatchClaimCommittedAndUnlocked(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	bank := mockHandler(t, bankDB, "success")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	c := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			bank.ServeHTTP(w, r)
			return
		}
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			bank.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	}))
	h := dispatchHandler(f, c)
	created := newDispatchTransfer(t, f)
	winner := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		winner <- perform(h, "POST", "/transfers/"+created.ID+"/dispatch", testCredentials()[0].Token, "")
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("bank call not reached")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var status, reference string
	if err := tx.QueryRowContext(ctx, "SELECT status,provider_reference FROM transfers WHERE id=$1 FOR UPDATE NOWAIT", created.ID).Scan(&status, &reference); err != nil {
		tx.Rollback()
		t.Fatalf("HTTP call retained database lock: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := f.db.QueryRowContext(ctx, "SELECT count(*) FROM transfer_attempts WHERE transfer_id=$1 AND outcome='STARTED'", created.ID).Scan(&attempts); err != nil || attempts != 1 || status != "PROCESSING" || reference != created.ProviderReference {
		t.Fatalf("claim/reference/attempt not committed before call: %v", err)
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 24)
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			<-start
			results <- perform(h, "POST", "/transfers/"+created.ID+"/dispatch", testCredentials()[0].Token, "")
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for w := range results {
		if got := decodeTransfer(t, w, 200); got.Status != "PROCESSING" {
			t.Fatalf("losing claim status=%s", got.Status)
		}
	}
	if w := perform(h, "POST", "/transfers/"+created.ID+"/dispatch", testCredentials()[1].Token, ""); w.Code != 404 {
		t.Fatal("dispatch leaked another merchant")
	}
	if w := perform(h, "POST", "/transfers/"+created.ID+"/dispatch", "", ""); w.Code != 401 {
		t.Fatal("unauthenticated dispatch accepted")
	}
	if got := decodeTransfer(t, perform(h, "POST", "/transfers", testCredentials()[0].Token, payload("dispatch", 100, "001", "0012345")), 200); got.ProviderReference != created.ProviderReference {
		t.Fatal("duplicate created new reference")
	}
	once.Do(func() { close(release) })
	if got := decodeTransfer(t, <-winner, 200); got.Status != "SUCCESS" || calls.Load() != 1 {
		t.Fatalf("winner=%s calls=%d", got.Status, calls.Load())
	}
	t.Log("claim+attempt committed before call; FOR UPDATE NOWAIT succeeds during HTTP; 24 competing dispatches cause no extra HTTP")
}

func TestPostgresDispatchLateEvidenceCannotOverwriteFinal(t *testing.T) {
	for _, tc := range []struct{ bank, final, want string }{{"reject", "SUCCESS", "FINAL_CONFLICT"}, {"success", "FAILED", "FINAL_CONFLICT"}, {"accepted", "SUCCESS", "AMBIGUOUS"}} {
		t.Run(tc.bank+"_after_"+tc.final, func(t *testing.T) {
			f := postgres(t, true)
			bankDB, _ := testdb.MockBank(t)
			bank := mockHandler(t, bankDB, tc.bank)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			c := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					bank.ServeHTTP(w, r)
					return
				}
				close(entered)
				select {
				case <-release:
					bank.ServeHTTP(w, r)
				case <-r.Context().Done():
				}
			}))
			h := dispatchHandler(f, c)
			created := newDispatchTransfer(t, f)
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				response <- perform(h, "POST", "/transfers/"+created.ID+"/dispatch", testCredentials()[0].Token, "")
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("dispatch not entered")
			}
			setTestStatus(t, f.db, created.ID, tc.final)
			once.Do(func() { close(release) })
			if got := decodeTransfer(t, <-response, 200); got.Status != tc.final {
				t.Fatal("late evidence overwrote final status")
			}
			var outcome string
			if err := f.db.QueryRowContext(t.Context(), "SELECT outcome FROM transfer_attempts WHERE transfer_id=$1", created.ID).Scan(&outcome); err != nil || outcome != tc.want {
				t.Fatalf("conflict not recorded for review: %s %v", outcome, err)
			}
		})
	}
}

func TestPostgresDispatchTimeoutAndCancellationAfterBankCommit(t *testing.T) {
	for _, fault := range []string{"timeout", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			f := postgres(t, true)
			bankDB, _ := testdb.MockBank(t)
			c := networkProvider(t, mockHandler(t, bankDB, "success"))
			transport := c.http.Transport
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(r)
				if r.Method == http.MethodGet {
					return response, err
				}
				if err != nil {
					return nil, err
				}
				_, err = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if err != nil {
					return nil, err
				}
				if fault == "timeout" {
					return nil, context.DeadlineExceeded
				}
				cancel()
				return nil, context.Canceled
			})
			created := newDispatchTransfer(t, f)
			got, err := dispatch(ctx, f.db, c, "merchant_a", created.ID, 5*time.Second)
			if err != nil || got.Status != "PROCESSING" || got.ProviderReference != created.ProviderReference {
				t.Fatalf("transport failure finalized/replaced transfer: %v", err)
			}
			var status, reason string
			if err := bankDB.QueryRowContext(t.Context(), "SELECT status FROM bank_operations WHERE provider_reference=$1", created.ProviderReference).Scan(&status); err != nil || status != "SUCCEEDED" {
				t.Fatalf("bank had not committed before fault: status=%s error=%v", status, err)
			}
			want := "HTTP_TIMEOUT"
			if fault == "canceled" {
				want = "HTTP_CANCELED"
			}
			if err := f.db.QueryRowContext(t.Context(), "SELECT reason_code FROM transfer_attempts WHERE transfer_id=$1 AND outcome='AMBIGUOUS'", created.ID).Scan(&reason); err != nil || reason != want {
				t.Fatalf("canceled caller lost durable observation: %s %v", reason, err)
			}
		})
	}
}

func TestPostgresDispatchDatabaseBoundariesAreAtomic(t *testing.T) {
	t.Run("claim_and_attempt", func(t *testing.T) {
		f := postgres(t, true)
		created := newDispatchTransfer(t, f)
		if _, err := f.db.ExecContext(t.Context(), "ALTER TABLE transfer_attempts ADD CONSTRAINT reject_attempt_test CHECK (false)"); err != nil {
			t.Fatal(err)
		}
		c := testProviderClient(t)
		c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("bank called before claim committed")
			return nil, context.Canceled
		})
		if _, err := dispatch(t.Context(), f.db, c, "merchant_a", created.ID, 5*time.Second); err == nil {
			t.Fatal("failed attempt INSERT accepted")
		}
		stored, err := get(t.Context(), f.db, "merchant_a", created.ID)
		if err != nil || stored.Status != "PENDING" {
			t.Fatalf("partial claim committed: status=%s error=%v", stored.Status, err)
		}
	})
	t.Run("final_status_and_evidence", func(t *testing.T) {
		f := postgres(t, true)
		created := newDispatchTransfer(t, f)
		bankDB, _ := testdb.MockBank(t)
		c := networkProvider(t, mockHandler(t, bankDB, "success"))
		if _, err := f.db.ExecContext(t.Context(), "ALTER TABLE transfer_attempts ADD CONSTRAINT reject_evidence_test CHECK (outcome='STARTED')"); err != nil {
			t.Fatal(err)
		}
		if _, err := dispatch(t.Context(), f.db, c, "merchant_a", created.ID, 5*time.Second); err == nil {
			t.Fatal("failed evidence update accepted")
		}
		stored, err := get(t.Context(), f.db, "merchant_a", created.ID)
		if err != nil || stored.Status != "PROCESSING" {
			t.Fatalf("final status committed without evidence: status=%s error=%v", stored.Status, err)
		}
		if o := bankInquiry(t, c, created.ProviderReference); o.Status != "SUCCEEDED" {
			t.Fatal("bank outcome lost")
		}
	})
}
