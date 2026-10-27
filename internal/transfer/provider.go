package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const maxProviderBody = 8192

var providerReferencePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

type ProviderCredential struct {
	AccountID string `json:"provider_account_id"`
	Token     string `json:"token"`
}

func LoadProviderCredentials(path string) ([]ProviderCredential, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open provider credentials file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 {
		return nil, errors.New("cannot read provider credentials or file exceeds 64 KiB")
	}
	var credentials []ProviderCredential
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&credentials) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid provider credentials JSON")
	}
	if err := validateProviderCredentials(credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

func validateProviderCredentials(credentials []ProviderCredential) error {
	accounts, tokens := map[string]bool{}, map[string]bool{}
	for _, c := range credentials {
		if !referencePattern.MatchString(c.AccountID) || !tokenPattern.MatchString(c.Token) || accounts[c.AccountID] || tokens[c.Token] {
			return errors.New("invalid or duplicate provider account/token")
		}
		accounts[c.AccountID], tokens[c.Token] = true, true
	}
	if len(accounts) == 0 {
		return errors.New("at least one provider account is required")
	}
	return nil
}

type ProviderClient struct {
	baseURL string
	tokens  map[string]string
	timeout time.Duration
	http    *http.Client
}

func NewProviderClient(baseURL string, credentials []ProviderCredential, timeout time.Duration) (*ProviderClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("MOCK_BANK_URL must be an HTTP origin on loopback")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("MOCK_BANK_URL must target the local mock bank")
	}
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("provider timeout must be positive and at most 30s")
	}
	if err := validateProviderCredentials(credentials); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = timeout
	client := &ProviderClient{baseURL: strings.TrimRight(baseURL, "/"), tokens: make(map[string]string), timeout: timeout,
		http: &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, c := range credentials {
		client.tokens[c.AccountID] = c.Token
	}
	return client, nil
}

func (c *ProviderClient) CloseIdleConnections() { c.http.CloseIdleConnections() }

const MaxReportBytes = 8 << 20

func (c *ProviderClient) FetchReport(ctx context.Context, account, id string) ([]byte, error) {
	token, ok := c.tokens[account]
	if !ok || !providerReferencePattern.MatchString(id) {
		return nil, errors.New("invalid report account or id")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/reports/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := c.http.Do(r)
	if err != nil {
		return nil, errors.New("report HTTP request failed")
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || response.StatusCode != 200 || media != "text/csv" || response.Header.Get("X-Mock-Provider-Account") != account || response.ContentLength > MaxReportBytes {
		return nil, errors.New("invalid report HTTP response")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxReportBytes+1))
	if err != nil || len(data) > MaxReportBytes {
		return nil, errors.New("report unreadable or exceeds 8 MiB")
	}
	return data, nil
}

type providerPayload struct {
	Reference     string `json:"provider_reference"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
}

type providerOperation struct {
	providerPayload
	OperationID string `json:"operation_id"`
	AccountID   string `json:"provider_account_id"`
	Status      string `json:"status"`
	ReasonCode  string `json:"reason_code,omitempty"`
}

type providerResult struct {
	httpStatus     int
	bankStatus     string
	reason         string
	outcome        string
	evidence       json.RawMessage
	retentionEpoch string
	notFound       bool
	reviewReason   string
}

func (c *ProviderClient) submit(ctx context.Context, t Transfer) providerResult {
	return c.request(ctx, t, http.MethodPost, "", time.Time{})
}

func (c *ProviderClient) inquiry(ctx context.Context, t Transfer) providerResult {
	return c.request(ctx, t, http.MethodGet, "", time.Time{})
}

func (c *ProviderClient) submitGuarded(ctx context.Context, t Transfer, epoch string, before time.Time) providerResult {
	return c.request(ctx, t, http.MethodPost, epoch, before)
}

func (c *ProviderClient) request(ctx context.Context, t Transfer, method, epoch string, before time.Time) providerResult {
	result := providerResult{outcome: "AMBIGUOUS", reason: "TRANSPORT_ERROR"}
	token, ok := c.tokens[t.ProviderAccountID]
	if !ok {
		result.reason = "ACCOUNT_NOT_CONFIGURED"
		return result
	}
	payload := providerPayload{t.ProviderReference, t.Amount, t.Currency, t.BankCode, t.AccountNumber}
	var bodyReader io.Reader
	if method == http.MethodPost {
		data, _ := json.Marshal(payload)
		bodyReader = bytes.NewReader(data)
	}
	path := "/v1/transfers"
	if method == http.MethodGet {
		path += "/" + url.PathEscape(t.ProviderReference)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return result
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	if epoch != "" {
		request.Header.Set("X-Mock-Retention-Epoch", epoch)
		request.Header.Set("X-Mock-Resubmit-Before", before.UTC().Format(time.RFC3339Nano))
	}
	response, err := c.http.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.reason = "HTTP_TIMEOUT"
		} else if errors.Is(err, context.Canceled) {
			result.reason = "HTTP_CANCELED"
		}
		return result
	}
	defer response.Body.Close()
	result.httpStatus = response.StatusCode
	if account := response.Header.Get("X-Mock-Provider-Account"); account != "" && account != t.ProviderAccountID {
		result.reason, result.reviewReason = "PROVIDER_ACCOUNT_MISMATCH", "PROVIDER_ACCOUNT_MISMATCH"
		return result
	}
	if value := response.Header.Get("X-Mock-Retention-Epoch"); providerReferencePattern.MatchString(value) && response.Header.Get("X-Mock-Provider-Account") == t.ProviderAccountID {
		result.retentionEpoch = value
	}
	if response.StatusCode != http.StatusOK && !(method == http.MethodGet && response.StatusCode == http.StatusNotFound) {
		result.reason = "HTTP_NONFINAL"
		if response.StatusCode == http.StatusConflict {
			result.reason, result.reviewReason = "PROVIDER_PAYLOAD_CONFLICT", "PROVIDER_PAYLOAD_CONFLICT"
		}
		if response.StatusCode == http.StatusPreconditionFailed {
			result.reason, result.reviewReason = "PROVIDER_RETENTION_REJECTED", "PROVIDER_RETENTION_REJECTED"
		}
		return result
	}
	result.reason = "INVALID_PROVIDER_RESPONSE"
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || response.ContentLength > maxProviderBody {
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBody+1))
	if err != nil || len(body) > maxProviderBody {
		return result
	}
	if method == http.MethodGet && response.StatusCode == http.StatusNotFound {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if d.Decode(&failure) == nil && d.Decode(new(any)) == io.EOF && failure.Error.Code == "NOT_FOUND" {
			result.notFound, result.reason = true, "BANK_NOT_FOUND"
		}
		return result
	}
	var operation providerOperation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&operation) != nil || decoder.Decode(new(any)) != io.EOF || operation.providerPayload != payload ||
		operation.AccountID != t.ProviderAccountID || !providerReferencePattern.MatchString(operation.OperationID) {
		return result
	}
	switch operation.Status {
	case "SUCCEEDED":
		if operation.ReasonCode != "" {
			return result
		}
		result.outcome = "FINAL_SUCCESS"
	case "REJECTED":
		if !referencePattern.MatchString(operation.ReasonCode) {
			return result
		}
		result.outcome = "FINAL_REJECTION"
	case "ACCEPTED":
		if operation.ReasonCode != "" {
			return result
		}
	default:
		return result
	}
	result.bankStatus = operation.Status
	result.reason = operation.ReasonCode
	if operation.Status == "ACCEPTED" {
		result.reason = "BANK_ACCEPTED"
	}
	result.evidence, _ = json.Marshal(operation)
	return result
}
