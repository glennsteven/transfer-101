//go:build integration

package transfer

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transfer-101/internal/mockbank"
	"transfer-101/internal/testdb"
)

func recoveryWorker(t *testing.T, f postgresFixture, c *ProviderClient) *RecoveryWorker {
	t.Helper()
	o := DefaultRecoveryOptions()
	o.DBTimeout = 5 * time.Second
	o.BaseBackoff = time.Hour
	o.MaxBackoff = time.Hour
	o.jitter = func(cap time.Duration) time.Duration { return cap }
	w, err := NewRecoveryWorker(f.db, c, o)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func due(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `UPDATE transfers SET next_attempt_at=clock_timestamp()-interval '1 second',
		lease_until=CASE WHEN lease_token IS NULL THEN NULL ELSE clock_timestamp()-interval '1 second' END WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func runOnce(t *testing.T, w *RecoveryWorker) {
	t.Helper()
	did, err := w.RunOnce(t.Context())
	if err != nil || !did {
		t.Fatalf("no recovery progress: did=%v error=%v", did, err)
	}
}
func assertStatus(t *testing.T, f postgresFixture, id, status string, review bool) Transfer {
	t.Helper()
	got, err := get(t.Context(), f.db, "merchant_a", id)
	if err != nil || got.Status != status || got.NeedsReview != review {
		t.Fatalf("status=%s review=%v error=%v", got.Status, got.NeedsReview, err)
	}
	return got
}
func bankCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM bank_operations").Scan(&n); err != nil || n != want {
		t.Fatalf("bank operations=%d wanted=%d error=%v", n, want, err)
	}
}

func TestPostgresRecovery504AndPersistentSchedule(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "success_504"))
	created := newDispatchTransfer(t, f)
	runOnce(t, recoveryWorker(t, f, c))
	assertStatus(t, f, created.ID, "PROCESSING", false)
	bankCount(t, bankDB, 1)
	var next time.Time
	var count int
	if err := f.db.QueryRowContext(t.Context(), "SELECT next_attempt_at,recovery_attempts FROM transfers WHERE id=$1", created.ID).Scan(&next, &count); err != nil || count != 1 {
		t.Fatal("schedule/attempt not durable")
	}
	restarted := recoveryWorker(t, f, c)
	if did, err := restarted.RunOnce(t.Context()); err != nil || did {
		t.Fatal("restart ignored persisted future backoff")
	}
	var persisted time.Time
	if err := f.db.QueryRowContext(t.Context(), "SELECT next_attempt_at FROM transfers WHERE id=$1", created.ID).Scan(&persisted); err != nil || !persisted.Equal(next) {
		t.Fatal("restart changed schedule")
	}
	due(t, f.db, created.ID)
	runOnce(t, restarted)
	got := assertStatus(t, f, created.ID, "SUCCESS", false)
	if got.ProviderReference != created.ProviderReference {
		t.Fatal("recovery replaced reference")
	}
	bankCount(t, bankDB, 1)
	if did, err := restarted.RunOnce(t.Context()); err != nil || did {
		t.Fatal("final transfer reclaimed")
	}
}

func TestPostgresRecoveryTemporaryNotFoundThenVisibleWithoutResubmit(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	bank := mockHandler(t, bankDB, "success_504")
	var epoch string
	if err := bankDB.QueryRowContext(t.Context(), "SELECT epoch FROM bank_retention WHERE singleton").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	var hidden atomic.Bool
	var posts atomic.Int32
	c := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		if r.Method == http.MethodGet && hidden.Load() {
			w.Header().Set("X-Mock-Provider-Account", "bank_a")
			w.Header().Set("X-Mock-Retention-Epoch", epoch)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND"}}`))
			return
		}
		bank.ServeHTTP(w, r)
	}))
	created := newDispatchTransfer(t, f)
	runOnce(t, recoveryWorker(t, f, c))
	assertStatus(t, f, created.ID, "PROCESSING", false)
	bankCount(t, bankDB, 1)
	if _, err := f.db.ExecContext(t.Context(), "UPDATE transfers SET resubmit_before=NULL WHERE id=$1", created.ID); err != nil {
		t.Fatal(err)
	}
	hidden.Store(true)
	due(t, f.db, created.ID)
	runOnce(t, recoveryWorker(t, f, c))
	assertStatus(t, f, created.ID, "PROCESSING", false)
	if posts.Load() != 1 {
		t.Fatal("temporary NOT_FOUND triggered an unauthorized resubmit")
	}
	hidden.Store(false)
	due(t, f.db, created.ID)
	runOnce(t, recoveryWorker(t, f, c))
	got := assertStatus(t, f, created.ID, "SUCCESS", false)
	if got.ProviderReference != created.ProviderReference || posts.Load() != 1 {
		t.Fatal("inquiry recovery changed reference or submitted again")
	}
	bankCount(t, bankDB, 1)
	var attempts int
	if err := f.db.QueryRowContext(t.Context(), "SELECT recovery_attempts FROM transfers WHERE id=$1", created.ID).Scan(&attempts); err != nil || attempts != 3 {
		t.Fatalf("durable attempts=%d, expected 3: %v", attempts, err)
	}
}

