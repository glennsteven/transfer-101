//go:build integration

package transfer

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"testing"

	"transfer-101/internal/testdb"
)

func funded(t *testing.T, amount int64) postgresFixture {
	t.Helper()
	f := postgres(t, false)
	if err := testdb.Migrate(t, f.dsn, "migrate"); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeFunds(t.Context(), f.db, "merchant_a", "funds_a", amount); err != nil {
		t.Fatal(err)
	}
	return f
}
func assertFunds(t *testing.T, f postgresFixture, available, held int64) {
	t.Helper()
	got, err := ReadFunds(t.Context(), f.db, "merchant_a", "funds_a")
	if err != nil || got.Available != available || got.Held != held {
		t.Fatalf("funds=%+v want=%d/%d error=%v", got, available, held, err)
	}
}
func movementCount(t *testing.T, db *sql.DB, id, kind string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM fund_movements WHERE transfer_id=$1 AND kind=$2`, id, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresFundsInitializationInsufficientAndRollback(t *testing.T) {
	f := funded(t, 100)
	if _, err := InitializeFunds(t.Context(), f.db, "merchant_a", "funds_a", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeFunds(t.Context(), f.db, "merchant_a", "funds_a", 101); err == nil {
		t.Fatal("silently changed opening funds")
	}
	h := f.handler()
	if got := perform(h, "POST", "/transfers", testCredentials()[0].Token, payload("too-much", 101, "001", "001")); got.Code != 422 {
		t.Fatalf("insufficient HTTP=%d %s", got.Code, got.Body.String())
	}
	if rowCount(t, f.db) != 0 {
		t.Fatal("partial transfer on insufficient funds")
	}
	assertFunds(t, f, 100, 0)
	if _, err := f.db.ExecContext(t.Context(), `CREATE FUNCTION reject_reservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture reserve failure'; END $$;
		CREATE TRIGGER reject_reservation BEFORE INSERT ON fund_movements FOR EACH ROW WHEN (NEW.kind='RESERVE') EXECUTE FUNCTION reject_reservation()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{"rollback", 100, "001", "001"}); err == nil {
		t.Fatal("expected injected reservation failure")
	}
	assertFunds(t, f, 100, 0)
	if rowCount(t, f.db) != 0 {
		t.Fatal("partial row/job after reserve journal failure")
	}
	if _, err := f.db.ExecContext(t.Context(), `DROP TRIGGER reject_reservation ON fund_movements`); err != nil {
		t.Fatal(err)
	}
	created, _, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{"duplicate", 100, "001", "001"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got, new, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{"duplicate", 100, "001", "001"})
		if err != nil || new || got.ID != created.ID {
			t.Fatal("duplicate retried reservation")
		}
	}
	if _, _, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{"duplicate", 99, "001", "001"}); !errors.Is(err, errPayloadConflict) {
		t.Fatal("balance check masked payload conflict")
	}
	assertFunds(t, f, 0, 100)
	if movementCount(t, f.db, created.ID, "RESERVE") != 1 {
		t.Fatal("duplicate reserve")
	}
	if _, _, err := create(t.Context(), f.db, testCredentials()[1].Merchant, createRequest{"not-initialized", 1, "001", "001"}); !errors.Is(err, errInsufficientFunds) {
		t.Fatal("created without initialized source")
	}
}

func TestPostgresFundsConcurrentTransfersCannotOverspend(t *testing.T) {
	f := funded(t, 100)
	start := make(chan struct{})
	results := make(chan error, 24)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			<-start
			_, _, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{fmt.Sprintf("spend-%d", i), 70, "001", "001"})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, errInsufficientFunds) {
			t.Fatal(err)
		}
	}
	if won != 1 || rowCount(t, f.db) != 1 {
		t.Fatalf("overspend wins=%d rows=%d", won, rowCount(t, f.db))
	}
	assertFunds(t, f, 30, 70)
}

func TestPostgresFundsConcurrentFinalizationExactlyOneEffect(t *testing.T) {
	for _, scenario := range []string{"success", "reject"} {
		t.Run(scenario, func(t *testing.T) {
			f := funded(t, 1000)
			bankDB, _ := testdb.MockBank(t)
			client := networkProvider(t, mockHandler(t, bankDB, scenario))
			created := newDispatchTransfer(t, f)
			w := recoveryWorker(t, f, client)
			claim, _, err := w.claim(t.Context(), "", "")
			if err != nil || claim == nil {
				t.Fatal(err)
			}
			proof, err := w.perform(t.Context(), claim)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			out := make(chan error, 12)
			var wg sync.WaitGroup
			for range 12 {
				wg.Go(func() { <-start; _, err := w.finish(t.Context(), claim, proof); out <- err })
			}
			close(start)
			wg.Wait()
			close(out)
			wins := 0
			for err := range out {
				if err == nil {
					wins++
				} else if !errors.Is(err, errLeaseLost) {
					t.Fatal(err)
				}
			}
			if wins != 1 {
				t.Fatalf("finalization winners=%d", wins)
			}
			kind, status, state, available := "CAPTURE", "SUCCESS", "CAPTURED", int64(900)
			if scenario == "reject" {
				kind, status, state, available = "RELEASE", "FAILED", "RELEASED", 1000
			}
			got := assertStatus(t, f, created.ID, status, false)
			if got.FundsState != state {
				t.Fatal("status/funds state not atomic")
			}
			assertFunds(t, f, available, 0)
			if movementCount(t, f.db, created.ID, kind) != 1 {
				t.Fatal("multiple financial effects")
			}
			_, err = f.db.ExecContext(t.Context(), `INSERT INTO fund_movements(merchant_id,source_account_id,transfer_id,kind,amount,evidence_source) VALUES('merchant_a','funds_a',$1,'RELEASE',100,'invalid-second-final')`, created.ID)
			requirePGError(t, err, "23505", "fund_settle_once")
		})
	}
}

