//go:build integration

package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"github.com/lib/pq"
	"net/url"
	"strings"
	"testing"
	"time"
)

func MockBank(t *testing.T) (*sql.DB, string) {
	t.Helper()
	admin, dsn := Open(t)
	schema := "mockbank_test_" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE"); err != nil {
			t.Errorf("cleanup bank fixture: %v", err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(24)
	t.Cleanup(func() { db.Close() })
	if err := MigrateMockBank(t, u.String(), "migrate"); err != nil {
		t.Fatal(err)
	}
	return db, u.String()
}