func TestPostgresRecoveryTwoWorkersAndSkipLocked(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "success"))
	created := newDispatchTransfer(t, f)
	workers := []*RecoveryWorker{recoveryWorker(t, f, c), recoveryWorker(t, f, c)}
	start := make(chan struct{})
	results := make(chan *workClaim, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Go(func() { <-start; claim, _, err := w.claim(t.Context(), "", ""); results <- claim; errs <- err })
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var winner *workClaim
	n := 0
	for claim := range results {
		if claim != nil {
			winner = claim
			n++
		}
	}
	if n != 1 || winner.Transfer.ID != created.ID {
		t.Fatalf("claim winners=%d", n)
	}
	result, err := workers[0].perform(t.Context(), winner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workers[0].finish(t.Context(), winner, result); err != nil {
		t.Fatal(err)
	}
	bankCount(t, bankDB, 1)
	second, _, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{"second", 100, "001", "001"})
	if err != nil {
		t.Fatal(err)
	}
	third, _, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{"third", 100, "001", "001"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "SELECT id FROM transfers WHERE id=$1 FOR UPDATE", second.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	claim, _, err := workers[1].claim(ctx, "", "")
	if err != nil || claim == nil || claim.Transfer.ID != third.ID {
		t.Fatalf("locked row blocked unrelated job: %v", err)
	}
}

func TestPostgresRecoveryLeaseExpiryFencesEveryWrite(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	bank := mockHandler(t, bankDB, "success_504")
	c := networkProvider(t, bank)
	created := newDispatchTransfer(t, f)
	runOnce(t, recoveryWorker(t, f, c))
	due(t, f.db, created.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	oldClient := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			bank.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	}))
	old := recoveryWorker(t, f, oldClient)
	claim, _, err := old.claim(t.Context(), "", "")
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	resultCh := make(chan providerResult, 1)
	errCh := make(chan error, 1)
	go func() { result, err := old.perform(t.Context(), claim); resultCh <- result; errCh <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("inquiry not entered")
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "SELECT id FROM transfers WHERE id=$1 FOR UPDATE NOWAIT", created.ID); err != nil {
		tx.Rollback()
		t.Fatal("inquiry retained row lock")
	}
	tx.Rollback()
	due(t, f.db, created.ID)
	if _, err := old.finish(t.Context(), claim, providerResult{outcome: "AMBIGUOUS", reason: "STALE"}); !errors.Is(err, errLeaseLost) {
		t.Fatalf("expired owner wrote without reclaim: %v", err)
	}
	current := recoveryWorker(t, f, c)
	newClaim, _, err := current.claim(t.Context(), "", "")
	if err != nil || newClaim == nil || newClaim.Token == claim.Token {
		t.Fatal("expired lease not reclaimed")
	}
	once.Do(func() { close(release) })
	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := old.finish(t.Context(), claim, result); !errors.Is(err, errLeaseLost) {
		t.Fatalf("stale result accepted: %v", err)
	}
	var token string
	if err := f.db.QueryRowContext(t.Context(), "SELECT lease_token FROM transfers WHERE id=$1", created.ID).Scan(&token); err != nil || token != newClaim.Token {
		t.Fatal("old worker released new owner's lease")
	}
	result, err = current.perform(t.Context(), newClaim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.finish(t.Context(), newClaim, result); err != nil {
		t.Fatal(err)
	}
	if _, err := old.finish(t.Context(), claim, providerResult{outcome: "FINAL_REJECTION"}); !errors.Is(err, errLeaseLost) {
		t.Fatal("stale result changed final")
	}
	assertStatus(t, f, created.ID, "SUCCESS", false)
	bankCount(t, bankDB, 1)
}

func TestPostgresRecoveryBudgetTimeoutNotFoundAndAccepted(t *testing.T) {
	for _, mode := range []string{"timeout", "not_found", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			f := postgres(t, true)
			bankDB, _ := testdb.MockBank(t)
			c := networkProvider(t, mockHandler(t, bankDB, "accepted"))
			created := newDispatchTransfer(t, f)
			if _, err := f.db.ExecContext(t.Context(), "UPDATE transfers SET recovery_max_attempts=3 WHERE id=$1", created.ID); err != nil {
				t.Fatal(err)
			}
			if mode != "accepted" {
				if _, err := f.db.ExecContext(t.Context(), "UPDATE transfers SET status='PROCESSING',job_kind='INQUIRY',resubmit_before=NULL WHERE id=$1", created.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "timeout" {
				c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
			}
			for attempt := 1; attempt <= 3; attempt++ {
				due(t, f.db, created.ID)
				runOnce(t, recoveryWorker(t, f, c))
				assertStatus(t, f, created.ID, "PROCESSING", attempt == 3)
			}
			got := assertStatus(t, f, created.ID, "PROCESSING", true)
			if got.ReviewReason != "RETRY_BUDGET_EXHAUSTED" {
				t.Fatalf("review reason=%s", got.ReviewReason)
			}
			if did, err := recoveryWorker(t, f, c).RunOnce(t.Context()); err != nil || did {
				t.Fatal("review job automatically retried")
			}
			want := 0
			if mode == "accepted" {
				want = 1
			}
			bankCount(t, bankDB, want)
		})
	}
}

