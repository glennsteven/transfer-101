package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9.-]{1,32}$`)

type options struct {
	baseURL, token, merchant, providerAccount, prefix string
	databaseURL, bankDatabaseURL                      string
	requests, concurrency, duplicatePercent           int
	conflictPercent, insufficientPercent              int
	allowTestDatabase                                 bool
	amount                                            int64
	timeout, duration, completionTimeout              time.Duration
}
type apiTransfer struct {
	ID                string `json:"id"`
	ProviderReference string `json:"provider_reference"`
}
type task struct {
	reference string
	amount    int64
	kind      string
	started   time.Time
}
type result struct {
	task
	status   int
	elapsed  time.Duration
	err      error
	transfer apiTransfer
}
type transferRow struct {
	ID, Reference, ProviderReference, Status, FundsState string
	Amount                                               int64
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func readOptions() options {
	var o options
	flag.StringVar(&o.baseURL, "base-url", env("DEMO_API_URL", "http://127.0.0.1:8080"), "local API origin")
	flag.StringVar(&o.token, "token", env("DEMO_MERCHANT_TOKEN", ""), "merchant bearer token")
	flag.StringVar(&o.merchant, "merchant", "merchant_a", "configured merchant ID")
	flag.StringVar(&o.providerAccount, "provider-account", "bank_account_demo_a", "configured mock bank account")
	flag.StringVar(&o.prefix, "prefix", "", "unique run prefix (generated when empty)")
	flag.StringVar(&o.databaseURL, "database-url", os.Getenv("DATABASE_URL"), "local application PostgreSQL URL")
	flag.StringVar(&o.bankDatabaseURL, "bank-database-url", os.Getenv("MOCK_BANK_DATABASE_URL"), "local mock bank PostgreSQL URL")
	flag.BoolVar(&o.allowTestDatabase, "allow-test-database", false, "allow only isolated loadtest_* schemas in transfer_test")
	flag.IntVar(&o.requests, "requests", 200, "maximum API requests")
	flag.IntVar(&o.concurrency, "concurrency", 16, "maximum concurrent requests")
	flag.IntVar(&o.duplicatePercent, "duplicate-percent", 30, "request share using duplicate keys")
	flag.IntVar(&o.conflictPercent, "conflict-percent", 0, "share using pre-seeded keys with changed amount")
	flag.IntVar(&o.insufficientPercent, "insufficient-percent", 0, "share using amount above starting available funds")
	flag.Int64Var(&o.amount, "amount", 1000, "normal transfer amount in whole IDR")
	flag.DurationVar(&o.timeout, "http-timeout", 3*time.Second, "per-request HTTP timeout")
	flag.DurationVar(&o.duration, "duration", 20*time.Second, "maximum traffic duration")
	flag.DurationVar(&o.completionTimeout, "completion-timeout", 30*time.Second, "maximum wait for finality")
	flag.Parse()
	if o.token == "" {
		o.token = "demo-merchant-a-token"
	}
	return o
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validate(o *options) error {
	if o.requests < 1 || o.requests > 100000 || o.concurrency < 1 || o.concurrency > 256 || o.amount < 1 ||
		o.timeout <= 0 || o.timeout > time.Minute || o.duration <= 0 || o.duration > 10*time.Minute ||
		o.completionTimeout <= 0 || o.completionTimeout > 10*time.Minute ||
		o.duplicatePercent < 0 || o.conflictPercent < 0 || o.insufficientPercent < 0 ||
		o.duplicatePercent+o.conflictPercent+o.insufficientPercent > 90 {
		return errors.New("invalid limits; requests<=100000, concurrency<=256, duration<=10m, scenario percentages<=90")
	}
	if o.token == "" || o.merchant == "" || o.providerAccount == "" || o.databaseURL == "" || o.bankDatabaseURL == "" {
		return errors.New("token, merchant, provider account, DATABASE_URL and MOCK_BANK_DATABASE_URL are required")
	}
	if o.conflictPercent > 0 && o.amount == math.MaxInt64 {
		return errors.New("amount must leave room for a conflict payload")
	}
	if o.prefix == "" {
		o.prefix = fmt.Sprintf("load-%s-%d", time.Now().UTC().Format("20060102T150405"), os.Getpid())
	}
	if !prefixPattern.MatchString(o.prefix) {
		return errors.New("prefix must contain ASCII letters, digits, dot, or dash (max 32 chars)")
	}
	u, err := url.Parse(o.baseURL)
	if err != nil || u.Scheme != "http" || !isLoopback(u.Hostname()) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("base URL must be an HTTP origin on loopback")
	}
	var schemaPaths []string
	for _, raw := range []string{o.databaseURL, o.bankDatabaseURL} {
		dbURL, parseErr := url.Parse(raw)
		if parseErr != nil || (dbURL.Scheme != "postgres" && dbURL.Scheme != "postgresql") || !isLoopback(dbURL.Hostname()) || dbURL.User == nil || dbURL.Path == "" || dbURL.Path == "/" {
			return errors.New("load test only accepts local PostgreSQL TCP URLs")
		}
		path := dbURL.Query().Get("search_path")
		database := strings.TrimPrefix(dbURL.Path, "/")
		if database == "transfer_test" && (!o.allowTestDatabase || !regexp.MustCompile(`^loadtest_[a-z0-9]+$`).MatchString(path)) {
			return errors.New("transfer_test requires -allow-test-database and a loadtest_* search_path")
		}
		schemaPaths = append(schemaPaths, strings.ToLower(dbURL.Host)+"/"+database+"/"+path)
	}
	if schemaPaths[0] == schemaPaths[1] {
		return errors.New("application and mock bank must use separate local PostgreSQL database/schema scopes")
	}
	return nil
}

func openDB(ctx context.Context, raw string) (*sql.DB, error) {
	db, err := sql.Open("postgres", raw)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(check); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func create(ctx context.Context, client *http.Client, o options, work task) result {
	start := time.Now()
	body, _ := json.Marshal(map[string]any{"reference_id": work.reference, "amount": work.amount, "bank_code": "001", "account_number": "00123456789"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.baseURL, "/")+"/transfers", bytes.NewReader(body))
	if err != nil {
		return result{task: work, elapsed: time.Since(start), err: err}
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return result{task: work, elapsed: time.Since(start), err: err}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024))
	out := result{task: work, status: response.StatusCode, elapsed: time.Since(start)}
	if readErr != nil {
		out.err = readErr
		return out
	}
	if response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusOK {
		out.err = json.Unmarshal(data, &out.transfer)
		if out.err == nil && (out.transfer.ID == "" || out.transfer.ProviderReference == "") {
			out.err = errors.New("API response omitted transfer identity")
		}
	} else if (work.kind == "conflict" && response.StatusCode != http.StatusConflict) ||
		(work.kind == "insufficient" && response.StatusCode != http.StatusUnprocessableEntity) ||
		((work.kind == "unique" || work.kind == "duplicate") && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK) {
		out.err = fmt.Errorf("unexpected HTTP %d", response.StatusCode)
	}
	return out
}

func makeWork(o options, available int64) ([]task, []task) {
	conflicts, duplicates, insufficient := o.requests*o.conflictPercent/100, o.requests*o.duplicatePercent/100, o.requests*o.insufficientPercent/100
	duplicateKeys := max(1, duplicates/4)
	seeds := make([]task, conflicts)
	for i := range conflicts {
		ref := fmt.Sprintf("%s-c-%d", o.prefix, i)
		seeds[i] = task{reference: ref, amount: o.amount, kind: "conflict_seed"}
	}
	jobs := make([]task, 0, o.requests)
	for i := range o.requests {
		var ref, kind string
		amount := o.amount
		switch {
		case i < conflicts:
			ref, amount, kind = fmt.Sprintf("%s-c-%d", o.prefix, i), o.amount+1, "conflict"
		case i < conflicts+duplicates:
			ref, kind = fmt.Sprintf("%s-d-%d", o.prefix, (i-conflicts)%duplicateKeys), "duplicate"
		case i < conflicts+duplicates+insufficient:
			ref, kind, amount = fmt.Sprintf("%s-i-%d", o.prefix, i-conflicts-duplicates), "insufficient", available+1
		default:
			ref, kind = fmt.Sprintf("%s-u-%d", o.prefix, i-conflicts-duplicates-insufficient), "unique"
		}
		jobs = append(jobs, task{reference: ref, amount: amount, kind: kind})
	}
	return jobs, seeds
}

func runTraffic(ctx context.Context, client *http.Client, o options, jobs []task) []result {
	queue, out := make(chan task), make(chan result, len(jobs))
	var workers sync.WaitGroup
	for range o.concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case work, ok := <-queue:
					if !ok {
						return
					}
					out <- create(ctx, client, o, work)
				}
			}
		}()
	}
	go func() {
		defer close(queue)
		for _, work := range jobs {
			select {
			case <-ctx.Done():
				return
			case queue <- work:
			}
		}
	}()
	go func() { workers.Wait(); close(out) }()
	var results []result
	for item := range out {
		results = append(results, item)
	}
	return results
}

func loadRows(ctx context.Context, db *sql.DB, merchant, prefix string) ([]transferRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,reference_id,provider_reference,status,funds_state,amount FROM transfers WHERE merchant_id=$1 AND left(reference_id,length($2))=$2 ORDER BY reference_id`, merchant, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []transferRow
	for rows.Next() {
		var row transferRow
		if err := rows.Scan(&row.ID, &row.Reference, &row.ProviderReference, &row.Status, &row.FundsState, &row.Amount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func finalCount(rows []transferRow) int {
	n := 0
	for _, row := range rows {
		if row.Status == "SUCCESS" || row.Status == "FAILED" {
			n++
		}
	}
	return n
}

func waitForFinal(ctx context.Context, db *sql.DB, merchant, prefix string, limit time.Duration) ([]transferRow, time.Duration, error) {
	start := time.Now()
	deadline := time.NewTimer(limit)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		rows, err := loadRows(ctx, db, merchant, prefix)
		if err != nil {
			return rows, time.Since(start), err
		}
		if finalCount(rows) == len(rows) {
			return rows, time.Since(start), nil
		}
		select {
		case <-ctx.Done():
			return rows, time.Since(start), ctx.Err()
		case <-deadline.C:
			return rows, time.Since(start), context.DeadlineExceeded
		case <-ticker.C:
		}
	}
}

func percentile(data []time.Duration, p int) time.Duration {
	if len(data) == 0 {
		return 0
	}
	sort.Slice(data, func(i, j int) bool { return data[i] < data[j] })
	idx := (p*len(data)+99)/100 - 1
	return data[max(0, idx)]
}
func latency(name string, values []time.Duration) {
	copyValues := append([]time.Duration(nil), values...)
	fmt.Printf("%s_ms p50=%d p95=%d p99=%d n=%d\n", name, percentile(copyValues, 50).Milliseconds(), percentile(copyValues, 95).Milliseconds(), percentile(copyValues, 99).Milliseconds(), len(values))
}

func verify(ctx context.Context, app, bank *sql.DB, o options, rows []transferRow) error {
	var count, refs, providerRefs int
	if err := app.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT reference_id),count(DISTINCT provider_reference) FROM transfers WHERE merchant_id=$1 AND left(reference_id,length($2))=$2`, o.merchant, o.prefix).Scan(&count, &refs, &providerRefs); err != nil {
		return err
	}
	if count != refs || count != providerRefs || count != len(rows) {
		return fmt.Errorf("local uniqueness failed: rows=%d references=%d provider_references=%d", count, refs, providerRefs)
	}
	refsList := make([]string, 0, len(rows))
	finalRows := 0
	for _, row := range rows {
		refsList = append(refsList, row.ProviderReference)
		var reserve, capture, release int
		var reserveAmount, captureAmount, releaseAmount int64
		if err := app.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE kind='RESERVE'),count(*) FILTER(WHERE kind='CAPTURE'),count(*) FILTER(WHERE kind='RELEASE'),
			COALESCE(max(amount) FILTER(WHERE kind='RESERVE'),0),COALESCE(max(amount) FILTER(WHERE kind='CAPTURE'),0),COALESCE(max(amount) FILTER(WHERE kind='RELEASE'),0)
			FROM fund_movements WHERE transfer_id=$1`, row.ID).Scan(&reserve, &capture, &release, &reserveAmount, &captureAmount, &releaseAmount); err != nil {
			return err
		}
		if reserve != 1 || reserveAmount != row.Amount || capture > 1 || release > 1 || capture+release > 1 ||
			(capture == 1 && captureAmount != row.Amount) || (release == 1 && releaseAmount != row.Amount) {
			return fmt.Errorf("movement uniqueness failed for %s", row.Reference)
		}
		if row.Status == "SUCCESS" && (capture != 1 || release != 0 || row.FundsState != "CAPTURED") {
			return fmt.Errorf("SUCCESS funds mismatch for %s", row.Reference)
		}
		if row.Status == "FAILED" && (release != 1 || capture != 0 || row.FundsState != "RELEASED") {
			return fmt.Errorf("FAILED funds mismatch for %s", row.Reference)
		}
		if row.Status == "SUCCESS" || row.Status == "FAILED" {
			finalRows++
		}
	}
	if len(refsList) > 0 {
		var ops, opRefs int
		if err := bank.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT provider_reference) FROM bank_operations WHERE provider_account_id=$1 AND provider_reference=ANY($2)`, o.providerAccount, pq.Array(refsList)).Scan(&ops, &opRefs); err != nil {
			return err
		}
		if ops != opRefs || ops > count || ops < finalRows {
			return fmt.Errorf("provider uniqueness failed: operations=%d references=%d local=%d", ops, opRefs, count)
		}
	}
	var opening, available, held, captured int64
	if err := app.QueryRowContext(ctx, `SELECT f.opening_amount,f.available,f.held,COALESCE((SELECT sum(m.amount) FROM fund_movements m WHERE m.merchant_id=f.merchant_id AND m.source_account_id=f.source_account_id AND m.kind='CAPTURE'),0) FROM merchant_funds f WHERE f.merchant_id=$1`, o.merchant).Scan(&opening, &available, &held, &captured); err != nil {
		return err
	}
	if available < 0 || held < 0 || available > math.MaxInt64-held || available+held > math.MaxInt64-captured || available+held+captured != opening {
		return fmt.Errorf("fund conservation failed opening=%d available=%d held=%d captured=%d", opening, available, held, captured)
	}
	return nil
}

func main() {
	o := readOptions()
	if err := validate(&o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.duration+o.completionTimeout+30*time.Second)
	defer cancel()
	app, err := openDB(ctx, o.databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "application DB:", err)
		os.Exit(1)
	}
	defer app.Close()
	bank, err := openDB(ctx, o.bankDatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mock bank DB:", err)
		os.Exit(1)
	}
	defer bank.Close()
	var collisionCount int
	if err := app.QueryRowContext(ctx, `SELECT count(*) FROM transfers WHERE merchant_id=$1 AND left(reference_id,length($2))=$2`, o.merchant, o.prefix).Scan(&collisionCount); err != nil {
		fmt.Fprintln(os.Stderr, "check run namespace:", err)
		os.Exit(1)
	}
	if collisionCount != 0 {
		fmt.Fprintln(os.Stderr, "run prefix already exists; choose a new prefix; load data is never deleted")
		os.Exit(2)
	}
	var available int64
	if err := app.QueryRowContext(ctx, `SELECT available FROM merchant_funds WHERE merchant_id=$1`, o.merchant).Scan(&available); err != nil {
		fmt.Fprintln(os.Stderr, "initialize local demo funds first:", err)
		os.Exit(1)
	}
	var pgVersion string
	if err := app.QueryRowContext(ctx, `SELECT current_setting('server_version')`).Scan(&pgVersion); err != nil {
		fmt.Fprintln(os.Stderr, "read database version:", err)
		os.Exit(1)
	}
	if o.insufficientPercent > 0 && available == math.MaxInt64 {
		fmt.Fprintln(os.Stderr, "cannot create an above-balance test amount when available is MaxInt64")
		os.Exit(2)
	}
	jobs, seeds := makeWork(o, available)
	client := &http.Client{Timeout: o.timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for _, seed := range seeds {
		r := create(ctx, client, o, seed)
		if r.err != nil || r.status != http.StatusCreated {
			fmt.Fprintf(os.Stderr, "conflict pre-seed failed reference=%s status=%d err=%v\n", seed.reference, r.status, r.err)
			os.Exit(1)
		}
	}
	runCtx, stop := context.WithTimeout(ctx, o.duration)
	start := time.Now()
	results := runTraffic(runCtx, client, o, jobs)
	stop()
	elapsed := time.Since(start)
	statuses := map[string]int{}
	var allLatency, acceptedLatency []time.Duration
	failures := 0
	for _, r := range results {
		allLatency = append(allLatency, r.elapsed)
		statuses[strconv.Itoa(r.status)]++
		if r.err != nil {
			failures++
			fmt.Fprintf(os.Stderr, "request error scenario=%s status=%d err=%v\n", r.kind, r.status, r.err)
		}
		if r.kind == "unique" || r.kind == "duplicate" {
			if r.status == 200 || r.status == 201 {
				acceptedLatency = append(acceptedLatency, r.elapsed)
			}
		}
	}
	fmt.Printf("prefix=%s go=%s os=%s/%s postgres=%s requests=%d completed=%d concurrency=%d http_timeout=%s traffic_limit=%s elapsed=%s throughput_rps=%.2f statuses=%v errors=%d\n", o.prefix, runtime.Version(), runtime.GOOS, runtime.GOARCH, pgVersion, o.requests, len(results), o.concurrency, o.timeout, o.duration, elapsed, float64(len(results))/max(elapsed.Seconds(), .000001), statuses, failures)
	latency("api_request", allLatency)
	latency("api_accepted", acceptedLatency)
	rows, completionElapsed, completionErr := waitForFinal(ctx, app, o.merchant, o.prefix, o.completionTimeout)
	fmt.Printf("completion_observed_elapsed=%s final_transfers=%d outstanding_transfers=%d deadline=%s\n", completionElapsed, finalCount(rows), len(rows)-finalCount(rows), o.completionTimeout)
	fmt.Printf("transfer_completion_observed_ms=%d (time from traffic end until final statuses observed)\n", completionElapsed.Milliseconds())
	if err := verify(ctx, app, bank, o, rows); err != nil {
		fmt.Fprintln(os.Stderr, "correctness check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("correctness=PASS local_rows=%d provider_refs_unique=true settlement_once=true funds_conserved=true\n", len(rows))
	if len(results) != o.requests || failures > 0 || completionErr != nil || finalCount(rows) != len(rows) {
		fmt.Fprintln(os.Stderr, "run incomplete; inspect outstanding transfers and retained run data")
		os.Exit(1)
	}
}
