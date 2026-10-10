package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultMaxAttempts = 4

type Config struct {
	MockURL     string
	MaxAttempts int
	HTTPTimeout time.Duration
	Lease       time.Duration
	Poll        time.Duration
	Parallel    int
	Backoff     func(attempt int) time.Duration
}

type Voucher struct {
	ID          int64
	Attempts    int
	ClientRef   string
	AmountPaise int64
}

type Worker struct {
	pool   *pgxpool.Pool
	cfg    Config
	client *http.Client
}

func New(pool *pgxpool.Pool, cfg Config) *Worker {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 8 * time.Second
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 12 * time.Second
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 500 * time.Millisecond
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 8
	}
	if cfg.Backoff == nil {
		cfg.Backoff = DefaultBackoff
	}
	if cfg.MockURL == "" {
		cfg.MockURL = "http://127.0.0.1:8090"
	}
	cfg.MockURL = strings.TrimRight(cfg.MockURL, "/")
	return &Worker{
		pool:   pool,
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
	}
}

// DefaultBackoff is 250ms, 500ms, 1s, then 2s. The HTTP timeout is 8s, longer
// than the mock's 5s hang, and the lease is 12s so a live push is not stolen.
func DefaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 5 {
		shift = 5
	}
	return 250 * time.Millisecond * time.Duration(uint(1)<<uint(shift))
}

// Run claims pending vouchers and pushes them until ctx is cancelled.
// In-flight pushes finish before Run returns, so a Ctrl+C still records
// a success or a 409. A killed process leaves the lease, and the next
// process posts the same client_ref and reconciles the 409.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, w.cfg.Parallel)
	for {
		if ctx.Err() != nil {
			wg.Wait()
			return ctx.Err()
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		voucher, ok, err := w.claim(ctx)
		if err != nil {
			<-sem
			log.Printf("claim: %v", err)
			if !sleep(ctx, w.cfg.Poll) {
				wg.Wait()
				return ctx.Err()
			}
			continue
		}
		if !ok {
			<-sem
			if !sleep(ctx, w.cfg.Poll) {
				wg.Wait()
				return ctx.Err()
			}
			continue
		}
		wg.Add(1)
		go func(v Voucher) {
			defer wg.Done()
			defer func() { <-sem }()
			pushCtx, cancel := context.WithTimeout(context.Background(), w.cfg.HTTPTimeout)
			defer cancel()
			if err := w.push(pushCtx, v); err != nil {
				log.Printf("voucher %d: %v", v.ID, err)
			}
		}(voucher)
	}
}

func (w *Worker) claim(ctx context.Context) (Voucher, bool, error) {
	var v Voucher
	err := w.pool.QueryRow(ctx, `
		WITH picked AS (
			SELECT id
			FROM vouchers
			WHERE status = 'pending'
			  AND (next_attempt_at IS NULL OR next_attempt_at <= now())
			  AND (lease_until IS NULL OR lease_until < now())
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE vouchers AS v
		SET attempts = v.attempts + 1,
		    lease_until = now() + ($1 * interval '1 millisecond')
		FROM picked
		WHERE v.id = picked.id
		RETURNING v.id, v.attempts, v.client_ref, v.amount_paise
	`, w.cfg.Lease.Milliseconds()).Scan(&v.ID, &v.Attempts, &v.ClientRef, &v.AmountPaise)
	if errors.Is(err, pgx.ErrNoRows) {
		return Voucher{}, false, nil
	}
	if err != nil {
		return Voucher{}, false, err
	}
	log.Printf("voucher %d claim attempt %d", v.ID, v.Attempts)
	return v, true, nil
}

func (w *Worker) push(ctx context.Context, v Voucher) error {
	body, err := json.Marshal(map[string]any{
		"client_ref":   v.ClientRef,
		"amount_paise": v.AmountPaise,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.MockURL+"/api/vouchers", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return w.fail(ctx, v, err.Error())
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		ExternalRef string `json:"external_ref"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(payload, &parsed)

	switch resp.StatusCode {
	case http.StatusOK:
		if parsed.ExternalRef == "" {
			return w.fail(ctx, v, "accounting response missing external_ref")
		}
		log.Printf("voucher %d pushed %s", v.ID, parsed.ExternalRef)
		return w.markPushed(ctx, v.ID, parsed.ExternalRef)
	case http.StatusConflict:
		if parsed.ExternalRef == "" {
			return w.fail(ctx, v, "duplicate response missing external_ref")
		}
		log.Printf("voucher %d duplicate, reconciled %s", v.ID, parsed.ExternalRef)
		return w.markPushed(ctx, v.ID, parsed.ExternalRef)
	default:
		reason := parsed.Error
		if reason == "" {
			reason = fmt.Sprintf("accounting HTTP %d", resp.StatusCode)
		}
		return w.fail(ctx, v, reason)
	}
}

func (w *Worker) markPushed(ctx context.Context, id int64, externalRef string) error {
	_, err := w.pool.Exec(context.WithoutCancel(ctx), `
		UPDATE vouchers
		SET status = 'pushed',
		    external_ref = $2,
		    last_error = NULL,
		    lease_until = NULL,
		    next_attempt_at = NULL
		WHERE id = $1
		  AND status = 'pending'
	`, id, externalRef)
	return err
}

func (w *Worker) fail(ctx context.Context, v Voucher, reason string) error {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	dbCtx := context.WithoutCancel(ctx)
	if v.Attempts >= w.cfg.MaxAttempts {
		_, err := w.pool.Exec(dbCtx, `
			UPDATE vouchers
			SET status = 'failed',
			    last_error = $2,
			    lease_until = NULL,
			    next_attempt_at = NULL
			WHERE id = $1
			  AND status = 'pending'
		`, v.ID, reason)
		if err != nil {
			return err
		}
		log.Printf("voucher %d failed: %s", v.ID, reason)
		return nil
	}
	wait := w.cfg.Backoff(v.Attempts)
	_, err := w.pool.Exec(dbCtx, `
		UPDATE vouchers
		SET last_error = $2,
		    lease_until = NULL,
		    next_attempt_at = now() + ($3 * interval '1 millisecond')
		WHERE id = $1
		  AND status = 'pending'
	`, v.ID, reason, wait.Milliseconds())
	if err != nil {
		return err
	}
	log.Printf("voucher %d attempt %d failed: %s", v.ID, v.Attempts, reason)
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
