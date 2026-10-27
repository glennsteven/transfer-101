package transfer

import (
	"regexp"
	"time"
)

var (
	referencePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	bankPattern      = regexp.MustCompile(`^[0-9]{3}$`)
	accountPattern   = regexp.MustCompile(`^[0-9]{1,34}$`)
	tokenPattern     = regexp.MustCompile(`^[!-~]{16,256}$`)
)

type Transfer struct {
	ID                string    `json:"id"`
	MerchantID        string    `json:"-"`
	SourceAccountID   string    `json:"-"`
	ProviderAccountID string    `json:"-"`
	ReferenceID       string    `json:"reference_id"`
	ProviderReference string    `json:"provider_reference"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	BankCode          string    `json:"bank_code"`
	AccountNumber     string    `json:"account_number"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	NeedsReview       bool      `json:"needs_review,omitempty"`
	ReviewReason      string    `json:"review_reason,omitempty"`
	FundsState        string    `json:"funds_state"`
}

type createRequest struct {
	ReferenceID   string `json:"reference_id"`
	Amount        int64  `json:"amount"`
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
}

func (r createRequest) valid() bool {
	return referencePattern.MatchString(r.ReferenceID) && r.Amount > 0 &&
		bankPattern.MatchString(r.BankCode) && accountPattern.MatchString(r.AccountNumber)
}

func (r createRequest) matches(t Transfer) bool {
	return r.Amount == t.Amount && r.BankCode == t.BankCode && r.AccountNumber == t.AccountNumber
}
