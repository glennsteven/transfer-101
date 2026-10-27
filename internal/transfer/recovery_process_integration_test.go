//go:build integration

package transfer

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"transfer-101/internal/testdb"
)

func TestRecoveryChildProcess(t *testing.T) {
	if os.Getenv("RECOVERY_CHILD") != "1" {
		return
	}
	dsn := os.Getenv("RECOVERY_TEST_DSN")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid child URL")
	}
	q := u.Query()
	schema := q.Get("search_path")
	q.Del("search_path")
	u.RawQuery = q.Encode()
	if _, err := testdb.ValidateURL(u.String()); err != nil || !strings.HasPrefix(schema, "transfer_test_") {
		t.Fatal("child requires dedicated fixture database")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := NewProviderClient(os.Getenv("RECOVERY_TEST_BANK"), []ProviderCredential{{"bank_a", "bank-a-secret-token"}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	phase := os.Getenv("RECOVERY_TEST_PHASE")
	barrier := func() { fmt.Println("RECOVERY_BARRIER"); <-ctx.Done() }
	opts := DefaultRecoveryOptions()
	opts.DBTimeout = 5 * time.Second
	opts.Poll = time.Hour / 60
	switch phase {
	case "claim":
		opts.afterClaim = barrier
	case "http":
		opts.afterHTTP = barrier
	case "finish":
		opts.afterFinish = barrier
	}
	w, err := NewRecoveryWorker(db, c, opts)
	if err != nil {
		t.Fatal(err)
	}
	if phase == "before_claim" {
		barrier()
	}
	if err := w.Run(ctx, testLogger()); err != nil {
		t.Fatal(err)
	}
}

type recoveryProcess struct {
	cmd     *exec.Cmd
	ready   chan struct{}
	done    chan error
	stopped sync.Once
}

func startRecoveryProcess(t *testing.T, dsn, bank, phase string) *recoveryProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryChildProcess$", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "RECOVERY_CHILD=1", "RECOVERY_TEST_DSN="+dsn, "RECOVERY_TEST_BANK="+bank, "RECOVERY_TEST_PHASE="+phase)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &recoveryProcess{cmd: cmd, ready: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			if scanner.Text() == "RECOVERY_BARRIER" {
				close(p.ready)
			}
		}
	}()
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() { p.stop(t, true) })
	return p
}

func (p *recoveryProcess) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.ready:
	case err := <-p.done:
		p.stopped.Do(func() {})
		t.Fatalf("worker exited before barrier: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("worker barrier deadline exceeded")
	}
}

func (p *recoveryProcess) stop(t *testing.T, kill bool) {
	t.Helper()
	p.stopped.Do(func() {
		if kill {
			_ = p.cmd.Process.Kill()
		} else {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case err := <-p.done:
			if kill {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Errorf("expected SIGKILL exit, got %v", err)
				} else if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Errorf("process was not killed by SIGKILL: %v", err)
				}
			}
			if !kill && err != nil {
				t.Errorf("worker shutdown: %v", err)
			}
			if kill && err == nil {
				t.Error("crash process exited normally instead of being killed")
			}
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
			t.Error("worker did not terminate")
		}
	})
}

func TestPostgresRecoverySIGKILLAndRestartAtCommitBoundaries(t *testing.T) {
	for _, phase := range []string{"before_claim", "claim", "bank_before_response", "http", "finish"} {
		t.Run(phase, func(t *testing.T) {
			f := postgres(t, true)
			bankDB, _ := testdb.MockBank(t)
			bank := mockHandler(t, bankDB, "success")
			committed, requestFinished := make(chan struct{}), make(chan struct{})
			var posts atomic.Int32
			var once sync.Once
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts.Add(1)
				}
				if phase == "bank_before_response" && r.Method == "POST" {
					recorded := httptest.NewRecorder()
					bank.ServeHTTP(recorded, r)
					if recorded.Code != 200 {
						t.Errorf("bank response before crash=%d", recorded.Code)
					}
					once.Do(func() { close(committed) })
					<-r.Context().Done()
					close(requestFinished)
					return
				}
				bank.ServeHTTP(w, r)
			})
			server := httptest.NewServer(h)
			defer server.Close()
			created := newDispatchTransfer(t, f)
			first := startRecoveryProcess(t, f.dsn, server.URL, phase)
			if phase == "bank_before_response" {
				select {
				case <-committed:
				case <-time.After(15 * time.Second):
					t.Fatal("bank commit not reached")
				}
			} else {
				first.wait(t)
			}
			if phase == "before_claim" {
				assertStatus(t, f, created.ID, "PENDING", false)
			} else if phase == "finish" {
				assertStatus(t, f, created.ID, "SUCCESS", false)
			} else {
				assertStatus(t, f, created.ID, "PROCESSING", false)
			}
			first.stop(t, true)
			if phase == "before_claim" || phase == "claim" {
				bankCount(t, bankDB, 0)
			} else {
				bankCount(t, bankDB, 1)
			}
			if phase == "bank_before_response" {
				select {
				case <-requestFinished:
				case <-time.After(10 * time.Second):
					t.Fatal("killed worker HTTP connection remained active")
				}
			}
			if phase == "finish" {
				c := networkProvider(t, bank)
				if did, err := recoveryWorker(t, f, c).RunOnce(t.Context()); err != nil || did {
					t.Fatal("committed final job repeated")
				}
			} else {
				due(t, f.db, created.ID)
				second := startRecoveryProcess(t, f.dsn, server.URL, "finish")
				second.wait(t)
				second.stop(t, false)
			}
			got := assertStatus(t, f, created.ID, "SUCCESS", false)
			if got.ProviderReference != created.ProviderReference {
				t.Fatal("restart replaced reference")
			}
			bankCount(t, bankDB, 1)
			if posts.Load() != 1 {
				t.Fatalf("unnecessary bank submit across restart: %d", posts.Load())
			}
			var active int
			if err := f.db.QueryRowContext(t.Context(), "SELECT count(*) FROM transfers WHERE next_attempt_at IS NOT NULL OR lease_token IS NOT NULL").Scan(&active); err != nil || active != 0 {
				t.Fatal("final job still active")
			}
			t.Logf("SIGKILL at %s: durable work recovered, original reference, one effective bank operation", phase)
		})
	}
}
