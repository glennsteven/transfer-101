package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const usage = "Usage: go run ./cmd/demo-provider REFERENCE EXPECTED_APP_STATUS_OR_ANY EXPECTED_BANK_STATUS [WAIT_APP_STATUS]"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println(usage)
		return
	}
	if len(os.Args) != 4 && len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	wait := ""
	if len(os.Args) == 5 {
		wait = os.Args[4]
	}
	if err := demo(ctx, os.Args[1], os.Args[2], os.Args[3], wait); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type resource struct {
	ID                string `json:"id"`
	ProviderReference string `json:"provider_reference"`
	OperationID       string `json:"operation_id"`
	Status            string `json:"status"`
	NeedsReview       bool   `json:"needs_review"`
	ReviewReason      string `json:"review_reason"`
}

func demo(ctx context.Context, reference, expectedApp, expectedBank, wait string) error {
	api := strings.TrimRight(env("DEMO_API_URL", "http://127.0.0.1:8080"), "/")
	bank := strings.TrimRight(env("DEMO_BANK_URL", "http://127.0.0.1:8081"), "/")
	for _, origin := range []string{api, bank} {
		u, err := url.Parse(origin)
		if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("demo URLs must be HTTP origins on loopback")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("demo only targets local services")
		}
	}
	merchantToken := env("DEMO_MERCHANT_TOKEN", "demo-merchant-a-token")
	bankToken := env("DEMO_BANK_TOKEN", "demo-bank-account-a-token")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Timeout: 40 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	call := func(method, target, token string, payload any) (resource, error) {
		var body io.Reader
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				return resource{}, err
			}
			body = bytes.NewReader(data)
		}
		r, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return resource{}, errors.New("cannot create demo request")
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			return resource{}, errors.New("demo HTTP request failed; check running local services")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 201 {
			return resource{}, fmt.Errorf("demo %s returned HTTP %d", method, response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
		if err != nil || len(data) > 8192 {
			return resource{}, errors.New("invalid demo response size")
		}
		var value resource
		if json.Unmarshal(data, &value) != nil {
			return resource{}, errors.New("invalid demo JSON")
		}
		return value, nil
	}
	payload := map[string]any{"reference_id": reference, "amount": 150000, "bank_code": "001", "account_number": "00123456789"}
	created, err := call("POST", api+"/transfers", merchantToken, payload)
	if err != nil {
		return err
	}
	if created.ID == "" || created.ProviderReference == "" {
		return errors.New("create response missing identity")
	}
	duplicate, err := call("POST", api+"/transfers", merchantToken, payload)
	if err != nil {
		return err
	}
	if duplicate.ID != created.ID || duplicate.ProviderReference != created.ProviderReference {
		return errors.New("merchant duplicate changed identity")
	}
	dispatched, err := call("POST", api+"/transfers/"+url.PathEscape(created.ID)+"/dispatch", merchantToken, nil)
	if err != nil {
		return err
	}
	if dispatched.ID != created.ID || dispatched.ProviderReference != created.ProviderReference {
		return errors.New("dispatch changed identity")
	}
	initialStatus := dispatched.Status
	if wait != "" {
		if wait != "SUCCESS" && wait != "FAILED" {
			return errors.New("WAIT_APP_STATUS must be SUCCESS or FAILED")
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for dispatched.Status != wait {
			if dispatched.NeedsReview {
				return fmt.Errorf("manual review required: %s", dispatched.ReviewReason)
			}
			if dispatched.Status == "SUCCESS" || dispatched.Status == "FAILED" {
				return fmt.Errorf("unexpected final status: %s", dispatched.Status)
			}
			select {
			case <-ctx.Done():
				return errors.New("recovery demo deadline exceeded; check worker and durable job")
			case <-ticker.C:
			}
			dispatched, err = call("GET", api+"/transfers/"+url.PathEscape(created.ID), merchantToken, nil)
			if err != nil {
				return err
			}
			if dispatched.ID != created.ID || dispatched.ProviderReference != created.ProviderReference {
				return errors.New("recovery replaced identity")
			}
		}
	}
	inquiry, err := call("GET", bank+"/v1/transfers/"+url.PathEscape(created.ProviderReference), bankToken, nil)
	if err != nil {
		return err
	}
	bankPayload := map[string]any{"provider_reference": created.ProviderReference, "amount": 150000, "currency": "IDR", "bank_code": "001", "account_number": "00123456789"}
	bankDuplicate, err := call("POST", bank+"/v1/transfers", bankToken, bankPayload)
	if err != nil {
		return err
	}
	if inquiry.OperationID == "" || bankDuplicate.OperationID != inquiry.OperationID || bankDuplicate.Status != inquiry.Status {
		return errors.New("provider duplicate changed operation/result")
	}
	if (expectedApp != "ANY" && initialStatus != expectedApp) || inquiry.Status != expectedBank {
		return fmt.Errorf("unexpected result: app=%s bank=%s (expected %s/%s)", dispatched.Status, inquiry.Status, expectedApp, expectedBank)
	}
	fmt.Printf("transfer=%s provider_reference=%s\napp=%s bank=%s operation=%s\n", created.ID, created.ProviderReference, dispatched.Status, inquiry.Status, inquiry.OperationID)
	fmt.Println("Merchant duplicate and provider duplicate retained the existing IDs.")
	if wait != "" {
		fmt.Printf("recovered=%s initial=%s\n", dispatched.Status, initialStatus)
	}
	return nil
}
