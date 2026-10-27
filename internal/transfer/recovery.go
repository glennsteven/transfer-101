package transfer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	random "math/rand/v2"
	"time"
)

var errLeaseLost = errors.New("recovery lease no longer owned")

type RecoveryOptions struct {
	DBTimeout   time.Duration
	Lease       time.Duration
	Poll        time.Duration
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	jitter      func(time.Duration) time.Duration
	afterClaim  func()
	afterHTTP   func()
	afterFinish func()
}

func DefaultRecoveryOptions() RecoveryOptions {
	return RecoveryOptions{DBTimeout: 2 * time.Second, Lease: 30 * time.Second, Poll: 500 * time.Millisecond, BaseBackoff: time.Second, MaxBackoff: time.Minute}
}

type RecoveryWorker struct {
	db      *sql.DB
	client  *ProviderClient
	options RecoveryOptions
	logger  *slog.Logger
}

func NewRecoveryWorker(db *sql.DB, client *ProviderClient, options RecoveryOptions) (*RecoveryWorker, error) {
	if options.DBTimeout <= 0 || options.DBTimeout > 30*time.Second || options.Lease <= 0 || options.Lease > time.Hour ||
		options.Poll <= 0 || options.Poll > time.Minute || options.BaseBackoff < time.Microsecond || options.MaxBackoff < options.BaseBackoff || options.MaxBackoff > time.Hour {
		return nil, errors.New("invalid recovery timeout, lease, polling, or backoff configuration")
	}
	if options.jitter == nil {
		options.jitter = func(cap time.Duration) time.Duration { return cap/2 + time.Duration(random.Int64N(int64(cap-cap/2)+1)) }
	}
	return &RecoveryWorker{db: db, client: client, options: options}, nil
}

type workClaim struct {
	Transfer    Transfer
	Token       string
	AttemptID   int64
	Kind        string
	Number, Max int
	Epoch       string
}

