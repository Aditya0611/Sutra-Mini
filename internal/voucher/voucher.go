package voucher

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("voucher not found")

type Voucher struct {
	ID          int64     `json:"id"`
	Type        string    `json:"type"`
	SourceID    int64     `json:"source_id"`
	AmountPaise int64     `json:"amount_paise"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	LastError   *string   `json:"last_error"`
	ClientRef   string    `json:"client_ref"`
	ExternalRef *string   `json:"external_ref"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "pushed", "failed":
	default:
		writeErr(w, http.StatusBadRequest, "status must be pending, pushed, or failed")
		return
	}
	items, err := List(r.Context(), h.pool, status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list vouchers")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vouchers": items})
}

func (h *Handler) Retry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid voucher id")
		return
	}
	item, err := Retry(r.Context(), h.pool, id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func List(ctx context.Context, pool *pgxpool.Pool, status string) ([]Voucher, error) {
	query := `
		SELECT id, type, source_id, amount_paise, status, attempts,
		       last_error, client_ref, external_ref, created_at, updated_at
		FROM vouchers
	`
	args := []any{}
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY id`
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Voucher{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Retry queues a failed voucher again. Attempts go back to zero so the worker
// gives it a fresh set of tries. A pushed voucher is left alone.
func Retry(ctx context.Context, pool *pgxpool.Pool, id int64) (Voucher, error) {
	row := pool.QueryRow(ctx, `
		UPDATE vouchers
		SET status = 'pending',
		    attempts = 0,
		    last_error = NULL,
		    lease_until = NULL,
		    next_attempt_at = NULL
		WHERE id = $1
		  AND status = 'failed'
		RETURNING id, type, source_id, amount_paise, status, attempts,
		          last_error, client_ref, external_ref, created_at, updated_at
	`, id)
	item, err := scan(row)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Voucher{}, err
	}
	current, err := load(ctx, pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Voucher{}, ErrNotFound
	}
	if err != nil {
		return Voucher{}, err
	}
	if current.Status == "pushed" {
		return Voucher{}, errors.New("voucher is already pushed")
	}
	return current, nil
}

func load(ctx context.Context, pool *pgxpool.Pool, id int64) (Voucher, error) {
	row := pool.QueryRow(ctx, `
		SELECT id, type, source_id, amount_paise, status, attempts,
		       last_error, client_ref, external_ref, created_at, updated_at
		FROM vouchers
		WHERE id = $1
	`, id)
	return scan(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Voucher, error) {
	var item Voucher
	err := row.Scan(
		&item.ID, &item.Type, &item.SourceID, &item.AmountPaise, &item.Status, &item.Attempts,
		&item.LastError, &item.ClientRef, &item.ExternalRef, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
