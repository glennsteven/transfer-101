package main

import (
	"strings"
	"testing"
)

func TestMockBankConfiguration(t *testing.T) {
	set := func(t *testing.T) {
		t.Setenv("MOCK_BANK_HTTP_ADDR", "127.0.0.1:8081")
		t.Setenv("MOCK_BANK_DATABASE_URL", "postgres://mock_bank:secret@127.0.0.1:54331/mock_bank?sslmode=disable")
		t.Setenv("MOCK_BANK_CREDENTIALS_FILE", "config/provider-accounts.example.json")
		t.Setenv("MOCK_BANK_SCENARIO", "success")
		t.Setenv("MOCK_BANK_RESPONSE_DELAY", "5s")
	}
	set(t)
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value string }{{"MOCK_BANK_HTTP_ADDR", "0.0.0.0:8081"}, {"MOCK_BANK_HTTP_ADDR", "127.0.0.1:0"}, {"MOCK_BANK_DATABASE_URL", "postgres://transfer_demo:secret@127.0.0.1/transfer_demo"}, {"MOCK_BANK_DATABASE_URL", "postgres://mock_bank:secret@127.0.0.1/mock_bank?dbname=transfer_demo"}, {"MOCK_BANK_SCENARIO", "random"}, {"MOCK_BANK_RESPONSE_DELAY", "-1s"}, {"MOCK_BANK_CREDENTIALS_FILE", ""}} {
		t.Run(tc.key, func(t *testing.T) {
			set(t)
			t.Setenv(tc.key, tc.value)
			if _, err := loadConfig(); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid config accepted or secret leaked: %v", err)
			}
		})
	}
}
