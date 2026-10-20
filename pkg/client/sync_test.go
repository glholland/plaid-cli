package client

import (
	"errors"
	"testing"

	"github.com/plaid/plaid-go/v20/plaid"
)

type fakePage struct {
	page SyncPage
	err  error
}

// scriptedFetcher returns a PageFetcher that replays responses in order and
// records the cursor each call was made with.
func scriptedFetcher(t *testing.T, script []fakePage, cursors *[]string) PageFetcher {
	t.Helper()
	i := 0
	return func(cursor string) (SyncPage, error) {
		*cursors = append(*cursors, cursor)
		if i >= len(script) {
			t.Fatalf("unexpected extra fetch with cursor %q", cursor)
		}
		p := script[i]
		i++
		return p.page, p.err
	}
}

func txn(id string) plaid.Transaction { return plaid.Transaction{TransactionId: id} }

func mutationErr() error {
	return &APIError{Code: ErrCodeSyncMutationDuringPagination, Message: "mutated"}
}

func TestSyncItemPaginates(t *testing.T) {
	var cursors []string
	fetch := scriptedFetcher(t, []fakePage{
		{page: SyncPage{NextCursor: "c1", Added: []plaid.Transaction{txn("a")}, HasMore: true}},
		{page: SyncPage{NextCursor: "c2", Modified: []plaid.Transaction{txn("b")}, HasMore: false}},
	}, &cursors)

	res, err := SyncItem(fetch, "c0", 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.NextCursor != "c2" || len(res.Added) != 1 || len(res.Modified) != 1 {
		t.Errorf("unexpected result: %+v", res)
	}
	if want := []string{"c0", "c1"}; !equal(cursors, want) {
		t.Errorf("cursors: got %v, want %v", cursors, want)
	}
}

func TestSyncItemRestartsFromOriginalCursorOnMutation(t *testing.T) {
	var cursors []string
	fetch := scriptedFetcher(t, []fakePage{
		{page: SyncPage{NextCursor: "c1", Added: []plaid.Transaction{txn("stale")}, HasMore: true}},
		{err: mutationErr()},
		{page: SyncPage{NextCursor: "n1", Added: []plaid.Transaction{txn("fresh")}, HasMore: true}},
		{page: SyncPage{NextCursor: "n2", HasMore: false}},
	}, &cursors)

	res, err := SyncItem(fetch, "c0", 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"c0", "c1", "c0", "n1"}; !equal(cursors, want) {
		t.Errorf("cursors: got %v, want %v", cursors, want)
	}
	if len(res.Added) != 1 || res.Added[0].TransactionId != "fresh" {
		t.Errorf("pages from the aborted pass must be discarded, got %+v", res.Added)
	}
	if res.NextCursor != "n2" || res.Restarts != 1 {
		t.Errorf("unexpected result: cursor=%q restarts=%d", res.NextCursor, res.Restarts)
	}
}

func TestSyncItemGivesUpAfterMaxRestarts(t *testing.T) {
	var cursors []string
	fetch := scriptedFetcher(t, []fakePage{
		{err: mutationErr()},
		{err: mutationErr()},
	}, &cursors)

	_, err := SyncItem(fetch, "c0", 1)
	if err == nil || !IsPlaidErrorCode(err, ErrCodeSyncMutationDuringPagination) {
		t.Fatalf("expected mutation error after retries exhausted, got %v", err)
	}
	if len(cursors) != 2 {
		t.Errorf("expected 1 initial attempt + 1 restart, got %d fetches", len(cursors))
	}
}

func TestSyncItemDoesNotRetryOtherErrors(t *testing.T) {
	var cursors []string
	boom := &APIError{Code: "ITEM_LOGIN_REQUIRED"}
	fetch := scriptedFetcher(t, []fakePage{{err: boom}}, &cursors)

	_, err := SyncItem(fetch, "c0", 3)
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped original error, got %v", err)
	}
	if len(cursors) != 1 {
		t.Errorf("non-mutation errors must not be retried, got %d fetches", len(cursors))
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
