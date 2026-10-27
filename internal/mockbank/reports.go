package mockbank

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Each report is an immutable snapshot of committed bank state visible to this transaction.
func report(ctx context.Context, db *sql.DB, account, id string) ([]byte, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT csv_content FROM bank_reports WHERE provider_account_id=$1 AND report_id=$2`, account, id).Scan(&existing)
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var epoch string
	if err = tx.QueryRowContext(ctx, `SELECT epoch FROM bank_retention WHERE singleton FOR SHARE`).Scan(&epoch); err != nil {
		return nil, err
	}
	var cutoff time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&cutoff); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM bank_operations WHERE provider_account_id=$1 AND created_at<$2
		ORDER BY provider_reference LIMIT 1001`, account, cutoff)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	w := csv.NewWriter(&buffer)
	if err = w.Write([]string{"transfer-101-report-v1", id, account, "1970-01-01T00:00:00Z", cutoff.UTC().Format(time.RFC3339Nano), cutoff.UTC().Format(time.RFC3339Nano), "true", epoch}); err != nil {
		rows.Close()
		return nil, err
	}
	if err = w.Write([]string{"provider_reference", "operation_id", "amount", "currency", "bank_code", "account_number", "status", "reason_code"}); err != nil {
		rows.Close()
		return nil, err
	}
	n := 0
	for rows.Next() {
		var o Operation
		if err = rows.Scan(&o.Reference, &o.Amount, &o.Currency, &o.BankCode, &o.AccountNumber, &o.OperationID, &o.AccountID, &o.Status, &o.ReasonCode); err != nil {
			rows.Close()
			return nil, err
		}
		n++
		if n > 1000 {
			rows.Close()
			return nil, errors.New("report exceeds 1000 operations")
		}
		if err = w.Write([]string{o.Reference, o.OperationID, strconv.FormatInt(o.Amount, 10), o.Currency, o.BankCode, o.AccountNumber, o.Status, o.ReasonCode}); err != nil {
			rows.Close()
			return nil, err
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	w.Flush()
	if err = w.Error(); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO bank_reports(provider_account_id,report_id,csv_content) VALUES($1,$2,$3)`, account, id, buffer.Bytes()); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func registerReports(mux *http.ServeMux, db *sql.DB, options Options, auth func(func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc) {
	mux.HandleFunc("GET /v1/reports/{report_id}", auth(func(w http.ResponseWriter, r *http.Request, account string) {
		id := r.PathValue("report_id")
		if !referencePattern.MatchString(id) || r.URL.RawQuery != "" {
			writeError(w, 400, "INVALID_REPORT_REQUEST")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), options.DBTimeout)
		defer cancel()
		data, err := report(ctx, db, account, id)
		// Concurrent first exports can lose the snapshot insert race; read the winning immutable report.
		if err != nil {
			err = db.QueryRowContext(ctx, `SELECT csv_content FROM bank_reports WHERE provider_account_id=$1 AND report_id=$2`, account, id).Scan(&data)
		}
		if err != nil {
			writeError(w, 503, "REPORT_UNAVAILABLE")
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		_, _ = w.Write(data)
	}))
}
