package receipt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Aditya0611/Sutra-Mini/internal/envfile"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) {
	if err := envfile.LoadUpwards(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestConcurrentReceiptsCannotOvershoot(t *testing.T) {
	store, pool := openStore(t)
	const issued = 5
	const attempts = 10
	issueID := insertIssue(t, pool, issued)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	okCount := 0
	overshoot := 0
	var mu sync.Mutex
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			_, _, err := store.Create(ctx, Input{
				IssueID:        issueID,
				Qty:            1,
				ReceivedOn:     time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
				IdempotencyKey: fmt.Sprintf("overshoot-%d-%d", issueID, i),
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, ErrOvershoot):
				overshoot++
			default:
				t.Errorf("receipt %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if okCount != issued || overshoot != attempts-issued {
		t.Fatalf("accepted %d and rejected %d, want %d and %d", okCount, overshoot, issued, attempts-issued)
	}
	assertTotals(t, pool, issueID, issued, issued)
}

func TestIdempotencyReplayCreatesNothing(t *testing.T) {
	store, pool := openStore(t)
	issueID := insertIssue(t, pool, 10)
	key := fmt.Sprintf("replay-%d", issueID)
	in := Input{
		IssueID:        issueID,
		Qty:            3,
		ReceivedOn:     time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: key,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const n = 8
	got := make([]Receipt, n)
	createdCount := 0
	errs := make([]error, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			rec, created, err := store.Create(ctx, in)
			mu.Lock()
			defer mu.Unlock()
			got[i] = rec
			errs[i] = err
			if created {
				createdCount++
			}
		}(i)
	}
	wg.Wait()

	if createdCount != 1 {
		t.Fatalf("created %d receipts, want 1", createdCount)
	}
	first := got[0]
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if i == 0 {
			continue
		}
		if got[i].ID != first.ID || got[i].Voucher.ID != first.Voucher.ID {
			t.Fatalf("request %d returned receipt %d voucher %d, want %d and %d", i, got[i].ID, got[i].Voucher.ID, first.ID, first.Voucher.ID)
		}
	}
	if first.Voucher.Status != "pending" || first.Voucher.AmountPaise != int64(in.Qty)*PaisePerUnit {
		t.Fatalf("voucher = %+v", first.Voucher)
	}
	assertTotals(t, pool, issueID, in.Qty, 1)
}

func openStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 15
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
	return NewStore(pool), pool
}

func insertIssue(t *testing.T, pool *pgxpool.Pool, qty int) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO jobwork_issues (party_id, sku_id, qty, issued_on)
		VALUES (1, 1, $1, DATE '2026-10-01')
		RETURNING id
	`, qty).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM vouchers
			WHERE source_id IN (SELECT id FROM jobwork_receipts WHERE issue_id = $1)
		`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM jobwork_receipts WHERE issue_id = $1`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM jobwork_issues WHERE id = $1`, id)
	})
	return id
}

func assertTotals(t *testing.T, pool *pgxpool.Pool, issueID int64, receivedQty, receiptCount int) {
	t.Helper()
	var received, receipts, vouchers int
	err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT received_qty FROM jobwork_issues WHERE id = $1),
			(SELECT count(*) FROM jobwork_receipts WHERE issue_id = $1),
			(SELECT count(*) FROM vouchers v
			   JOIN jobwork_receipts r ON r.id = v.source_id
			  WHERE r.issue_id = $1)
	`, issueID).Scan(&received, &receipts, &vouchers)
	if err != nil {
		t.Fatal(err)
	}
	if received != receivedQty || receipts != receiptCount || vouchers != receiptCount {
		t.Fatalf("received_qty=%d receipts=%d vouchers=%d, want %d, %d, %d", received, receipts, vouchers, receivedQty, receiptCount, receiptCount)
	}
}
