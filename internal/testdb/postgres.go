//go:build integration

package testdb

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// Open checks the connected database and role before any fixture can run DDL.
func Open(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn, err := ValidateURL(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var database, role, version string
	if err := db.QueryRowContext(ctx, `SELECT current_database(), current_user, current_setting('server_version')`).Scan(&database, &role, &version); err != nil {
		t.Fatalf("dedicated PostgreSQL test database required: %v", err)
	}
	if database != "transfer_test" || role != "transfer_test" {
		t.Fatal("connected database or role is not transfer_test; refusing fixture setup")
	}
	t.Logf("verified database=%s role=%s PostgreSQL=%s", database, role, version)
	return db, dsn
}
