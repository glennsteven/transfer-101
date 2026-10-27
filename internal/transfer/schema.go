package transfer

import (
	"context"
	"database/sql"
	"fmt"
)

const migrationVersion = "20261020000100"
const dispatchMigrationVersion = "20261020000300"
const recoveryMigrationVersion = "20261021000100"
const fundsMigrationVersion = "20261021000300"

func CheckSchema(ctx context.Context, db *sql.DB) error {
	for _, required := range []string{migrationVersion, dispatchMigrationVersion, recoveryMigrationVersion, fundsMigrationVersion} {
		var version string
		if err := db.QueryRowContext(ctx, `SELECT version FROM dbmate_schema_migrations WHERE version = $1`, required).Scan(&version); err != nil {
			return fmt.Errorf("transfer schema missing or unreadable; run dbmate --migrations-dir internal/transfer/migrations --migrations-table dbmate_schema_migrations --no-dump-schema migrate: %w", err)
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT `+columns+` FROM transfers WHERE false`)
	if err != nil {
		return fmt.Errorf("check transfer columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.QueryContext(ctx, `SELECT id, transfer_id, started_at, finished_at, http_status, bank_status, outcome, reason_code, evidence, kind, lease_token FROM transfer_attempts WHERE false`)
	if err != nil {
		return fmt.Errorf("check dispatch attempts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.QueryContext(ctx, `SELECT next_attempt_at, job_kind, recovery_attempts, recovery_max_attempts,
		lease_token, lease_until, last_error, retention_epoch, resubmit_before FROM transfers WHERE false`)
	if err != nil {
		return fmt.Errorf("check recovery schema: %w", err)
	}
	defer rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, query := range []string{
		`SELECT merchant_id,source_account_id,opening_amount,available,held FROM merchant_funds WHERE false`,
		`SELECT transfer_id,kind,amount,evidence_source FROM fund_movements WHERE false`,
		`SELECT content_hash FROM reconciliation_sources WHERE false`,
		`SELECT raw_csv,source_hash,completed_at FROM reconciliation_imports WHERE false`,
		`SELECT import_id,code,decision FROM reconciliation_findings WHERE false`,
	} {
		r, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("check funds/reconciliation schema: %w", err)
		}
		if err = r.Close(); err != nil {
			return err
		}
	}
	return nil
}
