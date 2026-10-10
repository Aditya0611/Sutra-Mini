package sync

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Aditya0611/Sutra-Mini/internal/mockacct"
	"github.com/Aditya0611/Sutra-Mini/internal/receipt"
	"github.com/Aditya0611/Sutra-Mini/internal/testdb"
	"github.com/Aditya0611/Sutra-Mini/internal/voucher"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTwoClaimsTakeDifferentVouchers(t *testing.T) {
	pool := testdb.Open(t)
	issueID := testdb.InsertIssue(t, pool, 10)
	first := createReceipt(t, pool, issueID, "claim-a")
	second := createReceipt(t, pool, issueID, "claim-b")
	want := map[int64]bool{first.Voucher.ID: true, second.Voucher.ID: true}

	w := New(pool, Config{Lease: 30 * time.Second})
	var wg sync.WaitGroup
	got := make([]int64, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			got[i] = claimOneOf(t, pool, w, want).ID
		}(i)
	}
	wg.Wait()
	if got[0] == got[1] || !want[got[0]] || !want[got[1]] {
		t.Fatalf("claims = %v", got)
	}
	stray := claimUntilQuiet(t, pool, w, want)
	if stray {
		t.Fatal("a leased voucher was claimed again")
	}
}

func TestPushReconcilesDuplicate(t *testing.T) {
	pool := testdb.Open(t)
	issueID := testdb.InsertIssue(t, pool, 10)
	rec := createReceipt(t, pool, issueID, "dup-ref")

	mock := mockacct.New()
	mock.Remember(rec.Voucher.ClientRef, "acct_original")
	ts := httptest.NewServer(mock.Handler())
	defer ts.Close()

	w := New(pool, Config{MockURL: ts.URL, Lease: time.Minute, HTTPTimeout: 2 * time.Second})
	claimed := claimOneOf(t, pool, w, map[int64]bool{rec.Voucher.ID: true})
	if err := w.push(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	item := mustVoucher(t, pool, claimed.ID)
	if item.Status != "pushed" || item.ExternalRef == nil || *item.ExternalRef != "acct_original" {
		t.Fatalf("voucher = %+v", item)
	}
}

func TestPushSuccessAndRepeatedFailure(t *testing.T) {
	pool := testdb.Open(t)
	issueID := testdb.InsertIssue(t, pool, 10)
	okRec := createReceipt(t, pool, issueID, "push-ok")

	mock := mockacct.New()
	mock.SetRand(func() float64 { return 0.90 })
	ts := httptest.NewServer(mock.Handler())
	defer ts.Close()

	w := New(pool, Config{
		MockURL:     ts.URL,
		MaxAttempts: 3,
		Lease:       time.Minute,
		HTTPTimeout: 2 * time.Second,
		Backoff:     func(int) time.Duration { return 0 },
	})
	claimed := claimOneOf(t, pool, w, map[int64]bool{okRec.Voucher.ID: true})
	if err := w.push(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	pushed := mustVoucher(t, pool, okRec.Voucher.ID)
	if pushed.Status != "pushed" || pushed.ExternalRef == nil {
		t.Fatalf("pushed = %+v", pushed)
	}

	badRec := createReceipt(t, pool, issueID, "push-bad")
	mock.SetRand(func() float64 { return 0 })
	for i := 0; i < 3; i++ {
		claimed = claimOneOf(t, pool, w, map[int64]bool{badRec.Voucher.ID: true})
		if err := w.push(context.Background(), claimed); err != nil {
			t.Fatal(err)
		}
	}
	failed := mustVoucher(t, pool, badRec.Voucher.ID)
	if failed.Status != "failed" || failed.LastError == nil || *failed.LastError != "accounting unavailable" {
		t.Fatalf("failed = %+v", failed)
	}
	retried, err := voucher.Retry(context.Background(), pool, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "pending" || retried.Attempts != 0 {
		t.Fatalf("retry = %+v", retried)
	}
}

func createReceipt(t *testing.T, pool *pgxpool.Pool, issueID int64, key string) receipt.Receipt {
	t.Helper()
	rec, _, err := receipt.NewStore(pool).Create(context.Background(), receipt.Input{
		IssueID:        issueID,
		Qty:            1,
		ReceivedOn:     time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: fmt.Sprintf("%s-%d", key, issueID),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func claimOneOf(t *testing.T, pool *pgxpool.Pool, w *Worker, want map[int64]bool) Voucher {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		v, ok, err := w.claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if want[v.ID] {
			return v
		}
		releaseClaim(t, pool, v)
	}
	t.Fatal("timed out waiting for this test's voucher")
	return Voucher{}
}

func claimUntilQuiet(t *testing.T, pool *pgxpool.Pool, w *Worker, ours map[int64]bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, ok, err := w.claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return false
		}
		if ours[v.ID] {
			return true
		}
		releaseClaim(t, pool, v)
	}
	return false
}

func releaseClaim(t *testing.T, pool *pgxpool.Pool, v Voucher) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE vouchers
		SET attempts = GREATEST(attempts - 1, 0),
		    lease_until = NULL
		WHERE id = $1
		  AND status = 'pending'
	`, v.ID)
	if err != nil {
		t.Fatal(err)
	}
}

func mustVoucher(t *testing.T, pool *pgxpool.Pool, id int64) voucher.Voucher {
	t.Helper()
	items, err := voucher.List(context.Background(), pool, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("voucher %d not found", id)
	return voucher.Voucher{}
}
