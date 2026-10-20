package cmd

import (
	"fmt"
	"os"
	"plaid-cli/pkg/client"
	"plaid-cli/pkg/config"
	"plaid-cli/pkg/rules"
	"time"

	"github.com/plaid/plaid-go/v20/plaid"
	"github.com/spf13/cobra"
)

var (
	resetFlag     bool
	itemIdFlag    string
	accountIdFlag string
)

func init() {
	syncCmd.Flags().BoolVar(&resetFlag, "reset", false, "Reset the sync cursor and perform a full historical sync from scratch")
	syncCmd.Flags().StringVar(&itemIdFlag, "item-id", "", "Sync transactions only for a specific Plaid Item ID")
	syncCmd.Flags().StringVar(&accountIdFlag, "account-id", "", "Sync transactions only for the Item associated with a specific Plaid Account ID")
	rootCmd.AddCommand(syncCmd)
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync transaction data from Plaid locally",
	Long: `Incrementally fetch transaction changes (added, modified, and removed) from Plaid
using cursor-based synchronization and save them to your local cache (~/.plaid-cli/cache.json).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 1. Load config
		cfg, err := config.LoadConfig()
		if err != nil {
			return err
		}

		if len(cfg.Items) == 0 {
			return fmt.Errorf("no accounts linked. Please run 'plaid-cli login' first")
		}

		// 2. Load cache
		cache, err := config.LoadCache()
		if err != nil {
			return err
		}

		// 3. Initialize Plaid Client
		plaidClient, err := client.NewPlaidClient(cfg)
		if err != nil {
			return err
		}

		// 4. Resolve target items to sync
		if itemIdFlag != "" && accountIdFlag != "" {
			return fmt.Errorf("cannot specify both --item-id and --account-id")
		}

		var targetItems []config.LinkedItem
		if itemIdFlag != "" {
			found := false
			for _, item := range cfg.Items {
				if item.ItemID == itemIdFlag {
					targetItems = append(targetItems, item)
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("item ID %s not found in linked accounts config", itemIdFlag)
			}
		} else if accountIdFlag != "" {
			fmt.Fprintln(os.Stderr, "Resolving account ID to find its parent Plaid Item...")
			// Resolve via the cached directory when possible; the sync loop below
			// fetches the matched item's accounts anyway, so a live walk here would
			// double the /accounts/get calls for that item.
			idx, err := findItemByAccountID(cfg, plaidClient, accountIdFlag)
			if err != nil {
				return err
			}
			targetItems = append(targetItems, cfg.Items[idx])
		} else {
			targetItems = cfg.Items
		}

		// Map existing transactions by ID
		txMap := make(map[string]plaid.Transaction)
		for _, tx := range cache.Transactions {
			txMap[tx.TransactionId] = tx
		}

		if resetFlag {
			if len(targetItems) == len(cfg.Items) {
				cache.Cursors = make(map[string]string)
				cache.Cursor = ""
				txMap = make(map[string]plaid.Transaction)
				fmt.Println("Reset flag enabled. Clearing all local cursors and cached transactions from scratch...")
			} else {
				fmt.Println("Reset flag enabled for targeted sync. Resetting cursor and cached transactions for target items...")
			}
		}

		fmt.Println("Starting transaction synchronization...")

		addedCount := 0
		modifiedCount := 0
		removedCount := 0
		changedIDs := make(map[string]bool)
		// Collected account metadata per item, persisted after the sync loop so
		// offline commands can render account labels without a live Plaid call.
		accountDir := make(map[string][]config.Account)
		// Freshness metadata per item, persisted alongside the account directory.
		itemStatus := make(map[string]client.ItemStatus)
		syncedAt := make(map[string]time.Time)

		for idx, item := range targetItems {
			institutionStr := item.ItemID
			if len(institutionStr) > 8 {
				institutionStr = institutionStr[:8]
			}
			fmt.Printf("[%d/%d] Syncing transactions for Item %s...\n", idx+1, len(targetItems), institutionStr)

			// 0. Record Item health and Plaid's last institution update (/item/get,
			// free). This works even when the Item is in an error state, so the
			// error is captured before the calls below fail on it.
			if st, err := client.FetchItemStatus(plaidClient, item.AccessToken); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to fetch status for Item %s: %v\n", item.ItemID, err)
			} else {
				itemStatus[item.ItemID] = st
				if st.ErrorCode != "" {
					fmt.Fprintf(os.Stderr, "  Warning: Item %s reports %s. Run 'plaid-cli login --update' to re-authenticate.\n", item.ItemID, st.ErrorCode)
				}
			}

			// 1. Fetch and check linked accounts for the current item
			accounts, err := client.FetchAccounts(plaidClient, item.AccessToken)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to check accounts for Item %s: %v. Skipping this item.\n", item.ItemID, err)
				continue
			}

			fmt.Println("  Checking linked accounts:")
			metas := make([]config.Account, 0, len(accounts))
			for _, acc := range accounts {
				subtypeStr := ""
				if acc.Subtype.IsSet() && acc.Subtype.Get() != nil {
					subtypeStr = string(*acc.Subtype.Get())
				}
				typeDisplay := string(acc.Type)
				if subtypeStr != "" {
					typeDisplay = fmt.Sprintf("%s (%s)", acc.Type, subtypeStr)
				}
				fmt.Printf("    - %s [ID: %s, Type: %s]\n", acc.Name, acc.AccountId, typeDisplay)
				metas = append(metas, accountMetaFrom(acc))
			}
			accountDir[item.ItemID] = metas

			// 2. Reset handling (clear targeted cache) if requested
			if resetFlag && len(targetItems) < len(cfg.Items) {
				delete(cache.Cursors, item.ItemID)
				if item.ItemID == cfg.ItemID {
					cache.Cursor = ""
				}

				accMap := make(map[string]bool)
				for _, acc := range accounts {
					accMap[acc.AccountId] = true
				}

				// Remove existing cached transactions belonging to this item's accounts
				for txID, tx := range txMap {
					if accMap[tx.AccountId] {
						delete(txMap, txID)
					}
				}
			}

			itemCursor := cache.Cursors[item.ItemID]
			if itemCursor != "" {
				fmt.Printf("  Resuming sync from cursor: %s...\n", itemCursor[:15]+"...[truncated]")
			} else {
				fmt.Println("  Initial synchronization. This might take a moment...")
			}

			// Buffer the full pagination pass before touching the cache. SyncItem
			// restarts from itemCursor if Plaid reports the data mutated mid-pass.
			fetchPage := client.NewSyncPageFetcher(plaidClient, item.AccessToken)
			page := 0
			result, syncErr := client.SyncItem(func(cursor string) (client.SyncPage, error) {
				if cursor == itemCursor {
					page = 0 // fresh pass (initial or restarted)
				}
				page++
				fmt.Printf("  Fetching transaction page %d...\n", page)
				return fetchPage(cursor)
			}, itemCursor, client.DefaultSyncRestarts)
			if result.Restarts > 0 {
				fmt.Fprintf(os.Stderr, "  Note: transactions changed during pagination; restarted sync %d time(s).\n", result.Restarts)
			}
			if syncErr != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to sync transactions for Item %s: %v. Continuing...\n", item.ItemID, syncErr)
				continue
			}

			for _, tx := range result.Added {
				txMap[tx.TransactionId] = tx
				changedIDs[tx.TransactionId] = true
				addedCount++
			}
			for _, tx := range result.Modified {
				txMap[tx.TransactionId] = tx
				changedIDs[tx.TransactionId] = true
				modifiedCount++
			}
			for _, rtx := range result.Removed {
				if rtx.TransactionId != nil {
					delete(txMap, *rtx.TransactionId)
					removedCount++
				}
			}

			cache.Cursors[item.ItemID] = result.NextCursor
			syncedAt[item.ItemID] = time.Now()
		}

		// Persist the refreshed account directory back to config so offline
		// commands (e.g. transactions) can render human-readable account labels.
		dirChanged := false
		for i := range cfg.Items {
			id := cfg.Items[i].ItemID
			if metas, ok := accountDir[id]; ok {
				cfg.Items[i].Accounts = metas
				dirChanged = true
			}
			if st, ok := itemStatus[id]; ok {
				cfg.Items[i].ItemError = st.ErrorCode
				cfg.Items[i].TransactionsLastUpdated = st.TransactionsLastSuccessfulUpdate
				cfg.Items[i].TransactionsLastFailedUpdate = st.TransactionsLastFailedUpdate
				dirChanged = true
			}
			if t, ok := syncedAt[id]; ok {
				cfg.Items[i].LastSyncedAt = &t
				dirChanged = true
			}
		}
		if dirChanged {
			if err := cfg.SaveConfig(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to persist account directory: %v\n", err)
			}
		}

		// Convert map back to slice
		updatedTxList := make([]plaid.Transaction, 0, len(txMap))
		for _, tx := range txMap {
			updatedTxList = append(updatedTxList, tx)
		}

		// Save back to cache
		cache.Transactions = updatedTxList

		err = cache.SaveCache()
		if err != nil {
			return fmt.Errorf("failed to save transactions cache: %w", err)
		}

		// Apply categorization rules to the transactions added/modified in this sync.
		overrideCount := 0
		rf, rErr := rules.LoadRules()
		if rErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load rules, skipping auto-categorization: %v\n", rErr)
		} else if len(rf.Rules) > 0 && len(changedIDs) > 0 {
			ids := make([]string, 0, len(changedIDs))
			for id := range changedIDs {
				ids = append(ids, id)
			}
			overrideCount = rules.ApplyAll(cache, rf, ids)
			if err := cache.SaveCache(); err != nil {
				return fmt.Errorf("failed to save cache after applying rules: %w", err)
			}
		}

		fmt.Println("\nSynchronization completed successfully!")
		fmt.Printf("Added:      %d\n", addedCount)
		fmt.Printf("Modified:   %d\n", modifiedCount)
		fmt.Printf("Removed:    %d\n", removedCount)
		fmt.Printf("Overrides:  %d\n", overrideCount)
		fmt.Printf("Total Cache Size: %d transactions\n", len(updatedTxList))
		fmt.Println("You can run 'plaid-cli transactions' to view and filter them.")

		return nil
	},
}
