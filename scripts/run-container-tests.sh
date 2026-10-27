#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    printf '%s\n' 'Usage: sh run-container-tests.sh DIRECTORY_WITH_TEST_BINARIES' >&2
    exit 2
fi

test_directory=$(cd "$1" && pwd)
: "${TEST_DATABASE_URL:?TEST_DATABASE_URL must be set to the dedicated transfer_test database}"
DBMATE_BIN="$test_directory/dbmate"
DBMATE_MIGRATIONS_DIR="$test_directory/migrations"
MOCK_BANK_MIGRATIONS_DIR="$test_directory/mockbank-migrations"
PROVIDER_DEMO_BIN="$test_directory/demo-provider"
export DBMATE_BIN DBMATE_MIGRATIONS_DIR MOCK_BANK_MIGRATIONS_DIR PROVIDER_DEMO_BIN
if [ ! -x "$PROVIDER_DEMO_BIN" ]; then
    printf '%s\n' 'Missing demo-provider executable.' >&2
    exit 1
fi
"$PROVIDER_DEMO_BIN" --help >/dev/null
if [ ! -x "$DBMATE_BIN" ]; then
    printf 'Missing or non-executable dbmate: %s\n' "$DBMATE_BIN" >&2
    exit 1
fi
for executable in "$test_directory/funds" "$test_directory/reconcile" "$test_directory/loadtest" "$test_directory/api" "$test_directory/mockbank" "$test_directory/recovery-worker"; do
    if [ ! -x "$executable" ]; then
        printf 'Missing or non-executable application binary: %s\n' "$executable" >&2
        exit 1
    fi
done
"$DBMATE_BIN" --version
if [ ! -f "$DBMATE_MIGRATIONS_DIR/20261020000100_create_transfers.sql" ]; then
    printf 'Missing transfer migration in %s\n' "$DBMATE_MIGRATIONS_DIR" >&2
    exit 1
fi
if [ ! -f "$DBMATE_MIGRATIONS_DIR/20261020000300_dispatch_attempts.sql" ] || [ ! -f "$MOCK_BANK_MIGRATIONS_DIR/20261020000400_mock_bank_operations.sql" ]; then
    printf '%s\n' 'Missing stage 3 migrations.' >&2
    exit 1
fi
if [ ! -f "$DBMATE_MIGRATIONS_DIR/20261021000100_recovery.sql" ] || [ ! -f "$MOCK_BANK_MIGRATIONS_DIR/20261021000200_retention_epoch.sql" ]; then
    printf '%s\n' 'Missing stage 4 migrations.' >&2
    exit 1
fi
if [ ! -f "$DBMATE_MIGRATIONS_DIR/20261021000300_funds_reconciliation.sql" ] || [ ! -f "$MOCK_BANK_MIGRATIONS_DIR/20261021000400_reports.sql" ]; then
    printf '%s\n' 'Missing stage 5 migrations.' >&2
    exit 1
fi

for test_name in transfer mockbank health api mockbank-process; do
    test_binary="$test_directory/$test_name.test"
    if [ ! -f "$test_binary" ] || [ ! -x "$test_binary" ]; then
        printf 'Missing or non-executable binary: %s\n' "$test_binary" >&2
        exit 1
    fi
    printf 'Checking executable: %s\n' "$test_binary"
    test_listing=$("$test_binary" -test.list '^TestPostgres')
    if [ -z "$test_listing" ]; then
        printf 'No PostgreSQL tests in %s; rebuild with -tags=integration\n' "$test_binary" >&2
        exit 1
    fi
    printf '%s\n' "$test_listing"
done

for test_name in transfer mockbank health api mockbank-process; do
    printf 'Running suite: %s\n' "$test_name"
    "$test_directory/$test_name.test" -test.v -test.timeout=120s
done
