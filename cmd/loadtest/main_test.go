package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoadTestNamespaceAndLoopbackGuards(t *testing.T) {
	o := options{
		baseURL: "http://127.0.0.1:8080", token: "fixture-token", merchant: "merchant_a", providerAccount: "bank_a",
		prefix: "load-run-1", databaseURL: "postgres://u:p@127.0.0.1:54329/transfer_demo",
		bankDatabaseURL: "postgres://u:p@127.0.0.1:54331/mock_bank", requests: 100, concurrency: 4, amount: 100,
		duplicatePercent: 30, timeout: 2, duration: 10, completionTimeout: 10,
	}
	if err := validate(&o); err != nil {
		t.Fatal(err)
	}
	o.baseURL = "https://example.com"
	if err := validate(&o); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatal("non-local API target accepted")
	}
	o.baseURL = "http://127.0.0.1:8080"
	o.databaseURL = "postgres://u:p@127.0.0.1:54330/transfer_test"
	if err := validate(&o); err == nil || !strings.Contains(err.Error(), "transfer_test") {
		t.Fatal("test database accepted without an isolated schema opt-in")
	}
	u, _ := url.Parse("postgres://u:p@127.0.0.1:54330/transfer_test?search_path=loadtest_app1")
	o.databaseURL = u.String()
	u, _ = url.Parse("postgres://u:p@127.0.0.1:54330/transfer_test?search_path=loadtest_bank1")
	o.bankDatabaseURL = u.String()
	o.allowTestDatabase = true
	if err := validate(&o); err != nil {
		t.Fatalf("isolated test schemas rejected: %v", err)
	}
}

func TestLoadTestBuildsUniqueDuplicateConflictAndInsufficientCases(t *testing.T) {
	o := options{requests: 100, duplicatePercent: 20, conflictPercent: 20, insufficientPercent: 20, prefix: "test-run", amount: 25}
	jobs, seeds := makeWork(o, 1000)
	if len(jobs) != 100 || len(seeds) != 20 {
		t.Fatalf("jobs=%d seeds=%d", len(jobs), len(seeds))
	}
	counts := map[string]int{}
	for _, job := range jobs {
		counts[job.kind]++
		if job.kind == "insufficient" && job.amount != 1001 {
			t.Fatalf("insufficient amount=%d", job.amount)
		}
	}
	if counts["unique"] != 40 || counts["duplicate"] != 20 || counts["conflict"] != 20 || counts["insufficient"] != 20 {
		t.Fatalf("scenario counts=%v", counts)
	}
	if got := percentile([]time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}, 50); got != 2*time.Millisecond {
		t.Fatalf("median=%s", got)
	}
}
