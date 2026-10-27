package transfer

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

func dispatch(ctx context.Context, db *sql.DB, client *ProviderClient, merchantID, id string, dbTimeout time.Duration) (Transfer, error) {
	return dispatchWithLogger(ctx, db, client, merchantID, id, dbTimeout, slog.Default())
}

func dispatchWithLogger(ctx context.Context, db *sql.DB, client *ProviderClient, merchantID, id string, dbTimeout time.Duration, logger *slog.Logger) (Transfer, error) {
	opts := DefaultRecoveryOptions()
	opts.DBTimeout = dbTimeout
	worker, err := NewRecoveryWorker(db, client, opts)
	if err != nil {
		return Transfer{}, err
	}
	worker.logger = logger
	claimCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	work, _, err := worker.claim(claimCtx, merchantID, id)
	cancel()
	if err != nil {
		return Transfer{}, err
	}
	if work == nil {
		return get(ctx, db, merchantID, id)
	}
	started := time.Now()
	result, err := worker.perform(ctx, work)
	if err != nil {
		worker.logAttempt(work, result, started, 0, false, err)
		if errors.Is(err, errLeaseLost) {
			return get(ctx, db, merchantID, id)
		}
		return Transfer{}, err
	}
	providerDuration := time.Since(started)
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer finishCancel()
	stored, err := worker.finish(finishCtx, work, result)
	worker.logAttempt(work, result, started, providerDuration, err == nil, err)
	if errors.Is(err, errLeaseLost) {
		return get(finishCtx, db, merchantID, id)
	}
	return stored, err
}

func RegisterDispatch(mux *http.ServeMux, db *sql.DB, credentials []Credential, client *ProviderClient, timeout time.Duration, logger *slog.Logger) {
	h := handler{db: db, timeout: timeout, logger: logger}
	mux.Handle("POST /transfers/{id}/dispatch", authenticate(credentials, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		merchant := r.Context().Value(merchantContextKey{}).(Merchant)
		if r.ContentLength != 0 {
			writeError(w, 400, "INVALID_REQUEST", "dispatch does not accept a body")
			return
		}
		t, err := dispatchWithLogger(r.Context(), db, client, merchant.ID, r.PathValue("id"), timeout, logger)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "NOT_FOUND", "transfer not found")
			return
		}
		if err != nil {
			h.unavailable(w, "dispatch", err)
			return
		}
		writeJSON(w, 200, t)
	})))
}
