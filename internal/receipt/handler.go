package receipt

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	maxBodyBytes = 64 << 10
	maxKeyLen    = 200
)

type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > maxKeyLen {
		writeErr(w, http.StatusBadRequest, "Idempotency-Key is too long")
		return
	}
	if key == "" {
		var err error
		key, err = newKey()
		if err != nil {
			log.Printf("create receipt: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not record receipt")
			return
		}
	}

	var body struct {
		IssueID    int64  `json:"issue_id"`
		Qty        int    `json:"qty"`
		ReceivedOn string `json:"received_on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	receivedOn, err := time.Parse("2006-01-02", body.ReceivedOn)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "received_on must be YYYY-MM-DD")
		return
	}

	rec, created, err := h.store.Create(r.Context(), Input{
		IssueID:        body.IssueID,
		Qty:            body.Qty,
		ReceivedOn:     receivedOn,
		IdempotencyKey: key,
	})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, rec)
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrIssueNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrOvershoot):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrIdempotencyMismatch):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidQty), errors.Is(err, ErrInvalidDate), errors.Is(err, ErrDateBeforeIssue), errors.Is(err, ErrMissingKey):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("create receipt: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not record receipt")
	}
}

func newKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