func (w *RecoveryWorker) claim(ctx context.Context, merchantID, id string) (*workClaim, bool, error) {
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	predicate := `status IN ('PENDING','PROCESSING') AND next_attempt_at<=clock_timestamp()`
	var args []any
	if id != "" {
		predicate = `id=$1 AND merchant_id=$2 AND status='PENDING'`
		args = []any{id, merchantID}
	}
	var selected string
	err = tx.QueryRowContext(ctx, `SELECT id FROM transfers WHERE `+predicate+`
		AND funds_state='HELD' AND NOT needs_review AND (lease_until IS NULL OR lease_until<=clock_timestamp())
		ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, args...).Scan(&selected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c := &workClaim{Token: rand.Text()}
	var reserved bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fund_movements m JOIN transfers t ON t.id=m.transfer_id
		WHERE t.id=$1 AND m.kind='RESERVE' AND m.merchant_id=t.merchant_id AND m.source_account_id=t.source_account_id AND m.amount=t.amount)`, selected).Scan(&reserved); err != nil {
		return nil, false, err
	}
	if !reserved {
		_, err := tx.ExecContext(ctx, `UPDATE transfers SET needs_review=true,review_reason='FUNDS_RESERVATION_MISSING',next_attempt_at=NULL WHERE id=$1`, selected)
		if err != nil {
			return nil, false, err
		}
		return nil, true, tx.Commit()
	}
	if err := tx.QueryRowContext(ctx, `SELECT job_kind,recovery_attempts,recovery_max_attempts,COALESCE(retention_epoch,'') FROM transfers WHERE id=$1`, selected).Scan(&c.Kind, &c.Number, &c.Max, &c.Epoch); err != nil {
		return nil, false, err
	}
	// The new owner closes abandoned journal entries, never the stale worker.
	if _, err := tx.ExecContext(ctx, `UPDATE transfer_attempts SET outcome='AMBIGUOUS',reason_code='LEASE_EXPIRED',finished_at=clock_timestamp()
		WHERE transfer_id=$1 AND outcome='STARTED'`, selected); err != nil {
		return nil, false, err
	}
	if c.Number >= c.Max {
		if _, err := tx.ExecContext(ctx, `UPDATE transfers SET needs_review=true,review_reason='RETRY_BUDGET_EXHAUSTED',
			next_attempt_at=NULL,lease_token=NULL,lease_until=NULL WHERE id=$1`, selected); err != nil {
			return nil, false, err
		}
		return nil, true, tx.Commit()
	}
	c.Number++
	c.Transfer, err = scan(tx.QueryRowContext(ctx, `UPDATE transfers SET status='PROCESSING',job_kind='INQUIRY',
		recovery_attempts=recovery_attempts+1,lease_token=$2,
		lease_until=clock_timestamp()+$3::bigint*interval '1 microsecond',
		next_attempt_at=clock_timestamp()+$3::bigint*interval '1 microsecond',updated_at=clock_timestamp()
		WHERE id=$1 RETURNING `+columns, selected, c.Token, w.options.Lease.Microseconds()))
	if err != nil {
		return nil, false, err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO transfer_attempts (transfer_id,lease_token,kind) VALUES ($1,$2,$3) RETURNING id`, selected, c.Token, c.Kind).Scan(&c.AttemptID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return c, true, nil
}

func (w *RecoveryWorker) authorizeResubmit(ctx context.Context, c *workClaim, epoch string) (time.Time, string, error) {
	if epoch == "" {
		return time.Time{}, "NO_RETENTION_GUARANTEE", nil
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, "", err
	}
	defer tx.Rollback()
	var stored sql.NullString
	var before sql.NullTime
	var live, within bool
	err = tx.QueryRowContext(ctx, `SELECT retention_epoch,resubmit_before,lease_until>clock_timestamp(),
		COALESCE(resubmit_before>clock_timestamp(),false) FROM transfers WHERE id=$1 AND lease_token=$2 FOR UPDATE`, c.Transfer.ID, c.Token).Scan(&stored, &before, &live, &within)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !live {
		return time.Time{}, "", errLeaseLost
	}
	if err != nil {
		return time.Time{}, "", err
	}
	if stored.Valid && stored.String != epoch {
		return time.Time{}, "PROVIDER_RETENTION_CHANGED", nil
	}
	if !before.Valid {
		return time.Time{}, "RESUBMIT_NOT_AUTHORIZED", nil
	}
	if !within {
		return time.Time{}, "RESUBMIT_WINDOW_EXPIRED", nil
	}
	var bound string
	err = tx.QueryRowContext(ctx, `UPDATE transfers SET retention_epoch=COALESCE(retention_epoch,$3)
		WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() AND resubmit_before>clock_timestamp()
		RETURNING retention_epoch`, c.Transfer.ID, c.Token, epoch).Scan(&bound)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", errLeaseLost
	}
	if err != nil {
		return time.Time{}, "", err
	}
	kind := c.Kind
	if kind == "INQUIRY" {
		kind = "RESUBMIT"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE transfer_attempts SET kind=$1 WHERE id=$2 AND lease_token=$3", kind, c.AttemptID, c.Token); err != nil {
		return time.Time{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, "", err
	}
	c.Kind = kind
	c.Epoch = bound
	return before.Time, "", nil
}

func (w *RecoveryWorker) perform(ctx context.Context, c *workClaim) (providerResult, error) {
	result := w.client.inquiry(ctx, c.Transfer)
	if c.Epoch != "" && result.bankStatus != "" && result.retentionEpoch == "" {
		return providerResult{outcome: "AMBIGUOUS", reason: "RETENTION_GUARANTEE_MISSING"}, nil
	}
	if c.Epoch != "" && result.retentionEpoch != "" && result.retentionEpoch != c.Epoch {
		return providerResult{outcome: "AMBIGUOUS", reason: "PROVIDER_RETENTION_CHANGED", reviewReason: "PROVIDER_RETENTION_CHANGED"}, nil
	}
	if !result.notFound {
		return result, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, w.options.DBTimeout)
	before, review, err := w.authorizeResubmit(checkCtx, c, result.retentionEpoch)
	cancel()
	if err != nil {
		return providerResult{}, err
	}
	if review != "" {
		result.reason = review
		if review != "NO_RETENTION_GUARANTEE" && review != "RESUBMIT_NOT_AUTHORIZED" {
			result.reviewReason = review
		}
		return result, nil
	}
	result = w.client.submitGuarded(ctx, c.Transfer, c.Epoch, before)
	if result.bankStatus != "" && result.retentionEpoch == "" {
		return providerResult{outcome: "AMBIGUOUS", reason: "RETENTION_GUARANTEE_MISSING"}, nil
	}
	if result.retentionEpoch != "" && result.retentionEpoch != c.Epoch {
		result = providerResult{outcome: "AMBIGUOUS", reason: "PROVIDER_RETENTION_CHANGED", reviewReason: "PROVIDER_RETENTION_CHANGED"}
	}
	return result, nil
}

func (w *RecoveryWorker) backoff(number int) time.Duration {
	cap := w.options.BaseBackoff
	for n := 1; n < number && cap < w.options.MaxBackoff; n++ {
		if cap > w.options.MaxBackoff/2 {
			cap = w.options.MaxBackoff
		} else {
			cap *= 2
		}
	}
	delay := w.options.jitter(cap)
	if delay < cap/2 {
		delay = cap / 2
	}
	if delay > cap {
		delay = cap
	}
	return delay
}

func (w *RecoveryWorker) finish(ctx context.Context, c *workClaim, result providerResult) (Transfer, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback()
	t, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM transfers WHERE id=$1 AND lease_token=$2 FOR UPDATE", c.Transfer.ID, c.Token))
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, errLeaseLost
	}
	if err != nil {
		return Transfer{}, err
	}
	next := t.Status
	wanted := ""
	if result.outcome == "FINAL_SUCCESS" {
		wanted = "SUCCESS"
	}
	if result.outcome == "FINAL_REJECTION" {
		wanted = "FAILED"
	}
	if (next == "SUCCESS" || next == "FAILED") && wanted != "" && wanted != next {
		result.outcome = "FINAL_CONFLICT"
		result.reviewReason = "CONTRADICTING_FINAL_EVIDENCE"
	}
	if next == "PROCESSING" && wanted != "" {
		next = wanted
	}
	final := next == "SUCCESS" || next == "FAILED"
	fundsState := t.FundsState
	if final && t.Status != next {
		fundsState, err = settleFunds(ctx, tx, t, next, fmt.Sprintf("attempt:%d", c.AttemptID))
		if err != nil {
			return Transfer{}, err
		}
	}
	review := result.reviewReason
	if !final && review == "" && c.Number >= c.Max {
		review = "RETRY_BUDGET_EXHAUSTED"
	}
	var evidence any
	if len(result.evidence) > 0 {
		evidence = string(result.evidence)
	}
	res, err := tx.ExecContext(ctx, `UPDATE transfer_attempts SET finished_at=clock_timestamp(),http_status=NULLIF($1,0),bank_status=NULLIF($2,''),
		outcome=$3,reason_code=NULLIF($4,''),evidence=$5::jsonb WHERE id=$6 AND transfer_id=$7 AND lease_token=$8 AND outcome='STARTED'`,
		result.httpStatus, result.bankStatus, result.outcome, result.reason, evidence, c.AttemptID, c.Transfer.ID, c.Token)
	if err != nil {
		return Transfer{}, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return Transfer{}, errLeaseLost
	}
	t, err = scan(tx.QueryRowContext(ctx, `UPDATE transfers SET status=$3,needs_review=$4,review_reason=$5,last_error=$6,
		next_attempt_at=CASE WHEN $7 THEN NULL ELSE clock_timestamp()+$8::bigint*interval '1 microsecond' END,
		lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp(),funds_state=$9
		WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() RETURNING `+columns,
		c.Transfer.ID, c.Token, next, review != "", review, result.reason, final || review != "", w.backoff(c.Number).Microseconds(), fundsState))
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, errLeaseLost
	}
	if err != nil {
		return Transfer{}, err
	}
	if err := tx.Commit(); err != nil {
		return Transfer{}, err
	}
	return t, nil
}

func (w *RecoveryWorker) RunOnce(ctx context.Context) (bool, error) {
	claimCtx, cancel := context.WithTimeout(ctx, w.options.DBTimeout)
	c, touched, err := w.claim(claimCtx, "", "")
	cancel()
	if err != nil || c == nil {
		return touched, err
	}
	if w.options.afterClaim != nil {
		w.options.afterClaim()
	}
	started := time.Now()
	result, err := w.perform(ctx, c)
	if errors.Is(err, errLeaseLost) {
		w.logAttempt(c, result, started, 0, false, err)
		return true, nil
	}
	if err != nil {
		w.logAttempt(c, result, started, 0, false, err)
		return true, err
	}
	providerDuration := time.Since(started)
	if w.options.afterHTTP != nil {
		w.options.afterHTTP()
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), w.options.DBTimeout)
	_, err = w.finish(finishCtx, c, result)
	finishCancel()
	w.logAttempt(c, result, started, providerDuration, err == nil, err)
	if errors.Is(err, errLeaseLost) {
		return true, nil
	}
	if err == nil && w.options.afterFinish != nil {
		w.options.afterFinish()
	}
	return true, err
}

func (w *RecoveryWorker) logAttempt(c *workClaim, result providerResult, started time.Time, providerDuration time.Duration, persisted bool, err error) {
	if w.logger == nil {
		return
	}
	attrs := []any{
		"transaction_id", c.Transfer.ID,
		"provider_reference", c.Transfer.ProviderReference,
		"operation", c.Kind,
		"attempt", c.Number,
		"duration_ms", time.Since(started).Milliseconds(),
		"provider_duration_ms", providerDuration.Milliseconds(),
		"outcome", result.outcome,
		"persisted", persisted,
	}
	if result.bankStatus != "" {
		attrs = append(attrs, "provider_status", result.bankStatus)
	}
	if result.reason != "" {
		attrs = append(attrs, "reason_code", result.reason)
	}
	if err != nil {
		attrs = append(attrs, "error_type", errorType(err))
	}
	w.logger.Info("provider attempt completed", attrs...)
}

func (w *RecoveryWorker) Run(ctx context.Context, logger *slog.Logger) error {
	w.logger = logger
	for ctx.Err() == nil {
		did, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Error("recovery operation failed", "error_type", errorType(err))
		}
		if err != nil || !did {
			timer := time.NewTimer(w.options.Poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
	return nil
}
