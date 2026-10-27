package mockbank

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var accountPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var tokenPattern = regexp.MustCompile(`^[!-~]{16,256}$`)
var bankPattern = regexp.MustCompile(`^[0-9]{3}$`)
var numberPattern = regexp.MustCompile(`^[0-9]{1,34}$`)

type Credential struct {
	AccountID string `json:"provider_account_id"`
	Token     string `json:"token"`
}

func LoadCredentials(path string) ([]Credential, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open mock bank credentials")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return nil, errors.New("mock bank credentials unreadable or exceed 64 KiB")
	}
	var credentials []Credential
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&credentials) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid mock bank credentials JSON")
	}
	if err := validateCredentials(credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

func validateCredentials(credentials []Credential) error {
	accounts, tokens := map[string]bool{}, map[string]bool{}
	for _, c := range credentials {
		if !accountPattern.MatchString(c.AccountID) || !tokenPattern.MatchString(c.Token) || accounts[c.AccountID] || tokens[c.Token] {
			return errors.New("invalid or duplicate mock bank account/token")
		}
		accounts[c.AccountID], tokens[c.Token] = true, true
	}
	if len(accounts) == 0 {
		return errors.New("at least one mock bank account required")
	}
	return nil
}

type Options struct {
	Scenario        string
	DBTimeout       time.Duration
	ResponseDelay   time.Duration
	InquiryNotFound bool
	afterCommit     func()
}

func ValidateOptions(o Options) error {
	switch o.Scenario {
	case "success", "reject", "accepted", "success_504", "accepted_500", "success_timeout":
	default:
		return errors.New("unknown MOCK_BANK_SCENARIO")
	}
	if o.DBTimeout <= 0 || o.DBTimeout > 30*time.Second || o.ResponseDelay < 0 || o.ResponseDelay > time.Minute {
		return errors.New("invalid mock bank timeout/delay")
	}
	if o.Scenario == "success_timeout" && o.ResponseDelay == 0 {
		return errors.New("success_timeout requires positive response delay")
	}
	return nil
}

type payload struct {
	Reference     string `json:"provider_reference"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
}

type Operation struct {
	payload
	OperationID string `json:"operation_id"`
	AccountID   string `json:"provider_account_id"`
	Status      string `json:"status"`
	ReasonCode  string `json:"reason_code,omitempty"`
}

const columns = `provider_reference, amount, currency, bank_code, account_number,
operation_id, provider_account_id, status, reason_code`

func scan(row *sql.Row) (Operation, error) {
	var o Operation
	err := row.Scan(&o.Reference, &o.Amount, &o.Currency, &o.BankCode, &o.AccountNumber, &o.OperationID, &o.AccountID, &o.Status, &o.ReasonCode)
	return o, err
}

func submit(ctx context.Context, db *sql.DB, account string, p payload, scenario, expectedEpoch string, before time.Time) (Operation, bool, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Operation{}, false, err
	}
	defer tx.Rollback()
	var epoch string
	var now time.Time
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM bank_retention WHERE singleton FOR SHARE").Scan(&epoch); err != nil {
		return Operation{}, false, err
	}
	// Read the clock after acquiring the lock, rather than before a possible lock wait.
	if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return Operation{}, false, err
	}
	if expectedEpoch != "" && (expectedEpoch != epoch || !now.Before(before)) {
		return Operation{}, false, errRetention
	}
	status, reason := "SUCCEEDED", ""
	if scenario == "accepted" || scenario == "accepted_500" {
		status = "ACCEPTED"
	}
	if scenario == "reject" {
		status, reason = "REJECTED", "DEMO_REJECTED"
	}
	operation, err := scan(tx.QueryRowContext(ctx, `INSERT INTO bank_operations
		(operation_id, provider_account_id, provider_reference, amount, currency, bank_code, account_number, status, reason_code, scenario)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (provider_account_id, provider_reference) DO NOTHING RETURNING `+columns,
		"bank_"+rand.Text(), account, p.Reference, p.Amount, p.Currency, p.BankCode, p.AccountNumber, status, reason, scenario))
	if err == nil {
		return operation, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, err
	}
	operation, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM bank_operations WHERE provider_account_id=$1 AND provider_reference=$2", account, p.Reference))
	if err != nil {
		return Operation{}, false, err
	}
	if operation.payload != p {
		return Operation{}, false, errConflict
	}
	return operation, false, tx.Commit()
}

var errConflict = errors.New("provider reference payload conflict")
var errRetention = errors.New("provider retention guard rejected")

func NewHandler(db *sql.DB, credentials []Credential, options Options) (http.Handler, error) {
	if err := validateCredentials(credentials); err != nil {
		return nil, err
	}
	if err := ValidateOptions(options); err != nil {
		return nil, err
	}
	accounts := make(map[[32]byte]string)
	for _, c := range credentials {
		accounts[sha256.Sum256([]byte(c.Token))] = c.AccountID
	}
	mux := http.NewServeMux()
	auth := func(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fields := strings.Fields(r.Header.Get("Authorization"))
			if len(r.Header.Values("Authorization")) == 1 && len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
				if account, ok := accounts[sha256.Sum256([]byte(fields[1]))]; ok {
					w.Header().Set("X-Mock-Provider-Account", account)
					if db != nil {
						ctx, cancel := context.WithTimeout(r.Context(), options.DBTimeout)
						var epoch string
						err := db.QueryRowContext(ctx, "SELECT epoch FROM bank_retention WHERE singleton").Scan(&epoch)
						cancel()
						if err != nil {
							writeError(w, 503, "UNAVAILABLE")
							return
						}
						w.Header().Set("X-Mock-Retention-Epoch", epoch)
					}
					next(w, r, account)
					return
				}
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="mock-bank"`)
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED")
		}
	}
	mux.HandleFunc("POST /v1/transfers", auth(func(w http.ResponseWriter, r *http.Request, account string) {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			writeError(w, 415, "UNSUPPORTED_MEDIA_TYPE")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		var p payload
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		err = d.Decode(&p)
		if err == nil {
			err = d.Decode(new(any))
			if err == io.EOF {
				err = nil
			} else if err == nil {
				err = errors.New("trailing data")
			}
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "REQUEST_TOO_LARGE")
			return
		}
		if err != nil || !referencePattern.MatchString(p.Reference) || p.Amount <= 0 || p.Currency != "IDR" || !bankPattern.MatchString(p.BankCode) || !numberPattern.MatchString(p.AccountNumber) {
			writeError(w, 400, "INVALID_REQUEST")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), options.DBTimeout)
		epoch := r.Header.Get("X-Mock-Retention-Epoch")
		var before time.Time
		if epoch != "" || r.Header.Get("X-Mock-Resubmit-Before") != "" {
			before, err = time.Parse(time.RFC3339Nano, r.Header.Get("X-Mock-Resubmit-Before"))
			if err != nil || !referencePattern.MatchString(epoch) {
				cancel()
				writeError(w, 412, "RETENTION_GUARD_REJECTED")
				return
			}
		}
		operation, created, err := submit(ctx, db, account, p, options.Scenario, epoch, before)
		cancel()
		if errors.Is(err, errConflict) {
			writeError(w, 409, "PAYLOAD_CONFLICT")
			return
		}
		if errors.Is(err, errRetention) {
			writeError(w, 412, "RETENTION_GUARD_REJECTED")
			return
		}
		if err != nil {
			writeError(w, 503, "UNAVAILABLE")
			return
		}
		// The bank transaction has committed before any simulated transport failure.
		if created {
			if options.afterCommit != nil {
				options.afterCommit()
			}
			switch options.Scenario {
			case "success_504":
				writeError(w, 504, "SIMULATED_GATEWAY_TIMEOUT")
				return
			case "accepted_500":
				writeError(w, 500, "SIMULATED_INTERNAL_ERROR")
				return
			case "success_timeout":
				timer := time.NewTimer(options.ResponseDelay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
			}
		}
		writeJSON(w, 200, operation)
	}))
	mux.HandleFunc("GET /v1/transfers/{reference}", auth(func(w http.ResponseWriter, r *http.Request, account string) {
		if options.InquiryNotFound {
			writeError(w, 404, "NOT_FOUND")
			return
		}
		if !referencePattern.MatchString(r.PathValue("reference")) {
			writeError(w, 404, "NOT_FOUND")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), options.DBTimeout)
		defer cancel()
		o, err := scan(db.QueryRowContext(ctx, "SELECT "+columns+" FROM bank_operations WHERE provider_account_id=$1 AND provider_reference=$2", account, r.PathValue("reference")))
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "NOT_FOUND")
			return
		}
		if err != nil {
			writeError(w, 503, "UNAVAILABLE")
			return
		}
		writeJSON(w, 200, o)
	}))
	registerReports(mux, db, options, auth)
	return mux, nil
}

func CheckSchema(ctx context.Context, db *sql.DB) error {
	var reportVersion string
	if err := db.QueryRowContext(ctx, `SELECT version FROM dbmate_schema_migrations WHERE version='20261021000400'`).Scan(&reportVersion); err != nil {
		return errors.New("mock bank reports schema missing; apply migrations")
	}
	reportRows, err := db.QueryContext(ctx, `SELECT provider_account_id,report_id,csv_content FROM bank_reports WHERE false`)
	if err != nil {
		return errors.New("mock bank reports schema incompatible")
	}
	if err = reportRows.Close(); err != nil {
		return err
	}
	var epoch string
	if err := db.QueryRowContext(ctx, "SELECT epoch FROM bank_retention WHERE singleton").Scan(&epoch); err != nil {
		return errors.New("mock bank retention schema missing; run mock bank migrations")
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT version FROM dbmate_schema_migrations WHERE version='20261020000400'").Scan(&version); err != nil {
		return errors.New("mock bank schema missing; run dbmate for internal/mockbank/migrations")
	}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM bank_operations WHERE false")
	if err != nil {
		return errors.New("mock bank schema incompatible")
	}
	defer rows.Close()
	return rows.Err()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}
