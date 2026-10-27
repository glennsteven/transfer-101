//go:build integration

package transfer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"

	"transfer-101/internal/testdb"
)

type postgresFixture struct {
	db, admin *sql.DB
	dsn       string
	schema    string
}

func postgres(t *testing.T, migrate bool) postgresFixture {
	t.Helper()
	admin, dsn := testdb.Open(t)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schema := "transfer_test_" + strings.ToLower(rand.Text())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE"); err != nil {
			t.Errorf("cleanup owned schema: %v", err)
		}
	})
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	t.Cleanup(func() { db.Close() })
	if migrate {
		if err := testdb.Migrate(t, u.String(), "migrate"); err != nil {
			t.Fatal(err)
		}
		initializeTestFunds(t, db)
	}
	t.Logf("isolated schema %s", schema)
	return postgresFixture{db: db, admin: admin, dsn: u.String(), schema: schema}
}

func initializeTestFunds(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, c := range testCredentials() {
		if _, err := InitializeFunds(t.Context(), db, c.Merchant.ID, c.Merchant.SourceAccountID, math.MaxInt64); err != nil {
			t.Fatal(err)
		}
	}
}

func setTestStatus(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stored, err := scan(tx.QueryRowContext(t.Context(), "SELECT "+columns+" FROM transfers WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		t.Fatal(err)
	}
	state := stored.FundsState
	if status == "SUCCESS" || status == "FAILED" {
		state, err = settleFunds(t.Context(), tx, stored, status, "test-fixture-final-proof")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.ExecContext(t.Context(), `UPDATE transfers SET status=$2,funds_state=$3 WHERE id=$1`, id, status, state); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (f postgresFixture) handler() http.Handler {
	mux := http.NewServeMux()
	Register(mux, f.db, testCredentials(), 10*time.Second, testLogger())
	return mux
}

func perform(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(method, path, token, body))
	return w
}

func decodeTransfer(t *testing.T, w *httptest.ResponseRecorder, status int) Transfer {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
	}
	var result Transfer
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func payload(reference string, amount int64, bank, account string) string {
	data, err := json.Marshal(createRequest{ReferenceID: reference, Amount: amount, BankCode: bank, AccountNumber: account})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func rowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM transfers").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresCreateAndDuplicate(t *testing.T) {
	f := postgres(t, true)
	h := f.handler()
	body := payload("order-001", math.MaxInt64, "001", "00123456789")
	w := perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, body)
	created := decodeTransfer(t, w, http.StatusCreated)
	if created.ID == "" || created.ProviderReference == "" || created.Status != "PENDING" || created.Currency != "IDR" ||
		created.Amount != math.MaxInt64 || created.AccountNumber != "00123456789" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("invalid created transfer: %+v", created)
	}
	if w.Header().Get("Location") != "/transfers/"+created.ID {
		t.Fatal("wrong resource location")
	}
	duplicate := decodeTransfer(t, perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, body), http.StatusOK)
	if duplicate != created || rowCount(t, f.db) != 1 {
		t.Fatal("duplicate changed resource or inserted another row")
	}
	read := decodeTransfer(t, perform(h, http.MethodGet, "/transfers/"+created.ID, testCredentials()[0].Token, ""), http.StatusOK)
	if read != created {
		t.Fatal("GET resource differs from created resource")
	}
	stored, err := get(t.Context(), f.db, "merchant_a", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderReference != created.ProviderReference || stored.SourceAccountID != "funds_a" || stored.ProviderAccountID != "bank_a" {
		t.Fatalf("routing identity/reference not durable: %+v", stored)
	}
}

