package mockbank

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testCredentials() []Credential {
	return []Credential{{"bank_a", "bank-a-secret-token"}, {"bank_b", "bank-b-secret-token"}}
}

func TestAuthenticationAndInputBeforeStorage(t *testing.T) {
	h, err := NewHandler(nil, testCredentials(), Options{Scenario: "success", DBTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body, token, content string
		want                 int
	}{
		{`{}`, "", "application/json", 401},
		{`{}`, "wrong-token", "application/json", 401},
		{`{}`, "bank-a-secret-token", "text/plain", 415},
		{`{}`, "bank-a-secret-token", "application/json", 400},
		{`{"provider_reference":"p","amount":1.1,"currency":"IDR","bank_code":"001","account_number":"001"}`, "bank-a-secret-token", "application/json", 400},
		{`{"provider_reference":"p","amount":1,"currency":"IDR","bank_code":"001","account_number":"001","provider_account_id":"bank_b"}`, "bank-a-secret-token", "application/json", 400},
		{strings.Repeat(" ", 4097), "bank-a-secret-token", "application/json", 413},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/transfers", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
	}
}

func TestOptionsAndCredentialValidation(t *testing.T) {
	for _, o := range []Options{{Scenario: "unknown", DBTimeout: time.Second}, {Scenario: "success"}, {Scenario: "success_timeout", DBTimeout: time.Second}, {Scenario: "success", DBTimeout: time.Second, ResponseDelay: 2 * time.Minute}} {
		if ValidateOptions(o) == nil {
			t.Fatal("invalid options accepted")
		}
	}
	for _, c := range [][]Credential{nil, {{"bank_a", "short"}}, {{"bank_a", "bank-a-secret-token"}, {"bank_a", "bank-b-secret-token"}}, {{"bank_a", "bank-a-secret-token"}, {"bank_b", "bank-a-secret-token"}}} {
		if validateCredentials(c) == nil {
			t.Fatal("invalid account credentials accepted")
		}
	}
}
