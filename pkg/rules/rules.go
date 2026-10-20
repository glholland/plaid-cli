package rules

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"plaid-cli/pkg/config"
	"regexp"
	"strings"

	"github.com/plaid/plaid-go/v20/plaid"
)

// RulesFile is the name of the rules file stored under ~/.plaid-cli.
const RulesFile = "rules.json"

// Override is re-exported from the config package so the cache and rules engine
// share a single override type without introducing an import cycle.
type Override = config.Override

// Conditions describe the matching criteria for a rule. All non-zero conditions
// must match (AND logic) for the rule to apply.
type Conditions struct {
	NameContains string  `json:"name_contains,omitempty"`
	NameRegex    string  `json:"name_regex,omitempty"`
	AccountID    string  `json:"account_id,omitempty"`
	AmountMin    float64 `json:"amount_min,omitempty"`
	AmountMax    float64 `json:"amount_max,omitempty"`
	CategoryIs   string  `json:"category_is,omitempty"`
}

// Actions describe the overrides applied when a rule matches. All non-empty
// fields are applied.
type Actions struct {
	Rename      string   `json:"rename,omitempty"`
	SetCategory string   `json:"set_category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Ignore      bool     `json:"ignore,omitempty"`
}

// Rule is a single user-defined categorization rule.
type Rule struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	Conditions Conditions `json:"conditions"`
	Actions    Actions    `json:"actions"`
}

// RulesFileData is the on-disk representation of the rules file.
type RulesFileData struct {
	Rules []Rule `json:"rules"`
}

// DisplayTransaction wraps a Plaid transaction with override fields resolved for rendering.
type DisplayTransaction struct {
	plaid.Transaction
	DisplayName     string
	DisplayCategory string
	Tags            string
	Ignored         string
}

// NewRuleID generates a random rule identifier of the form "rule_xxxxxxxx".
func NewRuleID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// rand.Read should never fail; fall back to a static prefix length.
		return "rule_00000000"
	}
	return "rule_" + hex.EncodeToString(b)
}

// LoadRules reads the rules file from ~/.plaid-cli/rules.json. If it does not
// exist, an empty RulesFileData is returned without error.
func LoadRules() (*RulesFileData, error) {
	dir, err := config.GetDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, RulesFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RulesFileData{Rules: []Rule{}}, nil
		}
		return nil, fmt.Errorf("failed to read rules file: %w", err)
	}

	if config.IsEncrypted(data) {
		password, err := config.GetPassword()
		if err != nil {
			return nil, fmt.Errorf("failed to load encrypted rules: %w", err)
		}
		decrypted, err := config.Decrypt(data, password)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt rules: %w", err)
		}
		data = decrypted
	}

	var rf RulesFileData
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("failed to parse rules file: %w", err)
	}

	return &rf, nil
}

// SaveRules writes the rules to ~/.plaid-cli/rules.json, encrypting if the
// active config has secure mode enabled.
func (r *RulesFileData) SaveRules() error {
	dir, err := config.GetDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	path := filepath.Join(dir, RulesFile)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize rules: %w", err)
	}

	cfg, err := config.LoadConfig()
	if err == nil && cfg.Secure {
		password, err := config.GetPassword()
		if err != nil {
			return fmt.Errorf("failed to encrypt rules: %w", err)
		}
		encrypted, err := config.Encrypt(data, password)
		if err != nil {
			return fmt.Errorf("failed to encrypt rules: %w", err)
		}
		data = encrypted
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write rules file: %w", err)
	}

	return nil
}

// CategoryString derives the Plaid auto-assigned category string for a transaction,
// preferring the personal_finance_category taxonomy (Plaid's current, higher-fill
// categorization) and falling back to the deprecated legacy Category slice.
// PFC codes are humanized, e.g. FOOD_AND_DRINK / FOOD_AND_DRINK_COFFEE renders
// as "Food And Drink > Coffee".
func CategoryString(tx plaid.Transaction) string {
	if pfc := personalFinanceCategory(tx); pfc != nil && pfc.Primary != "" {
		primary := humanizeCategoryCode(pfc.Primary)
		detailed := strings.TrimPrefix(pfc.Detailed, pfc.Primary+"_")
		if detailed == "" || detailed == pfc.Primary {
			return primary
		}
		return primary + " > " + humanizeCategoryCode(detailed)
	}
	return legacyCategoryString(tx)
}

// categorySearchText returns every category representation of a transaction
// (humanized PFC, raw PFC codes, and the legacy hierarchy) so `category_is`
// rules written against either taxonomy keep matching after the PFC switch.
func categorySearchText(tx plaid.Transaction) string {
	parts := []string{CategoryString(tx), legacyCategoryString(tx)}
	if pfc := personalFinanceCategory(tx); pfc != nil {
		parts = append(parts, pfc.Primary, pfc.Detailed)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

func personalFinanceCategory(tx plaid.Transaction) *plaid.PersonalFinanceCategory {
	if tx.PersonalFinanceCategory.IsSet() {
		return tx.PersonalFinanceCategory.Get()
	}
	return nil
}

func legacyCategoryString(tx plaid.Transaction) string {
	return strings.Join(tx.Category, " > ")
}

// humanizeCategoryCode turns an UPPER_SNAKE PFC code into Title Case words.
func humanizeCategoryCode(code string) string {
	words := strings.Split(strings.ToLower(code), "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// Match evaluates all non-zero conditions of the rule against a transaction.
// Returns true only if every present condition matches (AND logic).
func (r *Rule) Match(tx plaid.Transaction) bool {
	c := r.Conditions

	if c.NameContains != "" {
		if !strings.Contains(strings.ToLower(tx.Name), strings.ToLower(c.NameContains)) {
			return false
		}
	}

	if c.NameRegex != "" {
		re, err := regexp.Compile(c.NameRegex)
		if err != nil {
			return false
		}
		if !re.MatchString(tx.Name) {
			return false
		}
	}

	if c.AccountID != "" {
		if tx.AccountId != c.AccountID {
			return false
		}
	}

	if c.AmountMin != 0 {
		if tx.Amount < c.AmountMin {
			return false
		}
	}

	if c.AmountMax != 0 {
		if tx.Amount > c.AmountMax {
			return false
		}
	}

	if c.CategoryIs != "" {
		if !strings.Contains(categorySearchText(tx), strings.ToLower(c.CategoryIs)) {
			return false
		}
	}

	return true
}

// Apply produces an Override from the rule's actions for the given transaction.
func (r *Rule) Apply(tx plaid.Transaction) Override {
	o := Override{
		RuleID: r.ID,
		Manual: false,
		Source: config.SourceRule,
	}
	if r.Actions.Rename != "" {
		o.DisplayName = r.Actions.Rename
	}
	if r.Actions.SetCategory != "" {
		o.Category = r.Actions.SetCategory
	}
	if len(r.Actions.Tags) > 0 {
		o.Tags = append([]string{}, r.Actions.Tags...)
	}
	o.Ignored = r.Actions.Ignore
	return o
}

// ApplyAll runs all enabled rules over the given transaction IDs (pass nil for all
// transactions), updating cache.Overrides. Manually overridden transactions are
// skipped. Returns the count of overrides written.
func ApplyAll(cache *config.Cache, rules *RulesFileData, onlyIDs []string) int {
	if cache.Overrides == nil {
		cache.Overrides = make(map[string]Override)
	}

	// Build a lookup of transaction IDs to consider.
	var idFilter map[string]bool
	if onlyIDs != nil {
		idFilter = make(map[string]bool, len(onlyIDs))
		for _, id := range onlyIDs {
			idFilter[id] = true
		}
	}

	count := 0
	for _, tx := range cache.Transactions {
		if idFilter != nil && !idFilter[tx.TransactionId] {
			continue
		}

		// Preserve manual and correlation overrides; rules never clobber them.
		if existing, ok := cache.Overrides[tx.TransactionId]; ok &&
			(existing.Manual || existing.Source == config.SourceCorrelate) {
			continue
		}

		// First matching enabled rule wins.
		matched := false
		for i := range rules.Rules {
			rule := &rules.Rules[i]
			if !rule.Enabled {
				continue
			}
			if rule.Match(tx) {
				cache.Overrides[tx.TransactionId] = rule.Apply(tx)
				count++
				matched = true
				break
			}
		}

		// If no rule matches but a stale rule-generated override exists, drop it.
		// Manual and correlation overrides are never auto-removed.
		if !matched {
			if existing, ok := cache.Overrides[tx.TransactionId]; ok &&
				!existing.Manual && existing.Source != config.SourceCorrelate {
				delete(cache.Overrides, tx.TransactionId)
			}
		}
	}

	return count
}

// MergeOverrides returns a new slice of DisplayTransactions with override fields
// applied for rendering. Raw Plaid values are used when no override is present.
func MergeOverrides(txs []plaid.Transaction, overrides map[string]Override) []DisplayTransaction {
	result := make([]DisplayTransaction, 0, len(txs))
	for _, tx := range txs {
		dt := DisplayTransaction{
			Transaction:     tx,
			DisplayName:     tx.Name,
			DisplayCategory: CategoryString(tx),
		}
		if o, ok := overrides[tx.TransactionId]; ok {
			if o.DisplayName != "" {
				dt.DisplayName = o.DisplayName
			}
			if o.Category != "" {
				dt.DisplayCategory = o.Category
			}
			if len(o.Tags) > 0 {
				dt.Tags = strings.Join(o.Tags, ",")
			}
			if o.Ignored {
				dt.Ignored = "true"
			}
		}
		result = append(result, dt)
	}
	return result
}
