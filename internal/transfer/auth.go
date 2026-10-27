package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

type Merchant struct {
	ID                string `json:"id"`
	SourceAccountID   string `json:"source_account_id"`
	ProviderAccountID string `json:"provider_account_id"`
}

type Credential struct {
	Token    string   `json:"token"`
	Merchant Merchant `json:"merchant"`
}

func LoadCredentials(path string) ([]Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read merchant credentials file")
	}
	if len(data) > 64*1024 {
		return nil, errors.New("merchant credentials file exceeds 64 KiB")
	}
	var credentials []Credential
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		return nil, errors.New("invalid merchant credentials JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("merchant credentials must contain one JSON array")
	}
	if err := validateCredentials(credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

func validateCredentials(credentials []Credential) error {
	if len(credentials) == 0 {
		return errors.New("at least one merchant credential is required")
	}
	ids, tokens := make(map[string]bool), make(map[string]bool)
	for _, credential := range credentials {
		m := credential.Merchant
		if !referencePattern.MatchString(m.ID) || !referencePattern.MatchString(m.SourceAccountID) ||
			!referencePattern.MatchString(m.ProviderAccountID) {
			return errors.New("merchant and account IDs must be 1-64 ASCII letters, digits, dot, underscore, or hyphen")
		}
		if !tokenPattern.MatchString(credential.Token) {
			return errors.New("merchant token must contain 16-256 visible ASCII characters without spaces")
		}
		if ids[m.ID] || tokens[credential.Token] {
			return errors.New("duplicate merchant ID or token in credentials file")
		}
		ids[m.ID], tokens[credential.Token] = true, true
	}
	return nil
}

type merchantContextKey struct{}

func authenticate(credentials []Credential, next http.Handler) http.Handler {
	merchants := make(map[[32]byte]Merchant, len(credentials))
	for _, credential := range credentials {
		merchants[sha256.Sum256([]byte(credential.Token))] = credential.Merchant
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(r.Header.Values("Authorization")) != 1 || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			unauthenticated(w)
			return
		}
		merchant, ok := merchants[sha256.Sum256([]byte(fields[1]))]
		if !ok {
			unauthenticated(w)
			return
		}
		ctx := context.WithValue(r.Context(), merchantContextKey{}, merchant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="transfer-101"`)
	writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "valid merchant credential required")
}
