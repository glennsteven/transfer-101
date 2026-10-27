package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *trackedBody) Close() error { b.closed = true; return nil }

func providerTransfer() Transfer {
	return Transfer{ID: "trf_client", ProviderAccountID: "bank_a", ProviderReference: "prv_existing", Amount: 150000, Currency: "IDR", BankCode: "001", AccountNumber: "0012345"}
}

func testProviderClient(t *testing.T) *ProviderClient {
	t.Helper()
	c, err := NewProviderClient("http://127.0.0.1:8081", []ProviderCredential{{"bank_a", "bank-a-secret-token"}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func operationJSON(t *testing.T, status, reason string) []byte {
	t.Helper()
	tx := providerTransfer()
	p := providerOperation{providerPayload: providerPayload{tx.ProviderReference, tx.Amount, tx.Currency, tx.BankCode, tx.AccountNumber}, OperationID: "bank_effective", AccountID: tx.ProviderAccountID, Status: status, ReasonCode: reason}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProviderResultContractAndBodyClosure(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason, want string
		httpStatus                 int
		mutate                     func([]byte) []byte
	}{
		{name: "success", status: "SUCCEEDED", want: "FINAL_SUCCESS", httpStatus: 200},
		{name: "definitive rejection", status: "REJECTED", reason: "DEMO_REJECTED", want: "FINAL_REJECTION", httpStatus: 200},
		{name: "accepted is nonfinal", status: "ACCEPTED", want: "AMBIGUOUS", httpStatus: 200},
		{name: "504 despite successful body", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 504},
		{name: "500 despite successful body", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 500},
		{name: "provider conflict", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 409},
		{name: "unauthorized", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 401},
		{name: "unknown status", status: "UNKNOWN", want: "AMBIGUOUS", httpStatus: 200},
		{name: "rejection without reason", status: "REJECTED", want: "AMBIGUOUS", httpStatus: 200},
		{name: "success with rejection reason", status: "SUCCEEDED", reason: "BAD", want: "AMBIGUOUS", httpStatus: 200},
		{name: "malformed", want: "AMBIGUOUS", httpStatus: 200, mutate: func([]byte) []byte { return []byte("{") }},
		{name: "trailing JSON", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 200, mutate: func(b []byte) []byte { return append(b, []byte(" {}")...) }},
		{name: "wrong reference", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 200, mutate: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("prv_existing"), []byte("prv_other")) }},
		{name: "wrong account scope", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 200, mutate: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("bank_a"), []byte("bank_b")) }},
		{name: "wrong amount", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 200, mutate: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("150000"), []byte("150001")) }},
		{name: "wrong currency", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 200, mutate: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("IDR"), []byte("USD")) }},
		{name: "wrong bank", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 200, mutate: func(b []byte) []byte {
			return bytes.ReplaceAll(b, []byte(`"bank_code":"001"`), []byte(`"bank_code":"002"`))
		}},
		{name: "wrong number", status: "SUCCEEDED", want: "AMBIGUOUS", httpStatus: 200, mutate: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("0012345"), []byte("9912345")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testProviderClient(t)
			data := operationJSON(t, tc.status, tc.reason)
			if tc.mutate != nil {
				data = tc.mutate(data)
			}
			body := &trackedBody{Reader: bytes.NewReader(data)}
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer bank-a-secret-token" || r.Method != "POST" || r.URL.Path != "/v1/transfers" {
					t.Fatal("wrong account/endpoint")
				}
				var sent providerPayload
				if json.NewDecoder(r.Body).Decode(&sent) != nil || sent.Reference != "prv_existing" {
					t.Fatal("existing provider reference was not sent")
				}
				return &http.Response{StatusCode: tc.httpStatus, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body, ContentLength: -1}, nil
			})
			result := c.submit(t.Context(), providerTransfer())
			if result.outcome != tc.want || !body.closed {
				t.Fatalf("outcome=%s wanted=%s body_closed=%v", result.outcome, tc.want, body.closed)
			}
		})
	}
}

func TestProviderResponseSizeLimitAndRedirect(t *testing.T) {
	for _, declared := range []int64{-1, maxProviderBody + 1} {
		c := testProviderClient(t)
		body := &trackedBody{Reader: strings.NewReader(strings.Repeat("x", maxProviderBody*3))}
		c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body, ContentLength: declared}, nil
		})
		if c.submit(t.Context(), providerTransfer()).outcome != "AMBIGUOUS" || !body.closed || body.read > maxProviderBody+1 {
			t.Fatalf("unbounded read or open body: read=%d closed=%v", body.read, body.closed)
		}
	}
	c := testProviderClient(t)
	calls := 0
	body := &trackedBody{Reader: strings.NewReader("")}
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1:9999/other"}}, Body: body}, nil
	})
	if c.submit(t.Context(), providerTransfer()).outcome != "AMBIGUOUS" || calls != 1 || !body.closed {
		t.Fatal("redirect followed or body not closed")
	}
}

func TestProviderDeadlineAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testProviderClient(t)
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
		if result := c.submit(t.Context(), providerTransfer()); result.reason != "HTTP_TIMEOUT" || result.outcome != "AMBIGUOUS" {
			t.Fatalf("timeout not nonfinal: %+v", result)
		}
	})
	c := testProviderClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	result := make(chan providerResult, 1)
	go func() { result <- c.submit(ctx, providerTransfer()) }()
	<-entered
	cancel()
	if r := <-result; r.reason != "HTTP_CANCELED" || r.outcome != "AMBIGUOUS" {
		t.Fatalf("cancellation not propagated: %+v", r)
	}
}

func TestProviderConfigurationAndCredentials(t *testing.T) {
	credentials := []ProviderCredential{{"bank_a", "bank-a-secret-token"}}
	for _, u := range []string{"https://bank.example", "http://bank.example", "http://user:secret@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?secret=value", "http://127.0.0.1#fragment", ""} {
		if _, err := NewProviderClient(u, credentials, time.Second); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid URL accepted or exposed: %v", err)
		}
	}
	file := filepath.Join(t.TempDir(), "provider.json")
	for _, data := range []string{`[]`, `null`, `{}`, `[{"provider_account_id":"bank_a","token":"short"}]`, `[{"provider_account_id":"bank_a","token":"bank-a-secret-token","unknown":true}]`, strings.Repeat("x", 65537)} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProviderCredentials(file); err == nil {
			t.Fatal("invalid credential file accepted")
		}
	}
}
