//go:build integration

package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

var fixtureSchema = regexp.MustCompile(`^(transfer_(api_)?test_|mockbank_test_)[a-z0-9]+$`)

// Migrate runs the real dbmate CLI after Open has verified the test database/role.
func Migrate(t *testing.T, dsn, command string) error {
	return migrate(t, dsn, command, "DBMATE_MIGRATIONS_DIR", "transfer")
}

func MigrateMockBank(t *testing.T, dsn, command string) error {
	return migrate(t, dsn, command, "MOCK_BANK_MIGRATIONS_DIR", "mockbank")
}

func migrate(t *testing.T, dsn, command, directoryEnv, feature string) error {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("invalid migration test URL")
	}
	query := u.Query()
	if !fixtureSchema.MatchString(query.Get("search_path")) {
		return fmt.Errorf("dbmate requires an isolated fixture schema")
	}
	query.Del("search_path")
	u.RawQuery = query.Encode()
	if _, err := ValidateURL(u.String()); err != nil {
		return err
	}
	switch command {
	case "up", "migrate", "rollback", "status":
	default:
		return fmt.Errorf("unsupported test migration command")
	}
	binary := os.Getenv("DBMATE_BIN")
	if binary == "" {
		binary = "dbmate"
	}
	directory := os.Getenv(directoryEnv)
	if directory == "" {
		_, source, _, ok := runtime.Caller(0)
		if !ok {
			return fmt.Errorf("cannot locate migration directory; set DBMATE_MIGRATIONS_DIR")
		}
		directory = filepath.Join(filepath.Dir(source), "..", feature, "migrations")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--env-file=/dev/null", "--env=TRANSFER_TEST_MIGRATION_URL",
		"--migrations-dir="+directory, "--migrations-table=dbmate_schema_migrations", "--no-dump-schema", command)
	cmd.Env = append(os.Environ(), "TRANSFER_TEST_MIGRATION_URL="+dsn)
	output, err := cmd.CombinedOutput()
	redacted := strings.ReplaceAll(string(output), dsn, "[database URL]")
	if err != nil {
		return fmt.Errorf("dbmate %s: %w\n%s", command, err, redacted)
	}
	t.Logf("dbmate %s: %s", command, strings.TrimSpace(redacted))
	return nil
}
