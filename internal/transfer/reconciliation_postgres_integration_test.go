//go:build integration

package transfer

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"transfer-101/internal/testdb"
)

func reportRecords(t *testing.T, data []byte) [][]string {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func encodeReport(t *testing.T, rows [][]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := csv.NewWriter(&buffer)
	if err := w.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func storeReport(t *testing.T, db *sql.DB, id string, rows [][]string) []byte {
	t.Helper()
	rows[0][1] = id
	data := encodeReport(t, rows)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO bank_reports(provider_account_id,report_id,csv_content) VALUES('bank_a',$1,$2)`, id, data); err != nil {
		t.Fatal(err)
	}
	return data
}
func hasFinding(t *testing.T, result ReconciliationResult, code string) {
	t.Helper()
	for _, f := range result.Findings {
		if f.Code == code {
			return
		}
	}
	t.Fatalf("missing %s in %+v", code, result.Findings)
}

func TestPostgresReconciliationFinalizesOnceAcrossRepeatedConcurrentReports(t *testing.T) {
	for _, scenario := range []string{"success_504", "reject"} {
		t.Run(scenario, func(t *testing.T) {
			f := funded(t, 1000)
			bankDB, _ := testdb.MockBank(t)
			c := networkProvider(t, mockHandler(t, bankDB, scenario))
			created := newDispatchTransfer(t, f)
			w := recoveryWorker(t, f, c)
			claim, _, err := w.claim(t.Context(), "", "")
			if err != nil || claim == nil {
				t.Fatal(err)
			}
			if _, err = w.perform(t.Context(), claim); err != nil {
				t.Fatal(err)
			}
			assertFunds(t, f, 900, 100)
			data, err := c.FetchReport(t.Context(), "bank_a", "final-report")
			if err != nil {
				t.Fatal(err)
			}
			second, err := c.FetchReport(t.Context(), "bank_a", "other-report")
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan ReconciliationResult, 12)
			errs := make(chan error, 12)
			var wg sync.WaitGroup
			for i := range 12 {
				wg.Go(func() {
					<-start
					id, body := "final-report", data
					if i%2 == 1 {
						id, body = "other-report", second
					}
					result, err := ReconcileReport(t.Context(), f.db, c, "bank_a", id, body)
					results <- result
					errs <- err
				})
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
			for result := range results {
				if !result.Verified {
					t.Fatal("verified bank snapshot rejected")
				}
			}
			status, state, kind, available := "SUCCESS", "CAPTURED", "CAPTURE", int64(900)
			if scenario == "reject" {
				status, state, kind, available = "FAILED", "RELEASED", "RELEASE", 1000
			}
			got := assertStatus(t, f, created.ID, status, false)
			if got.FundsState != state {
				t.Fatal("funds mismatch")
			}
			assertFunds(t, f, available, 0)
			if movementCount(t, f.db, created.ID, kind) != 1 {
				t.Fatal("report double settled")
			}
			if _, err = w.finish(t.Context(), claim, providerResult{outcome: "FINAL_SUCCESS"}); !errors.Is(err, errLeaseLost) {
				t.Fatal("report failed to fence old owner")
			}
			reused, err := ReconcileReport(t.Context(), f.db, c, "bank_a", "final-report", data)
			if err != nil || !reused.Reused {
				t.Fatal("reimport not idempotent")
			}
			if _, err = ReadReconciliation(t.Context(), f.db, "other-account", reused.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("audit crossed account scope")
			}
			bankCount(t, bankDB, 1)
		})
	}
}

func TestPostgresReconciliationMismatchDuplicateInvalidMissingAndPartial(t *testing.T) {
	for _, mode := range []string{"amount", "bank", "number", "currency", "invalid_amount", "unknown_status", "duplicate", "conflicting_duplicate", "bank_only", "missing", "partial", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			f := funded(t, 1000)
			bankDB, _ := testdb.MockBank(t)
			c := networkProvider(t, mockHandler(t, bankDB, "accepted"))
			created := newDispatchTransfer(t, f)
			runOnce(t, recoveryWorker(t, f, c))
			base, err := c.FetchReport(t.Context(), "bank_a", "base")
			if err != nil {
				t.Fatal(err)
			}
			rows := reportRecords(t, base)
			rows[2][6] = "SUCCEEDED"
			want := ""
			switch mode {
			case "amount":
				rows[2][2] = "101"
				want = "AMOUNT_MISMATCH"
			case "bank":
				rows[2][4] = "002"
				want = "DESTINATION_MISMATCH"
			case "number":
				rows[2][5] = "002"
				want = "DESTINATION_MISMATCH"
			case "currency":
				rows[2][3] = "USD"
				want = "INVALID_ROW"
			case "invalid_amount":
				rows[2][2] = "9223372036854775808"
				want = "INVALID_ROW"
			case "unknown_status":
				rows[2][6] = "UNKNOWN"
				want = "INVALID_ROW"
			case "duplicate":
				rows = append(rows, append([]string(nil), rows[2]...))
				want = "DUPLICATE_ROW"
			case "conflicting_duplicate":
				duplicate := append([]string(nil), rows[2]...)
				duplicate[2] = "101"
				rows = append(rows, duplicate)
				want = "DUPLICATE_CONFLICT"
			case "bank_only":
				rows[2][0] = "bank-only-reference"
				want = "BANK_ONLY"
			case "missing":
				rows = rows[:2]
				want = "MISSING_FROM_REPORT"
			case "partial":
				rows[0][6] = "false"
				want = "PARTIAL_REPORT"
			case "malformed":
				want = "INVALID_CSV"
			}
			data := storeReport(t, bankDB, "fixture-"+mode, rows)
			if mode == "malformed" {
				data = append(data, []byte("\"unterminated")...)
				if _, err = bankDB.ExecContext(t.Context(), `UPDATE bank_reports SET csv_content=$2 WHERE report_id=$1`, "fixture-"+mode, data); err != nil {
					t.Fatal(err)
				}
			}
			result, err := ReconcileReport(t.Context(), f.db, c, "bank_a", "fixture-"+mode, data)
			if err != nil {
				t.Fatal(err)
			}
			hasFinding(t, result, want)
			got, err := get(t.Context(), f.db, "merchant_a", created.ID)
			if err != nil || got.Status != "PROCESSING" || got.FundsState != "HELD" {
				t.Fatal("bad/missing row changed finality")
			}
			assertFunds(t, f, 900, 100)
			if movementCount(t, f.db, created.ID, "CAPTURE") != 0 {
				t.Fatal("applied unsafe report")
			}
			if mode == "bank_only" {
				hasFinding(t, result, "MISSING_FROM_REPORT")
			}
		})
	}
}

func TestPostgresReconciliationSourceConflictFinalConflictAndAudit(t *testing.T) {
	f := funded(t, 1000)
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "success"))
	created := newDispatchTransfer(t, f)
	runOnce(t, recoveryWorker(t, f, c))
	data, err := c.FetchReport(t.Context(), "bank_a", "immutable")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileReport(t.Context(), f.db, c, "bank_a", "immutable", data)
	if err != nil {
		t.Fatal(err)
	}
	hasFinding(t, result, "MATCHED")
	rows := reportRecords(t, data)
	rows[2][6], rows[2][7] = "REJECTED", "DEMO_REJECTED"
	tampered := encodeReport(t, rows)
	untrusted, err := ReconcileReport(t.Context(), f.db, c, "bank_a", "immutable", tampered)
	if err != nil {
		t.Fatal(err)
	}
	hasFinding(t, untrusted, "SOURCE_MISMATCH")
	if untrusted.Verified {
		t.Fatal("trusted local edited file")
	}
	conflict := storeReport(t, bankDB, "contradictory", rows)
	result, err = ReconcileReport(t.Context(), f.db, c, "bank_a", "contradictory", conflict)
	if err != nil {
		t.Fatal(err)
	}
	hasFinding(t, result, "FINAL_STATUS_CONFLICT")
	assertStatus(t, f, created.ID, "SUCCESS", true)
	assertFunds(t, f, 900, 0)
	if movementCount(t, f.db, created.ID, "RELEASE") != 0 {
		t.Fatal("conflicting final returned spent funds")
	}
	rows[0][1] = "immutable"
	changed := encodeReport(t, rows)
	if _, err = bankDB.ExecContext(t.Context(), `UPDATE bank_reports SET csv_content=$2 WHERE report_id=$1`, "immutable", changed); err != nil {
		t.Fatal(err)
	}
	result, err = ReconcileReport(t.Context(), f.db, c, "bank_a", "immutable", changed)
	if err != nil {
		t.Fatal(err)
	}
	hasFinding(t, result, "REPORT_ID_CONFLICT")
	if result.Verified {
		t.Fatal("accepted report id with changed bank contents")
	}
	assertFunds(t, f, 900, 0)
	var recorded bool
	if err = f.db.QueryRowContext(t.Context(), `SELECT raw_csv=$2 AND completed_at IS NOT NULL FROM reconciliation_imports WHERE id=$1`, result.ID, changed).Scan(&recorded); err != nil || !recorded {
		t.Fatal("missing raw evidence/decision trail")
	}
}

func TestPostgresReconciliationImportRollbackAndRetry(t *testing.T) {
	f := funded(t, 1000)
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "success_504"))
	created := newDispatchTransfer(t, f)
	runOnce(t, recoveryWorker(t, f, c))
	data, err := c.FetchReport(t.Context(), "bank_a", "retry-report")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(t.Context(), `CREATE FUNCTION reject_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture audit failure'; END $$;
		CREATE TRIGGER reject_decision BEFORE INSERT ON reconciliation_findings FOR EACH ROW EXECUTE FUNCTION reject_decision()`); err != nil {
		t.Fatal(err)
	}
	if _, err = ReconcileReport(t.Context(), f.db, c, "bank_a", "retry-report", data); err == nil {
		t.Fatal("expected interrupted import")
	}
	assertStatus(t, f, created.ID, "PROCESSING", false)
	assertFunds(t, f, 900, 100)
	var imports int
	if err = f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM reconciliation_imports`).Scan(&imports); err != nil || imports != 0 {
		t.Fatal("partial import committed")
	}
	if _, err = f.db.ExecContext(t.Context(), `DROP TRIGGER reject_decision ON reconciliation_findings`); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileReport(t.Context(), f.db, c, "bank_a", "retry-report", data)
	if err != nil {
		t.Fatal(err)
	}
	hasFinding(t, result, "FINALIZED")
	assertFunds(t, f, 900, 0)
	if movementCount(t, f.db, created.ID, "CAPTURE") != 1 {
		t.Fatal("retry import double capture")
	}
}

func TestPostgresReconciliationConcurrentInquiryAndReport(t *testing.T) {
	f := funded(t, 1000)
	bankDB, _ := testdb.MockBank(t)
	bank := mockHandler(t, bankDB, "success_504")
	c := networkProvider(t, bank)
	created := newDispatchTransfer(t, f)
	runOnce(t, recoveryWorker(t, f, c))
	data, err := c.FetchReport(t.Context(), "bank_a", "race-report")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	late := networkProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			bank.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	}))
	w := recoveryWorker(t, f, late)
	due(t, f.db, created.ID)
	done := make(chan error, 1)
	go func() { _, err := w.RunOnce(t.Context()); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("inquiry not reached")
	}
	result, err := ReconcileReport(t.Context(), f.db, c, "bank_a", "race-report", data)
	if err != nil {
		t.Fatal(err)
	}
	hasFinding(t, result, "FINALIZED")
	once.Do(func() { close(release) })
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f, created.ID, "SUCCESS", false)
	assertFunds(t, f, 900, 0)
	if movementCount(t, f.db, created.ID, "CAPTURE") != 1 {
		t.Fatal("inquiry/report double finalization")
	}
	bankCount(t, bankDB, 1)
}
