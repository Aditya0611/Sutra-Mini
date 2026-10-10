package issue

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Aditya0611/Sutra-Mini/internal/receipt"
	"github.com/Aditya0611/Sutra-Mini/internal/testdb"
)

func TestOutstandingAfterReceipt(t *testing.T) {
	pool := testdb.Open(t)
	issueID := testdb.InsertIssue(t, pool, 10)
	_, _, err := receipt.NewStore(pool).Create(context.Background(), receipt.Input{
		IssueID:        issueID,
		Qty:            2,
		ReceivedOn:     time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: fmt.Sprintf("issue-total-%d", issueID),
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := Load(context.Background(), pool, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Issued != 10 || item.Received != 2 || item.Outstanding != 8 {
		t.Fatalf("issue = %+v", item)
	}
}