func TestPostgresRecoveryNotFoundResubmitRequiresSameEpochAndWindow(t *testing.T) {
	for _, mode := range []string{"same_dataset", "reset_dataset", "expired_window"} {
		t.Run(mode, func(t *testing.T) {
			f := postgres(t, true)
			bankDB, _ := testdb.MockBank(t)
			bank, err := mockbank.NewHandler(bankDB, []mockbank.Credential{{AccountID: "bank_a", Token: "bank-a-secret-token"}}, mockbank.Options{Scenario: "success_504", DBTimeout: 5 * time.Second, InquiryNotFound: true})
			if err != nil {
				t.Fatal(err)
			}
			var posts atomic.Int32
			c := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts.Add(1)
				}
				bank.ServeHTTP(w, r)
			}))
			created := newDispatchTransfer(t, f)
			worker := recoveryWorker(t, f, c)
			runOnce(t, worker)
			assertStatus(t, f, created.ID, "PROCESSING", false)
			bankCount(t, bankDB, 1)
			if mode == "reset_dataset" {
				if _, err := bankDB.ExecContext(t.Context(), "DELETE FROM bank_operations; UPDATE bank_retention SET epoch=gen_random_uuid()::text"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired_window" {
				if _, err := f.db.ExecContext(t.Context(), "UPDATE transfers SET resubmit_before=clock_timestamp()-interval '1 second' WHERE id=$1", created.ID); err != nil {
					t.Fatal(err)
				}
			}
			due(t, f.db, created.ID)
			runOnce(t, recoveryWorker(t, f, c))
			if mode == "same_dataset" {
				assertStatus(t, f, created.ID, "SUCCESS", false)
				bankCount(t, bankDB, 1)
				if posts.Load() != 2 {
					t.Fatal("safe duplicate was not submitted with existing key")
				}
			} else {
				assertStatus(t, f, created.ID, "PROCESSING", true)
				if posts.Load() != 1 {
					t.Fatal("resubmitted after lost guarantee")
				}
			}
		})
	}
}

func TestPostgresRecoveryOverlappingSubmitAfterLeaseExpiry(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	bank := mockHandler(t, bankDB, "success")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var posts atomic.Int32
	oldClient := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		bank.ServeHTTP(w, r)
	}))
	created := newDispatchTransfer(t, f)
	old := recoveryWorker(t, f, oldClient)
	claim, _, err := old.claim(t.Context(), "", "")
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	resultCh := make(chan providerResult, 1)
	errCh := make(chan error, 1)
	go func() { result, err := old.perform(t.Context(), claim); resultCh <- result; errCh <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first submit did not reach barrier")
	}
	due(t, f.db, created.ID)
	newClient := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
		}
		bank.ServeHTTP(w, r)
	}))
	runOnce(t, recoveryWorker(t, f, newClient))
	assertStatus(t, f, created.ID, "SUCCESS", false)
	once.Do(func() { close(release) })
	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := old.finish(t.Context(), claim, result); !errors.Is(err, errLeaseLost) {
		t.Fatal("expired submitter wrote over new result")
	}
	if posts.Load() != 2 {
		t.Fatalf("overlap not exercised: posts=%d", posts.Load())
	}
	bankCount(t, bankDB, 1)
	assertStatus(t, f, created.ID, "SUCCESS", false)
}

func TestPostgresRecoveryWrongCredentialScopeNeverSubmits(t *testing.T) {
	f := postgres(t, true)
	bankDB, _ := testdb.MockBank(t)
	bank, err := mockbank.NewHandler(bankDB, []mockbank.Credential{{AccountID: "bank_a", Token: "bank-a-secret-token"}, {AccountID: "bank_b", Token: "bank-b-secret-token"}}, mockbank.Options{Scenario: "success", DBTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := networkProvider(t, bank)
	wrong, err := NewProviderClient(endpoint.baseURL, []ProviderCredential{{"bank_a", "bank-b-secret-token"}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.CloseIdleConnections()
	created := newDispatchTransfer(t, f)
	runOnce(t, recoveryWorker(t, f, wrong))
	got := assertStatus(t, f, created.ID, "PROCESSING", true)
	if got.ReviewReason != "PROVIDER_ACCOUNT_MISMATCH" {
		t.Fatal("credential mismatch not recorded")
	}
	bankCount(t, bankDB, 0)
}
