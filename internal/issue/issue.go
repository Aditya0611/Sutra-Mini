package issue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Issue struct {
	ID          int64 `json:"id"`
	Issued      int   `json:"issued"`
	Received    int   `json:"received"`
	Outstanding int   `json:"outstanding"`
}

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid issue id")
		return
	}
	item, err := Load(r.Context(), h.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "issue not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load issue")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func Load(ctx context.Context, pool *pgxpool.Pool, id int64) (Issue, error) {
	var item Issue
	err := pool.QueryRow(ctx, `
		SELECT id, qty, received_qty, qty - received_qty
		FROM jobwork_issues
		WHERE id = $1
	`, id).Scan(&item.ID, &item.Issued, &item.Received, &item.Outstanding)
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
