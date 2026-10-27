package testdb

import (
	"net/url"
	"strings"
	"testing"
)

func TestValidateURLRejectsUnsafeTargets(t *testing.T) {
	for _, value := range []string{
		"",
		"postgres://transfer_demo:private-value@localhost:54329/transfer_demo?sslmode=disable",
		"postgres://transfer_test:private-value@localhost/transfer_demo",
		"postgres://transfer_demo:private-value@localhost/transfer_test",
		"postgres://localhost/transfer_test",
		"https://transfer_test:private-value@localhost/transfer_test",
		"postgres://transfer_test:private-value@/transfer_test",
		"postgres://transfer_test:private-value@localhost/transfer_test#fragment",
		"postgres://transfer_test:private-value@localhost/transfer_test?dbname=transfer_demo",
		"postgres://transfer_test:private-value@localhost/transfer_test?user=transfer_demo",
		"postgres://transfer_test:private-value@localhost/transfer_test?service=demo",
		"postgres://transfer_test:private-value@localhost/transfer_test?options=-c%20role=transfer_demo",
		"postgres://transfer_test:private-value@localhost/transfer_test?sslmode=%zz",
	} {
		_, err := ValidateURL(value)
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("unsafe URL accepted or credential exposed: %v", err)
		}
	}
}

func TestValidateURLPreservesTestTargetAndBoundsConnect(t *testing.T) {
	value, err := ValidateURL("postgresql://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=0")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(value)
	if err != nil || u.Path != "/transfer_test" || u.User.Username() != "transfer_test" ||
		u.Query().Get("sslmode") != "disable" || u.Query().Get("connect_timeout") != "3" {
		t.Fatal("test target or options changed unexpectedly")
	}
}
