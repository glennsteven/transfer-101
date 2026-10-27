package transfer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

var errPayloadConflict = errors.New("reference_id already used with a different payload")

const columns = `id, merchant_id, source_account_id, provider_account_id,
reference_id, provider_reference, amount, currency, bank_code, account_number,
status, created_at, updated_at, needs_review, review_reason, funds_state`

func create(ctx context.Context, db *sql.DB, merchant Merchant, request createRequest) (Transfer, bool, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Transfer{}, false, err
	}
	defer tx.Rollback()
	id := "trf_" + rand.Text()
	providerReference := "prv_" + rand.Text()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO transfers (id, merchant_id, source_account_id, provider_account_id,
			reference_id, provider_reference, amount, bank_code, account_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (merchant_id, reference_id) DO NOTHING
		RETURNING id`, id, merchant.ID, merchant.SourceAccountID, merchant.ProviderAccountID,
		request.ReferenceID, providerReference, request.Amount, request.BankCode, request.AccountNumber).Scan(&id)
	if err == nil {
		stored, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM transfers WHERE id=$1", id))
		if err != nil {
			return Transfer{}, false, err
		}
		if err = reserveFunds(ctx, tx, stored); err != nil {
			return Transfer{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return Transfer{}, false, err
		}
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, false, fmt.Errorf("insert transfer: %w", err)
	}
	// A separate statement gets a new snapshot after the conflicting insert commits.
	stored, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+`
		FROM transfers WHERE merchant_id = $1 AND reference_id = $2`, merchant.ID, request.ReferenceID))
	if err != nil {
		return Transfer{}, false, fmt.Errorf("read existing transfer: %w", err)
	}
	if !request.matches(stored) {
		return Transfer{}, false, errPayloadConflict
	}
	return stored, false, tx.Commit()
}

func get(ctx context.Context, db *sql.DB, merchantID, id string) (Transfer, error) {
	return scan(db.QueryRowContext(ctx, `SELECT `+columns+`
		FROM transfers WHERE merchant_id = $1 AND id = $2`, merchantID, id))
}

func scan(row interface{ Scan(...any) error }) (Transfer, error) {
	var stored Transfer
	err := row.Scan(&stored.ID, &stored.MerchantID, &stored.SourceAccountID, &stored.ProviderAccountID,
		&stored.ReferenceID, &stored.ProviderReference, &stored.Amount, &stored.Currency,
		&stored.BankCode, &stored.AccountNumber, &stored.Status, &stored.CreatedAt, &stored.UpdatedAt, &stored.NeedsReview, &stored.ReviewReason, &stored.FundsState)
	if err != nil {
		return Transfer{}, err
	}
	stored.CreatedAt = stored.CreatedAt.UTC()
	stored.UpdatedAt = stored.UpdatedAt.UTC()
	return stored, nil
}
