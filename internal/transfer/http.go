package transfer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

const maxBodyBytes = 4096

type handler struct {
	db      *sql.DB
	timeout time.Duration
	logger  *slog.Logger
}

func Register(mux *http.ServeMux, db *sql.DB, credentials []Credential, timeout time.Duration, logger *slog.Logger) {
	h := handler{db: db, timeout: timeout, logger: logger}
	mux.Handle("POST /transfers", authenticate(credentials, http.HandlerFunc(h.create)))
	mux.Handle("GET /transfers/{id}", authenticate(credentials, http.HandlerFunc(h.get)))
}

func (h handler) create(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	merchant, ok := r.Context().Value(merchantContextKey{}).(Merchant)
	if !ok {
		unauthenticated(w)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "application/json required")
		return
	}
	if r.ContentLength > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "body exceeds 4 KiB")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request createRequest
	err = decoder.Decode(&request)
	if err == nil {
		err = decoder.Decode(new(any))
		if err == io.EOF {
			err = nil
		} else if err == nil {
			err = errors.New("multiple JSON values")
		}
	}
	if err != nil || !request.valid() {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "body exceeds 4 KiB")
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid transfer payload")
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	stored, created, err := create(ctx, h.db, merchant, request)
	if errors.Is(err, errPayloadConflict) {
		writeError(w, http.StatusConflict, "PAYLOAD_CONFLICT", errPayloadConflict.Error())
		return
	}
	if errors.Is(err, errInsufficientFunds) {
		writeError(w, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS", errInsufficientFunds.Error())
		return
	}
	if err != nil {
		h.unavailable(w, "create", err)
		return
	}
	h.logger.Info("transfer request stored", "transaction_id", stored.ID, "provider_reference", stored.ProviderReference,
		"operation", "create", "attempt", 0, "duration_ms", time.Since(started).Milliseconds(),
		"outcome", map[bool]string{true: "created", false: "duplicate"}[created])
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", "/transfers/"+stored.ID)
	}
	writeJSON(w, status, stored)
}

func (h handler) get(w http.ResponseWriter, r *http.Request) {
	merchant, ok := r.Context().Value(merchantContextKey{}).(Merchant)
	if !ok {
		unauthenticated(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	stored, err := get(ctx, h.db, merchant.ID, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "transfer not found")
		return
	}
	if err != nil {
		h.unavailable(w, "get", err)
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

func (h handler) unavailable(w http.ResponseWriter, operation string, err error) {
	// Driver errors can contain rejected field values, so log only the operation and type.
	h.logger.Error("transfer storage unavailable", "operation", operation, "error_type", errorType(err))
	writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "transfer storage temporarily unavailable; retry with the same reference_id and payload")
}

func errorType(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "database"
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