func TestPostgresFundsTimeoutHeldInquiryCapturesAndRollbackFinal(t *testing.T) {
	f := funded(t, 1000)
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "success_504"))
	created := newDispatchTransfer(t, f)
	w := recoveryWorker(t, f, c)
	runOnce(t, w)
	assertFunds(t, f, 900, 100)
	assertStatus(t, f, created.ID, "PROCESSING", false)
	if _, err := f.db.ExecContext(t.Context(), `CREATE FUNCTION reject_capture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture capture failure'; END $$;
		CREATE TRIGGER reject_capture BEFORE INSERT ON fund_movements FOR EACH ROW WHEN (NEW.kind='CAPTURE') EXECUTE FUNCTION reject_capture()`); err != nil {
		t.Fatal(err)
	}
	due(t, f.db, created.ID)
	claim, _, err := w.claim(t.Context(), "", "")
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	proof, err := w.perform(t.Context(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.finish(t.Context(), claim, proof); err == nil {
		t.Fatal("expected final journal failure")
	}
	assertFunds(t, f, 900, 100)
	assertStatus(t, f, created.ID, "PROCESSING", false)
	if _, err := f.db.ExecContext(t.Context(), `DROP TRIGGER reject_capture ON fund_movements`); err != nil {
		t.Fatal(err)
	}
	if _, err = w.finish(t.Context(), claim, proof); err != nil {
		t.Fatal(err)
	}
	assertFunds(t, f, 900, 0)
	bankCount(t, bankDB, 1)
	if movementCount(t, f.db, created.ID, "CAPTURE") != 1 {
		t.Fatal("inquiry capture not exactly once")
	}
}

func TestPostgresFundsMaxIntegerAndReviewKeepsHeld(t *testing.T) {
	f := funded(t, math.MaxInt64)
	created, _, err := create(t.Context(), f.db, testCredentials()[0].Merchant, createRequest{"max", math.MaxInt64, "001", "001"})
	if err != nil {
		t.Fatal(err)
	}
	assertFunds(t, f, 0, math.MaxInt64)
	setTestStatus(t, f.db, created.ID, "FAILED")
	assertFunds(t, f, math.MaxInt64, 0)
	_, err = f.db.ExecContext(t.Context(), `UPDATE merchant_funds SET available=-1 WHERE merchant_id='merchant_a'`)
	requirePGError(t, err, "23514", "")
	_, err = f.db.ExecContext(t.Context(), `UPDATE merchant_funds SET held=1 WHERE merchant_id='merchant_a'`)
	requirePGError(t, err, "23514", "")
	g := funded(t, 1000)
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "accepted"))
	pending := newDispatchTransfer(t, g)
	if _, err = g.db.ExecContext(t.Context(), `UPDATE transfers SET recovery_max_attempts=1 WHERE id=$1`, pending.ID); err != nil {
		t.Fatal(err)
	}
	runOnce(t, recoveryWorker(t, g, c))
	assertStatus(t, g, pending.ID, "PROCESSING", true)
	assertFunds(t, g, 900, 100)
}

func TestPostgresFundsMigrationPreservesLegacyWithoutInventingBalance(t *testing.T) {
	f := postgres(t, false)
	if err := testdb.Migrate(t, f.dsn, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := testdb.Migrate(t, f.dsn, "rollback"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"PENDING", "PROCESSING", "SUCCESS", "FAILED"} {
		if _, err := f.db.ExecContext(t.Context(), `INSERT INTO transfers(id,merchant_id,source_account_id,provider_account_id,reference_id,provider_reference,amount,bank_code,account_number,status)
		VALUES($1,'merchant_a','funds_a','bank_a',$1,$1,100,'001','001',$2)`, "legacy-"+status, status); err != nil {
			t.Fatal(err)
		}
	}
	if err := testdb.Migrate(t, f.dsn, "migrate"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"PENDING", "PROCESSING", "SUCCESS", "FAILED"} {
		got, err := get(t.Context(), f.db, "merchant_a", "legacy-"+status)
		if err != nil || got.Status != status || got.FundsState != "LEGACY" {
			t.Fatalf("legacy changed: %+v %v", got, err)
		}
		if (status == "PENDING" || status == "PROCESSING") && (!got.NeedsReview || got.ReviewReason != "LEGACY_FUNDS_UNACCOUNTED") {
			t.Fatal("unbacked legacy still runnable")
		}
	}
	var count int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM merchant_funds`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invented historical balance")
	}
	bankDB, _ := testdb.MockBank(t)
	c := networkProvider(t, mockHandler(t, bankDB, "success"))
	w := recoveryWorker(t, f, c)
	if did, err := w.RunOnce(t.Context()); err != nil || did {
		t.Fatal("legacy dispatched without backing")
	}
	bankCount(t, bankDB, 0)
	if _, err := InitializeFunds(t.Context(), f.db, "merchant_a", "funds_a", 1000); err != nil {
		t.Fatal(err)
	}
	if did, err := w.RunOnce(t.Context()); err != nil || did {
		t.Fatal("initialization silently adopted legacy")
	}
	if got := perform(f.handler(), http.MethodPost, "/transfers", testCredentials()[0].Token, payload("legacy-PENDING", 100, "001", "001")); got.Code != 200 {
		t.Fatal("legacy duplicate unavailable")
	}
	assertFunds(t, f, 1000, 0)
}
