package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestLivenessDoesNotCallDatabase(t *testing.T) {
	handler := NewHandler(func(context.Context) error {
		t.Fatal("liveness must not call the database")
		return nil
	}, time.Second)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if w.Code != http.StatusOK || w.Body.String() != "{\"status\":\"alive\"}\n" {
		t.Fatalf("unexpected liveness: %d %s", w.Code, w.Body.String())
	}
}

func TestReadinessTracksDatabase(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(map[bool]string{true: "available", false: "unavailable"}[available], func(t *testing.T) {
			handler := NewHandler(func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("readiness ping has no deadline")
				}
				if !available {
					return errors.New("credential-must-not-appear")
				}
				return nil
			}, time.Second)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			want := http.StatusServiceUnavailable
			if available {
				want = http.StatusOK
			}
			if w.Code != want || strings.Contains(w.Body.String(), "credential") {
				t.Fatalf("unexpected readiness: %d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("health response must be JSON and must not be cached")
			}
		})
	}
}

func TestReadinessDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		handler := NewHandler(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}, 2*time.Second)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != http.StatusServiceUnavailable || time.Since(start) != 2*time.Second {
			t.Fatalf("deadline not enforced: status=%d elapsed=%s", w.Code, time.Since(start))
		}
	})
}

func TestReadinessPropagatesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := NewHandler(func(ctx context.Context) error {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("request cancellation not propagated")
		}
		return ctx.Err()
	}, time.Second)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", w.Code)
	}
}

func TestRoutesLimitMethodsAndPaths(t *testing.T) {
	handler := NewHandler(func(context.Context) error { return nil }, time.Second)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/readyz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodPost, "/v1/transfers", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}
