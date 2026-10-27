package transfer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCredentials() []Credential {
	return []Credential{
		{Token: "demo-merchant-a-token", Merchant: Merchant{ID: "merchant_a", SourceAccountID: "funds_a", ProviderAccountID: "bank_a"}},
		{Token: "demo-merchant-b-token", Merchant: Merchant{ID: "merchant_b", SourceAccountID: "funds_b", ProviderAccountID: "bank_a"}},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func request(method, path, token, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestAuthenticateUsesServerIdentity(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, ok := r.Context().Value(merchantContextKey{}).(Merchant)
		if !ok || m != testCredentials()[0].Merchant {
			t.Fatalf("wrong authenticated identity: %+v", m)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := authenticate(testCredentials(), next)
	r := request(http.MethodPost, "/transfers", testCredentials()[0].Token, `{"merchant_id":"merchant_b"}`)
	r.Header.Set("X-Merchant-ID", "merchant_b")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("valid credential rejected: %d", w.Code)
	}
	for _, headers := range [][]string{nil, {"Bearer unknown"}, {"Basic demo-merchant-a-token"}, {"Bearer"}, {"Bearer demo-merchant-a-token", "Bearer demo-merchant-b-token"}} {
		r := request(http.MethodGet, "/transfers/unknown", "", "")
		for _, value := range headers {
			r.Header.Add("Authorization", value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("unauthorized request accepted: %d", w.Code)
		}
	}
}

func TestLoadCredentials(t *testing.T) {
	valid, err := json.Marshal(testCredentials())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"valid", string(valid), true},
		{"empty", `[]`, false},
		{"null", `null`, false},
		{"malformed", `[`, false},
		{"trailing", string(valid) + `[]`, false},
		{"unknown field", strings.Replace(string(valid), `"token":`, `"unknown":`, 1), false},
		{"duplicate token", strings.ReplaceAll(string(valid), "demo-merchant-b-token", "demo-merchant-a-token"), false},
		{"duplicate merchant", strings.ReplaceAll(string(valid), "merchant_b", "merchant_a"), false},
		{"invalid account", strings.ReplaceAll(string(valid), "funds_b", "bad account"), false},
		{"short token", strings.ReplaceAll(string(valid), "demo-merchant-a-token", "short"), false},
		{"too large", strings.Repeat(" ", 65537), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "merchants.json")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadCredentials(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got error=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "demo-merchant") {
				t.Fatal("error leaks credential")
			}
			if tc.valid && len(loaded) != 2 {
				t.Fatal("expected both merchants")
			}
		})
	}
	if _, err := LoadCredentials(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing credentials file accepted")
	}
}

func TestInvalidTransferPayloads(t *testing.T) {
	base := `{"reference_id":"order-001","amount":150000,"bank_code":"001","account_number":"0012345"}`
	cases := []struct {
		name, body string
		status     int
	}{
		{"empty", ``, 400},
		{"null", `null`, 400},
		{"array", `[]`, 400},
		{"trailing", base + `{}`, 400},
		{"garbage trailing", base + `bad`, 400},
		{"merchant injection", strings.Replace(base, `"reference_id":`, `"merchant_id":"merchant_b","reference_id":`, 1), 400},
		{"source injection", strings.Replace(base, `"reference_id":`, `"source_account_id":"other","reference_id":`, 1), 400},
		{"provider injection", strings.Replace(base, `"reference_id":`, `"provider_reference":"chosen","reference_id":`, 1), 400},
		{"currency injection", strings.Replace(base, `"reference_id":`, `"currency":"USD","reference_id":`, 1), 400},
		{"zero", strings.Replace(base, "150000", "0", 1), 400},
		{"negative", strings.Replace(base, "150000", "-1", 1), 400},
		{"fraction", strings.Replace(base, "150000", "1.5", 1), 400},
		{"exponent", strings.Replace(base, "150000", "1e3", 1), 400},
		{"overflow", strings.Replace(base, "150000", "9223372036854775808", 1), 400},
		{"amount string", strings.Replace(base, "150000", `"150000"`, 1), 400},
		{"reference whitespace", strings.Replace(base, "order-001", " order-001", 1), 400},
		{"reference too long", strings.Replace(base, "order-001", strings.Repeat("a", 65), 1), 400},
		{"bank code", strings.Replace(base, `"bank_code":"001"`, `"bank_code":"01"`, 1), 400},
		{"unicode bank", strings.Replace(base, `"bank_code":"001"`, `"bank_code":"００１"`, 1), 400},
		{"account number numeric", strings.Replace(base, `"account_number":"0012345"`, `"account_number":12345`, 1), 400},
		{"account too long", strings.Replace(base, "0012345", strings.Repeat("1", 35), 1), 400},
		{"account whitespace", strings.Replace(base, "0012345", " 0012345", 1), 400},
		{"too large", base + strings.Repeat(" ", 4096), 413},
	}
	mux := http.NewServeMux()
	Register(mux, nil, testCredentials(), time.Second, testLogger())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, request(http.MethodPost, "/transfers", testCredentials()[0].Token, tc.body))
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	r := request(http.MethodPost, "/transfers", testCredentials()[0].Token, base)
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatal("wrong media type accepted")
	}
}

func TestValidIntegerAndLeadingZeros(t *testing.T) {
	r := createRequest{ReferenceID: "Order-001", Amount: math.MaxInt64, BankCode: "001", AccountNumber: "0012345"}
	if !r.valid() || !r.matches(Transfer{Amount: math.MaxInt64, BankCode: "001", AccountNumber: "0012345"}) {
		t.Fatal("valid integer or leading zeros rejected")
	}
	for _, same := range []Transfer{
		{Amount: math.MaxInt64 - 1, BankCode: "001", AccountNumber: "0012345"},
		{Amount: math.MaxInt64, BankCode: "002", AccountNumber: "0012345"},
		{Amount: math.MaxInt64, BankCode: "001", AccountNumber: "12345"},
	} {
		if r.matches(same) {
			t.Fatal("changed payload matched")
		}
	}
}

func TestMissingAuthenticatedContext(t *testing.T) {
	h := handler{logger: testLogger()}
	w := httptest.NewRecorder()
	h.get(w, httptest.NewRequest(http.MethodGet, "/transfers/id", nil).WithContext(context.Background()))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("handler trusted a request without authenticated context")
	}
}
