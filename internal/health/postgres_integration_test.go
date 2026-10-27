//go:build integration

package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"transfer-101/internal/testdb"
)

func TestPostgresReadiness(t *testing.T) {
	db, _ := testdb.Open(t)
	handler := NewHandler(db.PingContext, time.Second)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready with live PostgreSQL: got %d", w.Code)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		code int
	}{{"/readyz", http.StatusServiceUnavailable}, {"/livez", http.StatusOK}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.code {
			t.Errorf("%s with closed database pool: got %d, want %d", tc.path, w.Code, tc.code)
		}
	}
}
