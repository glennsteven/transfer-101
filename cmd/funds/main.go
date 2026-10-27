package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"transfer-101/internal/transfer"
)

func run(args []string) error {
	if len(args) < 2 || (args[0] != "init" && args[0] != "show") || (args[0] == "init" && len(args) != 3) || (args[0] == "show" && len(args) != 2) {
		return errors.New("usage: funds init MERCHANT AMOUNT | funds show MERCHANT")
	}
	file := os.Getenv("MERCHANT_CREDENTIALS_FILE")
	if file == "" {
		file = "config/merchants.example.json"
	}
	credentials, err := transfer.LoadCredentials(file)
	if err != nil {
		return err
	}
	var merchant transfer.Merchant
	for _, c := range credentials {
		if c.Merchant.ID == args[1] {
			merchant = c.Merchant
		}
	}
	if merchant.ID == "" {
		return errors.New("merchant not in authenticated server configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := transfer.OpenOperatorDatabase(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	var funds transfer.Funds
	if args[0] == "init" {
		amount, parseErr := strconv.ParseInt(args[2], 10, 64)
		if parseErr != nil || amount < 0 {
			return errors.New("amount must be nonnegative integer IDR")
		}
		funds, err = transfer.InitializeFunds(ctx, db, merchant.ID, merchant.SourceAccountID, amount)
	} else {
		funds, err = transfer.ReadFunds(ctx, db, merchant.ID, merchant.SourceAccountID)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(funds)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
