package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"
)

var reportHeader = []string{"provider_reference", "operation_id", "amount", "currency", "bank_code", "account_number", "status", "reason_code"}

type reportRow struct {
	Number    int
	Fields    []string
	Operation providerOperation
	Valid     bool
}
type bankReport struct {
	ID, Account, Epoch string
	Start, End         time.Time
	Complete           bool
	Rows               []reportRow
}

func parseReport(data []byte) (bankReport, error) {
	var result bankReport
	if len(data) > MaxReportBytes {
		return result, errors.New("report exceeds 8 MiB")
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	meta, err := r.Read()
	if err != nil || len(meta) != 8 || meta[0] != "transfer-101-report-v1" {
		return result, errors.New("invalid report metadata")
	}
	result.ID, result.Account, result.Epoch = meta[1], meta[2], meta[7]
	result.Start, err = time.Parse(time.RFC3339Nano, meta[3])
	if err != nil {
		return result, errors.New("invalid report start")
	}
	result.End, err = time.Parse(time.RFC3339Nano, meta[4])
	if err != nil || !result.Start.Before(result.End) {
		return result, errors.New("invalid report end")
	}
	generated, err := time.Parse(time.RFC3339Nano, meta[5])
	if err != nil || generated.Before(result.End) {
		return result, errors.New("invalid report generation time")
	}
	if !providerReferencePattern.MatchString(result.ID) || !referencePattern.MatchString(result.Account) || !providerReferencePattern.MatchString(result.Epoch) || (meta[6] != "true" && meta[6] != "false") {
		return result, errors.New("invalid report identity or completeness")
	}
	result.Complete = meta[6] == "true"
	header, err := r.Read()
	if err != nil || !slices.Equal(header, reportHeader) {
		return result, errors.New("invalid report header")
	}
	for line := 3; ; line++ {
		fields, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, errors.New("malformed CSV; no rows applied")
		}
		if len(result.Rows) >= 1000 {
			return result, errors.New("report exceeds 1000 rows")
		}
		row := reportRow{Number: line, Fields: fields}
		if len(fields) == 8 {
			amount, amountErr := strconv.ParseInt(fields[2], 10, 64)
			row.Operation = providerOperation{providerPayload: providerPayload{fields[0], amount, fields[3], fields[4], fields[5]}, OperationID: fields[1], AccountID: result.Account, Status: fields[6], ReasonCode: fields[7]}
			row.Valid = amountErr == nil && amount > 0 && providerReferencePattern.MatchString(fields[0]) && providerReferencePattern.MatchString(fields[1]) && fields[3] == "IDR" && bankPattern.MatchString(fields[4]) && accountPattern.MatchString(fields[5]) &&
				((fields[6] == "REJECTED" && referencePattern.MatchString(fields[7])) || ((fields[6] == "SUCCEEDED" || fields[6] == "ACCEPTED") && fields[7] == ""))
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

type ReconciliationFinding struct {
	RowNumber    int             `json:"row_number"`
	TransferID   string          `json:"transfer_id,omitempty"`
	Code         string          `json:"code"`
	NeedsReview  bool            `json:"needs_review"`
	Decision     string          `json:"decision"`
	BeforeStatus string          `json:"before_status,omitempty"`
	AfterStatus  string          `json:"after_status,omitempty"`
	Detail       json.RawMessage `json:"detail"`
}
type ReconciliationResult struct {
	ID        int64                   `json:"import_id"`
	AccountID string                  `json:"provider_account_id"`
	ReportID  string                  `json:"report_id"`
	Verified  bool                    `json:"verified"`
	Reused    bool                    `json:"reused"`
	Findings  []ReconciliationFinding `json:"findings"`
}

func ReadReconciliation(ctx context.Context, db *sql.DB, account string, id int64) (ReconciliationResult, error) {
	var result ReconciliationResult
	err := db.QueryRowContext(ctx, `SELECT id,provider_account_id,report_id,verified FROM reconciliation_imports WHERE id=$1 AND provider_account_id=$2 AND completed_at IS NOT NULL`, id, account).Scan(&result.ID, &result.AccountID, &result.ReportID, &result.Verified)
	if err != nil {
		return result, err
	}
	rows, err := db.QueryContext(ctx, `SELECT row_number,COALESCE(transfer_id,''),code,needs_review,decision,before_status,after_status,detail FROM reconciliation_findings WHERE import_id=$1 ORDER BY id`, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.Findings = []ReconciliationFinding{}
	for rows.Next() {
		var f ReconciliationFinding
		if err = rows.Scan(&f.RowNumber, &f.TransferID, &f.Code, &f.NeedsReview, &f.Decision, &f.BeforeStatus, &f.AfterStatus, &f.Detail); err != nil {
			return result, err
		}
		result.Findings = append(result.Findings, f)
	}
	return result, rows.Err()
}

func reportHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Fetch and parse before opening a transaction; a local CSV alone is never trusted final evidence.
func ReconcileReport(ctx context.Context, db *sql.DB, client *ProviderClient, account, id string, data []byte) (ReconciliationResult, error) {
	if len(data) > MaxReportBytes {
		return ReconciliationResult{}, errors.New("report exceeds 8 MiB")
	}
	source, err := client.FetchReport(ctx, account, id)
	if err != nil {
		return ReconciliationResult{}, err
	}
	parsed, parseErr := parseReport(data)
	inputHash, sourceHash := reportHash(data), reportHash(source)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ReconciliationResult{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO reconciliation_sources(provider_account_id,report_id,content_hash) VALUES($1,$2,$3) ON CONFLICT(provider_account_id,report_id) DO NOTHING`, account, id, sourceHash); err != nil {
		return ReconciliationResult{}, err
	}
	var anchored string
	if err = tx.QueryRowContext(ctx, `SELECT content_hash FROM reconciliation_sources WHERE provider_account_id=$1 AND report_id=$2 FOR UPDATE`, account, id).Scan(&anchored); err != nil {
		return ReconciliationResult{}, err
	}
	verified := inputHash == sourceHash && anchored == sourceHash && parseErr == nil && parsed.Account == account && parsed.ID == id
	var importID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO reconciliation_imports(provider_account_id,report_id,content_hash,source_hash,verified,raw_csv)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(provider_account_id,report_id,content_hash,source_hash) DO NOTHING RETURNING id`, account, id, inputHash, sourceHash, verified, data).Scan(&importID)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.QueryRowContext(ctx, `SELECT id FROM reconciliation_imports WHERE provider_account_id=$1 AND report_id=$2 AND content_hash=$3 AND source_hash=$4`, account, id, inputHash, sourceHash).Scan(&importID); err != nil {
			return ReconciliationResult{}, err
		}
		if err = tx.Commit(); err != nil {
			return ReconciliationResult{}, err
		}
		result, err := ReadReconciliation(ctx, db, account, importID)
		result.Reused = true
		return result, err
	}
	if err != nil {
		return ReconciliationResult{}, err
	}
	add := func(line int, t Transfer, code string, review bool, decision, after string, detail any) error {
		encoded, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO reconciliation_findings(import_id,row_number,transfer_id,code,needs_review,decision,before_status,after_status,detail)
			VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9::jsonb)`, importID, line, t.ID, code, review, decision, t.Status, after, string(encoded))
		return err
	}
	globalCode := ""
	switch {
	case anchored != sourceHash:
		globalCode = "REPORT_ID_CONFLICT"
	case inputHash != sourceHash:
		globalCode = "SOURCE_MISMATCH"
	case parseErr != nil:
		globalCode = "INVALID_CSV"
	case parsed.Account != account || parsed.ID != id:
		globalCode = "REPORT_SCOPE_MISMATCH"
	}
	if globalCode != "" {
		if err = add(0, Transfer{}, globalCode, true, "NO_EFFECT", "", map[string]string{"input_hash": inputHash, "source_hash": sourceHash}); err != nil {
			return ReconciliationResult{}, err
		}
	} else {
		if err = applyReport(ctx, tx, parsed, importID, add); err != nil {
			return ReconciliationResult{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reconciliation_imports SET completed_at=clock_timestamp() WHERE id=$1`, importID); err != nil {
		return ReconciliationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ReconciliationResult{}, err
	}
	return ReadReconciliation(ctx, db, account, importID)
}

func applyReport(ctx context.Context, tx *sql.Tx, report bankReport, importID int64, add func(int, Transfer, string, bool, string, string, any) error) error {
	// Acquire every transfer lock before any balance lock; finalizers use transfer -> balance too.
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM transfers WHERE provider_account_id=$1 ORDER BY id FOR UPDATE`, report.Account)
	if err != nil {
		return err
	}
	local := map[string]Transfer{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			rows.Close()
			return err
		}
		local[t.ProviderReference] = t
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT merchant_id,source_account_id FROM merchant_funds WHERE (merchant_id,source_account_id) IN
		(SELECT merchant_id,source_account_id FROM transfers WHERE provider_account_id=$1) ORDER BY merchant_id,source_account_id FOR UPDATE`, report.Account)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a, b string
		if err = rows.Scan(&a, &b); err != nil {
			rows.Close()
			return err
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if !report.Complete {
		if err = add(0, Transfer{}, "PARTIAL_REPORT", true, "NO_AUTOMATIC_RESOLUTION", "", nil); err != nil {
			return err
		}
	}
	groups := map[string][]reportRow{}
	for _, row := range report.Rows {
		if len(row.Fields) > 0 {
			groups[row.Fields[0]] = append(groups[row.Fields[0]], row)
		}
	}
	markReview := func(t Transfer, code string) error {
		if t.ID == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE transfers SET needs_review=true,review_reason=$2,next_attempt_at=NULL,lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1`, t.ID, "RECONCILIATION_"+code)
		return err
	}
	for _, row := range report.Rows {
		reference := ""
		if len(row.Fields) > 0 {
			reference = row.Fields[0]
		}
		t := local[reference]
		code, review, decision, after := "MATCHED", false, "UNCHANGED", t.Status
		duplicates := groups[reference]
		switch {
		case len(duplicates) > 1:
			code, review = "DUPLICATE_ROW", true
			for _, other := range duplicates {
				if !slices.Equal(other.Fields, row.Fields) {
					code = "DUPLICATE_CONFLICT"
				}
			}
		case !row.Valid:
			code, review = "INVALID_ROW", true
		case t.ID == "":
			code, review = "BANK_ONLY", true
		case t.Amount != row.Operation.Amount:
			code, review = "AMOUNT_MISMATCH", true
		case t.Currency != row.Operation.Currency:
			code, review = "CURRENCY_MISMATCH", true
		case t.BankCode != row.Operation.BankCode || t.AccountNumber != row.Operation.AccountNumber:
			code, review = "DESTINATION_MISMATCH", true
		default:
			target := ""
			if row.Operation.Status == "SUCCEEDED" {
				target = "SUCCESS"
			}
			if row.Operation.Status == "REJECTED" {
				target = "FAILED"
			}
			switch {
			case (t.Status == "SUCCESS" || t.Status == "FAILED") && target != "" && target != t.Status:
				code, review = "FINAL_STATUS_CONFLICT", true
			case target == "":
				if t.Status == "SUCCESS" || t.Status == "FAILED" {
					code = "STATUS_DIFFERENCE"
				} else {
					code = "NONFINAL"
				}
			case t.Status == target:
			case t.FundsState == "LEGACY":
				code, review = "LEGACY_FUNDS_UNACCOUNTED", true
			case !report.Complete:
				code, review = "PARTIAL_REPORT", true
			default:
				var epoch sql.NullString
				if err = tx.QueryRowContext(ctx, `SELECT retention_epoch FROM transfers WHERE id=$1`, t.ID).Scan(&epoch); err != nil {
					return err
				}
				if !epoch.Valid || epoch.String != report.Epoch {
					code, review = "RETENTION_UNPROVEN", true
					break
				}
				state, err := settleFunds(ctx, tx, t, target, fmt.Sprintf("report:%d:%d", importID, row.Number))
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `UPDATE transfers SET status=$2,funds_state=$3,needs_review=false,review_reason='',last_error='',
					next_attempt_at=NULL,lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1`, t.ID, target, state); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `UPDATE transfer_attempts SET outcome='AMBIGUOUS',reason_code='RESOLVED_BY_REPORT',finished_at=clock_timestamp()
					WHERE transfer_id=$1 AND outcome='STARTED'`, t.ID); err != nil {
					return err
				}
				code, decision, after = "FINALIZED", "FINALIZED_"+target, target
			}
		}
		if review && t.ID != "" {
			if err = markReview(t, code); err != nil {
				return err
			}
		}
		if err = add(row.Number, t, code, review, decision, after, row.Fields); err != nil {
			return err
		}
	}
	for ref, t := range local {
		if len(groups[ref]) == 0 && !t.CreatedAt.Before(report.Start) && t.CreatedAt.Before(report.End) {
			if err = add(0, t, "MISSING_FROM_REPORT", true, "NO_EFFECT", t.Status, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
