package client

import (
	"fmt"

	"github.com/plaid/plaid-go/v20/plaid"
)

// ErrCodeSyncMutationDuringPagination is returned by /transactions/sync when the
// Item's transactions changed between paginated calls. Per Plaid's docs the whole
// pagination pass must be restarted from the cursor it began with.
// https://plaid.com/docs/transactions/#integration-overview
const ErrCodeSyncMutationDuringPagination = "TRANSACTIONS_SYNC_MUTATION_DURING_PAGINATION"

// DefaultSyncRestarts bounds how many times SyncItem restarts a pagination pass
// after a mutation error before giving up on the Item.
const DefaultSyncRestarts = 3

// SyncPage is one /transactions/sync response page.
type SyncPage struct {
	NextCursor string
	Added      []plaid.Transaction
	Modified   []plaid.Transaction
	Removed    []plaid.RemovedTransaction
	HasMore    bool
}

// PageFetcher fetches a single sync page starting at cursor ("" for the initial sync).
type PageFetcher func(cursor string) (SyncPage, error)

// SyncResult is the accumulated outcome of a complete pagination pass.
type SyncResult struct {
	NextCursor string
	Added      []plaid.Transaction
	Modified   []plaid.Transaction
	Removed    []plaid.RemovedTransaction
	Pages      int
	Restarts   int
}

// SyncItem walks every /transactions/sync page for an Item starting at
// startCursor and returns the accumulated changes only once has_more is false,
// so callers never apply a partial pass. If Plaid reports
// TRANSACTIONS_SYNC_MUTATION_DURING_PAGINATION, the buffered pages are discarded
// and the pass restarts from startCursor, up to maxRestarts times. Any other
// error aborts immediately.
func SyncItem(fetch PageFetcher, startCursor string, maxRestarts int) (SyncResult, error) {
	restarts := 0
	for {
		res := SyncResult{Restarts: restarts}
		cursor := startCursor
		var passErr error
		for {
			page, err := fetch(cursor)
			if err != nil {
				passErr = fmt.Errorf("sync failed on page %d: %w", res.Pages+1, err)
				break
			}
			res.Pages++
			res.Added = append(res.Added, page.Added...)
			res.Modified = append(res.Modified, page.Modified...)
			res.Removed = append(res.Removed, page.Removed...)
			cursor = page.NextCursor
			if !page.HasMore {
				res.NextCursor = cursor
				return res, nil
			}
		}

		if !IsPlaidErrorCode(passErr, ErrCodeSyncMutationDuringPagination) || restarts >= maxRestarts {
			return SyncResult{Restarts: restarts}, passErr
		}
		restarts++
	}
}

// NewSyncPageFetcher returns a PageFetcher backed by /transactions/sync for the
// given access token.
func NewSyncPageFetcher(client *plaid.APIClient, accessToken string) PageFetcher {
	return func(cursor string) (SyncPage, error) {
		next, added, modified, removed, more, err := SyncTransactionsPage(client, accessToken, cursor)
		if err != nil {
			return SyncPage{}, err
		}
		return SyncPage{NextCursor: next, Added: added, Modified: modified, Removed: removed, HasMore: more}, nil
	}
}
