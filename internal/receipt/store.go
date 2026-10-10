package receipt

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TypeJobworkReceipt = "jobwork_receipt"
	// PaisePerUnit is ₹100. The brief has no price on a SKU.
	PaisePerUnit = int64(10000)
)

var (
	ErrIssueNotFound       = errors.New("issue not found")
	ErrOvershoot           = errors.New("receipt exceeds issued quantity")
	ErrInvalidQty          = errors.New("qty must be at least 1")
	ErrInvalidDate         = errors.New("received_on is required")
	ErrDateBeforeIssue     = errors.New("received_on is before the issue date")
	ErrMissingKey          = errors.New("Idempotency-Key is required")
	ErrIdempotencyMismatch = errors.New("idempotency key was already used with a different request")
)

type Input struct {
	IssueID        int64
	Qty            int
	ReceivedOn     time.Time
	IdempotencyKey string
}

type Receipt struct {
	ID             int64   `json:"id"`
	IssueID        int64   `json:"issue_id"`
	Qty            int     `json:"qty"`
	ReceivedOn     string  `json:"received_on"`
	IdempotencyKey string  `json:"idempotency_key"`
	Voucher        Voucher `json:"voucher"`
}

type Voucher struct {
	ID          int64   `json:"id"`
	Type        string  `json:"type"`
	SourceID    int64   `json:"source_id"`
	AmountPaise int64   `json:"amount_paise"`
	Status      string  `json:"status"`
	Attempts    int     `json:"attempts"`
	LastError   *string `json:"last_error,omitempty"`
	ClientRef   string  `json:"client_ref"`
	ExternalRef *string `json:"external_ref,omitempty"`
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type rowQuery interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Create inserts a receipt and its pending voucher in one transaction.
// The issue row is locked, then the idempotency key is checked again, so two
// identical requests cannot both pass the "not seen yet" read. received_qty
// moves only when the new total still fits the issued quantity; the check
// constraint is the backstop. A replay returns the original receipt.
func (s *Store) Create(ctx context.Context, in Input) (Receipt, bool, error) {
	if in.IdempotencyKey == "" {
		return Receipt{}, false, ErrMissingKey
	}
	if in.Qty < 1 {
		return Receipt{}, false, ErrInvalidQty
	}
	if in.ReceivedOn.IsZero() {
		return Receipt{}, false, ErrInvalidDate
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Receipt{}, false, err
	}
	defer tx.Rollback(ctx)

	if rec, ok, err := loadByKey(ctx, tx, in.IdempotencyKey); err != nil {
		return Receipt{}, false, err
	} else if ok {
		return replay(rec, in)
	}

	var issued, received int
	var issuedOn time.Time
	err = tx.QueryRow(ctx, `
		SELECT qty, received_qty, issued_on
		FROM jobwork_issues
		WHERE id = $1
		FOR UPDATE
	`, in.IssueID).Scan(&issued, &received, &issuedOn)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, false, ErrIssueNotFound
	}
	if err != nil {
		return Receipt{}, false, err
	}

	// The other request may have committed while this one waited on the lock.
	if rec, ok, err := loadByKey(ctx, tx, in.IdempotencyKey); err != nil {
		return Receipt{}, false, err
	} else if ok {
		return replay(rec, in)
	}

	if in.ReceivedOn.Before(issuedOn) {
		return Receipt{}, false, ErrDateBeforeIssue
	}

	if received+in.Qty > issued {
		return Receipt{}, false, ErrOvershoot
	}

	tag, err := tx.Exec(ctx, `
		UPDATE jobwork_issues
		SET received_qty = received_qty + $2
		WHERE id = $1
		  AND received_qty + $2 <= qty
	`, in.IssueID, in.Qty)
	if err != nil {
		return Receipt{}, false, mapConstraint(err)
	}
	if tag.RowsAffected() != 1 {
		return Receipt{}, false, ErrOvershoot
	}

	var receiptID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO jobwork_receipts (issue_id, qty, received_on, idempotency_key)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, in.IssueID, in.Qty, in.ReceivedOn, in.IdempotencyKey).Scan(&receiptID)
	if err != nil {
		if isUnique(err) {
			rec, _, err := s.replayAfterConflict(ctx, tx, in.IdempotencyKey)
			if err != nil {
				return Receipt{}, false, err
			}
			return replay(rec, in)
		}
		return Receipt{}, false, mapConstraint(err)
	}

	rec := Receipt{
		ID:             receiptID,
		IssueID:        in.IssueID,
		Qty:            in.Qty,
		ReceivedOn:     in.ReceivedOn.Format("2006-01-02"),
		IdempotencyKey: in.IdempotencyKey,
		Voucher: Voucher{
			Type:        TypeJobworkReceipt,
			SourceID:    receiptID,
			AmountPaise: int64(in.Qty) * PaisePerUnit,
			Status:      "pending",
		},
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO vouchers (type, source_id, amount_paise, status, client_ref)
		VALUES ($1, $2, $3, 'pending', gen_random_uuid()::text)
		RETURNING id, client_ref
	`, TypeJobworkReceipt, receiptID, rec.Voucher.AmountPaise).Scan(&rec.Voucher.ID, &rec.Voucher.ClientRef)
	if err != nil {
		return Receipt{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Receipt{}, false, err
	}
	return rec, true, nil
}

// replayAfterConflict runs when the idempotency key lost the insert race.
// The failed transaction is rolled back first, so the quantity bump is undone,
// then the winning receipt is read.
func (s *Store) replayAfterConflict(ctx context.Context, tx pgx.Tx, key string) (Receipt, bool, error) {
	if err := tx.Rollback(ctx); err != nil {
		return Receipt{}, false, err
	}
	rec, ok, err := loadByKey(ctx, s.pool, key)
	if err != nil {
		return Receipt{}, false, err
	}
	if !ok {
		return Receipt{}, false, fmt.Errorf("idempotency key %q conflicted but no receipt was stored", key)
	}
	return rec, false, nil
}

func replay(rec Receipt, in Input) (Receipt, bool, error) {
	if rec.IssueID != in.IssueID || rec.Qty != in.Qty || rec.ReceivedOn != in.ReceivedOn.Format("2006-01-02") {
		return Receipt{}, false, ErrIdempotencyMismatch
	}
	return rec, false, nil
}

func loadByKey(ctx context.Context, q rowQuery, key string) (Receipt, bool, error) {
	var rec Receipt
	var receivedOn time.Time
	err := q.QueryRow(ctx, `
		SELECT r.id, r.issue_id, r.qty, r.received_on, r.idempotency_key,
		       v.id, v.type, v.source_id, v.amount_paise, v.status, v.attempts,
		       v.last_error, v.client_ref, v.external_ref
		FROM jobwork_receipts r
		JOIN vouchers v ON v.type = $2 AND v.source_id = r.id
		WHERE r.idempotency_key = $1
	`, key, TypeJobworkReceipt).Scan(
		&rec.ID, &rec.IssueID, &rec.Qty, &receivedOn, &rec.IdempotencyKey,
		&rec.Voucher.ID, &rec.Voucher.Type, &rec.Voucher.SourceID, &rec.Voucher.AmountPaise,
		&rec.Voucher.Status, &rec.Voucher.Attempts, &rec.Voucher.LastError,
		&rec.Voucher.ClientRef, &rec.Voucher.ExternalRef,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	rec.ReceivedOn = receivedOn.Format("2006-01-02")
	return rec, true, nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func mapConstraint(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return ErrOvershoot
	}
	return err
}