func TestPostgresConflictAndDuplicateInEveryStatus(t *testing.T) {
	f := postgres(t, true)
	h := f.handler()
	for _, status := range []string{"PENDING", "PROCESSING", "SUCCESS", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			reference := "order-" + status
			body := payload(reference, 150000, "001", "0012345")
			original := decodeTransfer(t, perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, body), http.StatusCreated)
			setTestStatus(t, f.db, original.ID, status)
			before, err := get(t.Context(), f.db, "merchant_a", original.ID)
			if err != nil {
				t.Fatal(err)
			}
			duplicate := decodeTransfer(t, perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, body), http.StatusOK)
			if duplicate.ID != original.ID || duplicate.ProviderReference != original.ProviderReference || duplicate.Status != status {
				t.Fatal("existing status/reference was replaced")
			}
			for _, changed := range []string{
				payload(reference, 150001, "001", "0012345"),
				payload(reference, 150000, "002", "0012345"),
				payload(reference, 150000, "001", "12345"),
			} {
				w := perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, changed)
				if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "PAYLOAD_CONFLICT") {
					t.Fatalf("changed payload in %s: HTTP %d %s", status, w.Code, w.Body.String())
				}
			}
			after, err := get(t.Context(), f.db, "merchant_a", original.ID)
			if err != nil || after != before {
				t.Fatal("duplicate/conflict changed stored data")
			}
		})
	}
	if rowCount(t, f.db) != 4 {
		t.Fatal("duplicate/conflict inserted rows")
	}
}

func TestPostgresConcurrentIdenticalRequests(t *testing.T) {
	f := postgres(t, true)
	h := f.handler()
	const workers = 24
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-start
			results <- perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, payload("parallel", 150000, "001", "0012345"))
		}()
	}
	ready.Wait()
	close(start)
	created, duplicates := 0, 0
	var id, providerReference string
	for range workers {
		w := <-results
		if w.Code == http.StatusCreated {
			created++
		} else if w.Code == http.StatusOK {
			duplicates++
		} else {
			t.Fatalf("parallel request: HTTP %d %s", w.Code, w.Body.String())
		}
		var transfer Transfer
		if err := json.Unmarshal(w.Body.Bytes(), &transfer); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			id, providerReference = transfer.ID, transfer.ProviderReference
		}
		if transfer.ID != id || transfer.ProviderReference != providerReference || transfer.Status != "PENDING" {
			t.Fatal("parallel requests returned different resources")
		}
	}
	if created != 1 || duplicates != workers-1 || rowCount(t, f.db) != 1 {
		t.Fatalf("created=%d duplicates=%d rows=%d", created, duplicates, rowCount(t, f.db))
	}
	t.Logf("%d simultaneous requests: 1 created, %d duplicates, 1 row", workers, duplicates)
}

func TestPostgresConcurrentPayloadConflict(t *testing.T) {
	f := postgres(t, true)
	h := f.handler()
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, amount := range []int64{100, 200} {
		go func() {
			ready.Done()
			<-start
			results <- perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, payload("conflicting", amount, "001", "0012345"))
		}()
	}
	ready.Wait()
	close(start)
	counts := map[int]int{}
	for range 2 {
		w := <-results
		counts[w.Code]++
	}
	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != 1 || rowCount(t, f.db) != 1 {
		t.Fatalf("wrong concurrent results: %v", counts)
	}
}

func TestPostgresMerchantIsolation(t *testing.T) {
	f := postgres(t, true)
	h := f.handler()
	body := payload("shared-reference", 150000, "001", "0012345")
	a := decodeTransfer(t, perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, body), http.StatusCreated)
	b := decodeTransfer(t, perform(h, http.MethodPost, "/transfers", testCredentials()[1].Token, body), http.StatusCreated)
	if a.ID == b.ID || a.ProviderReference == b.ProviderReference || rowCount(t, f.db) != 2 {
		t.Fatal("merchant keys were not independent")
	}
	for _, tc := range []struct{ token, id string }{{testCredentials()[0].Token, b.ID}, {testCredentials()[1].Token, a.ID}} {
		r := request(http.MethodGet, "/transfers/"+tc.id, tc.token, "")
		r.Header.Set("X-Merchant-ID", "merchant_a")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		missing := perform(h, http.MethodGet, "/transfers/missing", tc.token, "")
		if w.Code != http.StatusNotFound || w.Body.String() != missing.Body.String() {
			t.Fatalf("another merchant's transfer was visible: %d %s", w.Code, w.Body.String())
		}
	}
	if perform(h, http.MethodGet, "/transfers/"+a.ID, "", "").Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated GET allowed")
	}
}

