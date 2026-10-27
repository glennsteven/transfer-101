package transfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	errInsufficientFunds = errors.New("insufficient available funds or funds account not initialized")
	errFundsInvariant    = errors.New("funds reservation invariant violated")
)

type Funds struct {
	MerchantID      string `json:"merchant_id"`
	SourceAccountID string `json:"source_account_id"`
	OpeningAmount   int64  `json:"opening_amount"`
	Available       int64  `json:"available"`
	Held            int64  `json:"held"`
}

func ReadFunds(ctx context.Context, db *sql.DB, merchantID, sourceID string) (Funds, error) {
	var f Funds
	err := db.QueryRowContext(ctx, `SELECT merchant_id,source_account_id,opening_amount,available,held FROM merchant_funds
		WHERE merchant_id=$1 AND source_account_id=$2`, merchantID, sourceID).Scan(&f.MerchantID, &f.SourceAccountID, &f.OpeningAmount, &f.Available, &f.Held)
	return f, err
}

// InitializeFunds is an explicit opening fixture, not a top-up or legacy backfill.
func InitializeFunds(ctx context.Context, db *sql.DB, merchantID, sourceID string, amount int64) (Funds, error) {
	if !referencePattern.MatchString(merchantID) || !referencePattern.MatchString(sourceID) || amount < 0 {
		return Funds{}, errors.New("invalid funds initialization")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Funds{}, err
	}
	defer tx.Rollback()
	var inserted string
	err = tx.QueryRowContext(ctx, `INSERT INTO merchant_funds (merchant_id,source_account_id,opening_amount,available)
		VALUES ($1,$2,$3,$3) ON CONFLICT (merchant_id,source_account_id) DO NOTHING RETURNING merchant_id`, merchantID, sourceID, amount).Scan(&inserted)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO fund_movements(merchant_id,source_account_id,kind,amount,evidence_source) VALUES($1,$2,'OPEN',$3,'explicit-initialization')`, merchantID, sourceID, amount)
	} else if errors.Is(err, sql.ErrNoRows) {
		var opening int64
		err = tx.QueryRowContext(ctx, `SELECT opening_amount FROM merchant_funds WHERE merchant_id=$1 AND source_account_id=$2`, merchantID, sourceID).Scan(&opening)
		if err == nil && opening != amount {
			return Funds{}, errors.New("funds account already initialized with a different opening amount")
		}
	}
	if err != nil {
		return Funds{}, err
	}
	if err = tx.Commit(); err != nil {
		return Funds{}, err
	}
	return ReadFunds(ctx, db, merchantID, sourceID)
}

func reserveFunds(ctx context.Context, tx *sql.Tx, t Transfer) error {
	res, err := tx.ExecContext(ctx, `UPDATE merchant_funds SET available=available-$3,held=held+$3
		WHERE merchant_id=$1 AND source_account_id=$2 AND available>=$3`, t.MerchantID, t.SourceAccountID, t.Amount)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errInsufficientFunds
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fund_movements(merchant_id,source_account_id,transfer_id,kind,amount,evidence_source)
		VALUES($1,$2,$3,'RESERVE',$4,'create')`, t.MerchantID, t.SourceAccountID, t.ID, t.Amount)
	return err
}

// Caller owns the transfer row lock. Its status/funds state update must commit in this transaction.
func settleFunds(ctx context.Context, tx *sql.Tx, t Transfer, target, source string) (string, error) {
	if t.Status == "SUCCESS" || t.Status == "FAILED" {
		return t.FundsState, nil
	}
	if t.FundsState != "HELD" || (target != "SUCCESS" && target != "FAILED") {
		return "", errFundsInvariant
	}
	var reserved bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fund_movements WHERE transfer_id=$1 AND kind='RESERVE'
		AND merchant_id=$2 AND source_account_id=$3 AND amount=$4)`, t.ID, t.MerchantID, t.SourceAccountID, t.Amount).Scan(&reserved); err != nil {
		return "", err
	}
	if !reserved {
		return "", errFundsInvariant
	}
	kind, state, availableDelta := "CAPTURE", "CAPTURED", int64(0)
	if target == "FAILED" {
		kind, state, availableDelta = "RELEASE", "RELEASED", t.Amount
	}
	res, err := tx.ExecContext(ctx, `UPDATE merchant_funds SET held=held-$3,available=available+$4
		WHERE merchant_id=$1 AND source_account_id=$2 AND held>=$3`, t.MerchantID, t.SourceAccountID, t.Amount, availableDelta)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", errFundsInvariant
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fund_movements(merchant_id,source_account_id,transfer_id,kind,amount,evidence_source)
		VALUES($1,$2,$3,$4,$5,$6)`, t.MerchantID, t.SourceAccountID, t.ID, kind, t.Amount, source)
	if err != nil {
		return "", fmt.Errorf("settle funds: %w", err)
	}
	return state, nil
}
