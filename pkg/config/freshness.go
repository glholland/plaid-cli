package config

import (
	"fmt"
	"time"
)

// DefaultStaleAfter is how old a local sync may get before offline commands
// warn that the cached transactions may be out of date. Plaid itself checks
// institutions for new transactions roughly 1–4 times per day.
const DefaultStaleAfter = 24 * time.Hour

// ItemLabel renders a short human-readable name for a linked Item.
func (i LinkedItem) ItemLabel() string {
	if i.InstitutionName != "" {
		return fmt.Sprintf("%s (%s)", i.InstitutionName, ShortAccountID(i.ItemID))
	}
	return ShortAccountID(i.ItemID)
}

// FreshnessWarnings returns one human-readable warning per linked Item whose
// cached data may be stale: never synced, last synced longer than staleAfter
// ago, or carrying an Item-level error (e.g. ITEM_LOGIN_REQUIRED) recorded
// during the last sync. Intended for stderr.
func (c *Config) FreshnessWarnings(now time.Time, staleAfter time.Duration) []string {
	var warnings []string
	for _, item := range c.Items {
		label := item.ItemLabel()
		switch {
		case item.ItemError != "":
			warnings = append(warnings, fmt.Sprintf(
				"%s needs attention (%s); data may be stale. Run 'plaid-cli login --update' to re-authenticate.",
				label, item.ItemError))
		case item.LastSyncedAt == nil:
			warnings = append(warnings, fmt.Sprintf(
				"%s has never been synced (or was last synced by an older plaid-cli). Run 'plaid-cli sync'.", label))
		case now.Sub(*item.LastSyncedAt) > staleAfter:
			warnings = append(warnings, fmt.Sprintf(
				"%s was last synced %s. Run 'plaid-cli sync' for the latest transactions.",
				label, HumanizeAge(now.Sub(*item.LastSyncedAt))))
		}
	}
	return warnings
}

// HumanizeAge renders a duration as a coarse "N units ago" string.
func HumanizeAge(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s ago", unit)
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/(24*time.Hour)), "day")
	}
}
