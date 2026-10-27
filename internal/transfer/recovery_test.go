package transfer

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRecoveryBackoffBoundsAndCap(t *testing.T) {
	options := DefaultRecoveryOptions()
	options.BaseBackoff = time.Second
	options.MaxBackoff = 8 * time.Second
	w, err := NewRecoveryWorker(nil, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2, 3, 4, 8, 1000000} {
		cap := time.Second
		for i := 1; i < n && cap < 8*time.Second; i++ {
			cap *= 2
		}
		for range 100 {
			delay := w.backoff(n)
			if delay < cap/2 || delay > cap {
				t.Fatalf("attempt=%d delay=%s cap=%s", n, delay, cap)
			}
		}
	}
	for _, sample := range []time.Duration{-time.Hour, time.Hour} {
		w.options.jitter = func(time.Duration) time.Duration { return sample }
		delay := w.backoff(1000)
		if delay < 4*time.Second || delay > 8*time.Second {
			t.Fatal("injected jitter escaped cap")
		}
	}
}

func TestInquiryNotFoundMustMatchContractAndBodyCloses(t *testing.T) {
	for _, tc := range []struct {
		body  string
		found bool
	}{{`{"error":{"code":"NOT_FOUND"}}`, true}, {`{"error":{"code":"OTHER"}}`, false}, {`{"error":{"code":"NOT_FOUND"}} {}`, false}, {`not json`, false}} {
		c := testProviderClient(t)
		body := &trackedBody{Reader: strings.NewReader(tc.body)}
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "prv_existing") {
				t.Fatal("wrong inquiry reference")
			}
			return &http.Response{StatusCode: 404, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Mock-Retention-Epoch": []string{"epoch_original"}}, Body: body, ContentLength: -1}, nil
		})
		result := c.inquiry(context.Background(), providerTransfer())
		if result.notFound != tc.found || result.outcome != "AMBIGUOUS" || !body.closed {
			t.Fatalf("not found fabricated or body leaked: %+v", result)
		}
	}
	c := testProviderClient(t)
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	if result := c.inquiry(t.Context(), providerTransfer()); result.reason != "HTTP_TIMEOUT" || result.outcome != "AMBIGUOUS" {
		t.Fatal("inquiry timeout finalized")
	}
}