func TestPostgresDuplicateKeepsOriginalRouting(t *testing.T) {
	f := postgres(t, true)
	body := payload("before-config-change", 123, "001", "0012345")
	before := decodeTransfer(t, perform(f.handler(), http.MethodPost, "/transfers", testCredentials()[0].Token, body), http.StatusCreated)
	credentials := testCredentials()
	credentials[0].Merchant.SourceAccountID = "new_funds"
	credentials[0].Merchant.ProviderAccountID = "new_bank"
	mux := http.NewServeMux()
	Register(mux, f.db, credentials, time.Second, testLogger())
	after := decodeTransfer(t, perform(mux, http.MethodPost, "/transfers", credentials[0].Token, body), http.StatusOK)
	stored, err := get(t.Context(), f.db, "merchant_a", before.ID)
	if err != nil || after != before || stored.SourceAccountID != "funds_a" || stored.ProviderAccountID != "bank_a" {
		t.Fatal("configuration change rewrote existing routing")
	}
}

func TestPostgresConflictAfterUncommittedInsert(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(fmt.Sprintf("different_payload_%v", different), func(t *testing.T) {
			f := postgres(t, true)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			winner, err := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer winner.Rollback()
			if _, err := winner.ExecContext(ctx, `INSERT INTO transfers
				(id, merchant_id, source_account_id, provider_account_id, reference_id, provider_reference, amount, bank_code, account_number)
				VALUES ('trf_winner', 'merchant_a', 'funds_a', 'bank_a', 'held-key', 'prv_winner', 100, '001', '0012345')`); err != nil {
				t.Fatal(err)
			}
			follower, err := sql.Open("postgres", f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer follower.Close()
			follower.SetMaxOpenConns(1)
			var pid int
			if err := follower.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			Register(mux, follower, testCredentials(), 10*time.Second, testLogger())
			amount := int64(100)
			want := http.StatusOK
			if different {
				amount, want = 101, http.StatusConflict
			}
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				response <- perform(mux, http.MethodPost, "/transfers", testCredentials()[0].Token, payload("held-key", amount, "001", "0012345"))
			}()
			// Commit only after PostgreSQL confirms that the other connection is waiting on this transaction.
			for {
				var blocked bool
				if err := f.admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
					WHERE pid = $1 AND wait_event_type = 'Lock' AND wait_event = 'transactionid')`, pid).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
			}
			if err := winner.Commit(); err != nil {
				t.Fatal(err)
			}
			w := <-response
			if w.Code != want {
				t.Fatalf("fresh-snapshot lookup: HTTP %d, want %d: %s", w.Code, want, w.Body.String())
			}
			if !different && decodeTransfer(t, w, want).ID != "trf_winner" {
				t.Fatal("did not return the committed winner")
			}
			if rowCount(t, f.db) != 1 {
				t.Fatal("more than one committed row")
			}
		})
	}
}

func TestPostgresConstraints(t *testing.T) {
	f := postgres(t, true)
	var collatedChecks int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'transfers'::regclass AND contype = 'c'
		AND pg_get_constraintdef(oid) LIKE '%COLLATE%'`).Scan(&collatedChecks); err != nil || collatedChecks != 0 {
		t.Fatalf("unexpected explicit collation in CHECK constraints: count=%d error=%v", collatedChecks, err)
	}
	h := f.handler()
	a := decodeTransfer(t, perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, payload("a", 100, "001", "0012345")), http.StatusCreated)
	b := decodeTransfer(t, perform(h, http.MethodPost, "/transfers", testCredentials()[1].Token, payload("b", 100, "001", "0012345")), http.StatusCreated)
	_, err := f.db.ExecContext(t.Context(), "UPDATE transfers SET provider_reference = $1 WHERE id = $2", a.ProviderReference, b.ID)
	requirePGError(t, err, "23505", "transfers_provider_reference_key")
	if _, err := f.db.ExecContext(t.Context(), "UPDATE transfers SET provider_account_id = 'other_bank', provider_reference = $1 WHERE id = $2", a.ProviderReference, b.ID); err != nil {
		t.Fatalf("same reference in another provider scope should be allowed: %v", err)
	}
	_, err = f.db.ExecContext(t.Context(), "UPDATE transfers SET merchant_id = 'merchant_a', reference_id = 'a' WHERE id = $1", b.ID)
	requirePGError(t, err, "23505", "transfers_merchant_reference_key")
	for _, set := range []string{"amount = 0", "amount = -1", "status = 'UNKNOWN'", "currency = 'USD'", "bank_code = '01'", "account_number = ' 123'", "reference_id = ''", "provider_reference = ''"} {
		_, err := f.db.ExecContext(t.Context(), "UPDATE transfers SET "+set+" WHERE id = $1", a.ID)
		requirePGError(t, err, "23514", "")
	}
}

