package testdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Aditya0611/Sutra-Mini/internal/envfile"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if err := envfile.LoadUpwards(); err != nil {
		t.Fatal(err)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL is required")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 10
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	return pool
}

func InsertIssue(t *testing.T, pool *pgxpool.Pool, qty int) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO jobwork_issues (party_id, sku_id, qty, issued_on)
		VALUES (1, 1, $1, DATE '2026-10-01')
		RETURNING id
	`, qty).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `
			DELETE FROM vouchers
			WHERE source_id IN (SELECT id FROM jobwork_receipts WHERE issue_id = $1)
		`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM jobwork_receipts WHERE issue_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM jobwork_issues WHERE id = $1`, id)
	})
	return id
}