func requirePGError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pq.Error
	if !errors.As(err, &pgErr) || string(pgErr.Code) != code || (constraint != "" && pgErr.Constraint != constraint) {
		t.Fatalf("wanted PostgreSQL error %s/%s, got %v", code, constraint, err)
	}
}

func TestPostgresMigrationRerunAndRollback(t *testing.T) {
	f := postgres(t, false)
	if CheckSchema(t.Context(), f.db) == nil {
		t.Fatal("unmigrated database accepted")
	}
	if err := testdb.Migrate(t, f.dsn, "up"); err != nil {
		t.Fatal(err)
	}
	initializeTestFunds(t, f.db)
	if err := testdb.Migrate(t, f.dsn, "status"); err != nil {
		t.Fatal(err)
	}
	original := decodeTransfer(t, perform(f.handler(), http.MethodPost, "/transfers", testCredentials()[0].Token, payload("retained", 100, "001", "0012345")), http.StatusCreated)
	if err := testdb.Migrate(t, f.dsn, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(t.Context(), f.db); err != nil || rowCount(t, f.db) != 1 {
		t.Fatalf("rerun damaged migration or data: %v", err)
	}
	var versions int
	if err := f.db.QueryRowContext(t.Context(), "SELECT count(*) FROM dbmate_schema_migrations").Scan(&versions); err != nil || versions != 4 {
		t.Fatalf("migration applied more than once: count=%d error=%v", versions, err)
	}
	stored, err := get(t.Context(), f.db, "merchant_a", original.ID)
	if err != nil || stored.ID != original.ID {
		t.Fatal("rerun deleted existing transfer")
	}
	// Reapply the SQL with a pre-existing table to exercise IF NOT EXISTS.
	if _, err := f.db.ExecContext(t.Context(), "DELETE FROM dbmate_schema_migrations WHERE version = $1", migrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Migrate(t, f.dsn, "migrate"); err != nil || rowCount(t, f.db) != 1 {
		t.Fatalf("IF NOT EXISTS did not preserve existing table: %v", err)
	}
	for range 4 {
		if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
			t.Fatal(err)
		}
	}
	var exists bool
	if err := f.db.QueryRowContext(t.Context(), "SELECT to_regclass('transfers') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("rollback left transfers table: exists=%v error=%v", exists, err)
	}
	if err := f.db.QueryRowContext(t.Context(), "SELECT count(*) FROM dbmate_schema_migrations").Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("rollback left migration version: count=%d error=%v", versions, err)
	}
	if CheckSchema(t.Context(), f.db) == nil {
		t.Fatal("rolled-back schema accepted")
	}
	if err := testdb.Migrate(t, f.dsn, "up"); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(t.Context(), f.db); err != nil || rowCount(t, f.db) != 0 {
		t.Fatalf("up after rollback failed: %v", err)
	}
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
	// Exercise the defensive DROP TABLE IF EXISTS when the table is already absent.
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(t.Context(), "DROP TABLE transfers"); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMigrationFailureIsAtomic(t *testing.T) {
	f := postgres(t, false)
	directory := t.TempDir()
	invalid := `-- migrate:up
CREATE TABLE IF NOT EXISTS rollback_probe (id integer);
SELECT 1 / 0;
-- migrate:down
DROP TABLE IF EXISTS rollback_probe;
`
	if err := os.WriteFile(filepath.Join(directory, "20261020000200_failure.sql"), []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBMATE_MIGRATIONS_DIR", directory)
	if err := testdb.Migrate(t, f.dsn, "migrate"); err == nil {
		t.Fatal("intentionally failing migration succeeded")
	} else {
		t.Logf("expected transactional failure: %v", err)
	}
	var exists bool
	if err := f.db.QueryRowContext(t.Context(), "SELECT to_regclass('rollback_probe') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("failed migration committed partial DDL: exists=%v error=%v", exists, err)
	}
	var versions int
	if err := f.db.QueryRowContext(t.Context(), "SELECT count(*) FROM dbmate_schema_migrations").Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("failed migration recorded version: count=%d error=%v", versions, err)
	}
}

func TestPostgresDBMatePreservesLegacyMetadata(t *testing.T) {
	f := postgres(t, true)
	original := decodeTransfer(t, perform(f.handler(), http.MethodPost, "/transfers", testCredentials()[0].Token, payload("legacy", 100, "001", "0012345")), http.StatusCreated)
	before, err := get(t.Context(), f.db, "merchant_a", original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(t.Context(), "DROP TABLE dbmate_schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(t.Context(), `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, checksum text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT statement_timestamp()
	); INSERT INTO schema_migrations (version, checksum) VALUES ('transfer/001_transfers.sql', 'legacy-checksum')`); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Migrate(t, f.dsn, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(t.Context(), f.db); err != nil {
		t.Fatal(err)
	}
	stored, err := get(t.Context(), f.db, "merchant_a", original.ID)
	if err != nil || stored.ID != before.ID || stored.ProviderReference != before.ProviderReference || stored.Status != before.Status || stored.FundsState != "LEGACY" || !stored.NeedsReview || rowCount(t, f.db) != 1 {
		t.Fatalf("dbmate adoption changed existing transfer: error=%v", err)
	}
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err := f.db.QueryRowContext(t.Context(), "SELECT checksum FROM schema_migrations WHERE version = 'transfer/001_transfers.sql'").Scan(&checksum); err != nil || checksum != "legacy-checksum" {
		t.Fatalf("legacy metadata changed: checksum=%q error=%v", checksum, err)
	}
}

func TestPostgresSchemaCheckRejectsMissingColumns(t *testing.T) {
	f := postgres(t, true)
	if _, err := f.db.ExecContext(t.Context(), "ALTER TABLE transfers DROP COLUMN bank_code"); err != nil {
		t.Fatal(err)
	}
	if CheckSchema(t.Context(), f.db) == nil {
		t.Fatal("incompatible schema accepted")
	}
}

func TestPostgresStorageErrorIsUnavailable(t *testing.T) {
	f := postgres(t, true)
	h := f.handler()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	w := perform(h, http.MethodPost, "/transfers", testCredentials()[0].Token, payload("unavailable", 100, "001", "0012345"))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "UNAVAILABLE") {
		t.Fatalf("storage error misreported: %d %s", w.Code, w.Body.String())
	}
}
