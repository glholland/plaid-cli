package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/plaid/plaid-go/v20/plaid"
	"golang.org/x/term"
)

// LinkedItem holds the access token and item ID of a linked bank account.
type LinkedItem struct {
	ItemID          string    `json:"item_id"`
	AccessToken     string    `json:"access_token"`
	InstitutionID   string    `json:"institution_id,omitempty"`
	InstitutionName string    `json:"institution_name,omitempty"`
	Accounts        []Account `json:"accounts,omitempty"`

	// Freshness metadata, refreshed by `sync` via /item/get. All optional: configs
	// written by older versions simply lack them and are treated as "unknown".
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"` // last successful local sync
	// TransactionsLastUpdated is when Plaid last pulled transactions from the institution.
	TransactionsLastUpdated *time.Time `json:"transactions_last_updated,omitempty"`
	// TransactionsLastFailedUpdate is when Plaid's last institution pull failed.
	TransactionsLastFailedUpdate *time.Time `json:"transactions_last_failed_update,omitempty"`
	// ItemError is the Item-level error code seen at last sync (e.g. ITEM_LOGIN_REQUIRED).
	ItemError string `json:"item_error,omitempty"`
}

// Account holds cached metadata for a single account within a linked Item. It
// is refreshed from Plaid whenever accounts are fetched (login/sync/accounts)
// so commands that only have an account_id can render a human-readable label
// without a live API call.
type Account struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name,omitempty"`
	Mask      string `json:"mask,omitempty"`
	Subtype   string `json:"subtype,omitempty"`
}

// Config holds Plaid credentials and list of authenticated items.
type Config struct {
	ClientID    string       `json:"client_id"`
	Secret      string       `json:"secret"`
	Environment string       `json:"environment"` // "sandbox" or "production" or "development"
	Secure      bool         `json:"secure,omitempty"`
	Items       []LinkedItem `json:"items,omitempty"`
	AccessToken string       `json:"access_token,omitempty"` // legacy
	ItemID      string       `json:"item_id,omitempty"`      // legacy
}

// Override holds rule- or manually-generated display overrides for a single transaction.
// It is stored in the cache keyed by transaction_id and never mutates the raw Plaid data.
type Override struct {
	DisplayName string   `json:"display_name,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Ignored     bool     `json:"ignored,omitempty"`
	RuleID      string   `json:"rule_id,omitempty"`
	Manual      bool     `json:"manual,omitempty"`
	// Source records what produced the override: "rule" (auto-categorization),
	// "correlate" (cross-account payment matching), or "" for legacy/manual entries.
	// Correlation overrides are preserved across `rules apply` so the engine never
	// clobbers an inter-account payment match.
	Source string `json:"source,omitempty"`
}

// Override source identifiers.
const (
	SourceRule      = "rule"
	SourceCorrelate = "correlate"
)

// Cache holds local cached transaction data and the sync cursors.
type Cache struct {
	Cursors      map[string]string   `json:"cursors,omitempty"` // item_id -> cursor
	Transactions []plaid.Transaction `json:"transactions"`
	Overrides    map[string]Override `json:"overrides,omitempty"` // transaction_id -> override
	Cursor       string              `json:"cursor,omitempty"`    // legacy
}

const (
	DirName    = ".plaid-cli"
	ConfigFile = "config.json"
	CacheFile  = "cache.json"
)

// shortIDLen is the number of leading characters shown when an account or item
// ID is rendered compactly. Matches the truncation used elsewhere in the CLI.
const shortIDLen = 8

// ShortAccountID returns the leading characters of an account ID for compact
// display, leaving short IDs untouched.
func ShortAccountID(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[:shortIDLen]
}

// AccountDirectory builds an account_id -> Account map across all linked items.
func (c *Config) AccountDirectory() map[string]Account {
	dir := make(map[string]Account)
	for _, item := range c.Items {
		for _, a := range item.Accounts {
			dir[a.AccountID] = a
		}
	}
	return dir
}

// AccountLabel renders a human-readable label for an account ID in the form
// "Name (shortid)", falling back to the short ID alone when the name is unknown.
func (c *Config) AccountLabel(accountID string) string {
	return AccountLabelFrom(c.AccountDirectory(), accountID)
}

// AccountLabelFrom is like AccountLabel but reuses a prebuilt directory to avoid
// rebuilding the map in hot render loops.
func AccountLabelFrom(dir map[string]Account, accountID string) string {
	short := ShortAccountID(accountID)
	if a, ok := dir[accountID]; ok && a.Name != "" {
		return fmt.Sprintf("%s (%s)", a.Name, short)
	}
	return short
}

var activePassword string

// SetPassword stores the master password in memory and saves it to the session file.
func SetPassword(password string) {
	activePassword = password
	_ = SaveSession(password)
}

// getPasswordOrPrompt retrieves the password from activePassword, environment, session cache, or interactively prompts.
func getPasswordOrPrompt() (string, error) {
	if activePassword != "" {
		return activePassword, nil
	}
	if envPass := os.Getenv("PLAID_CLI_PASSWORD"); envPass != "" {
		activePassword = envPass
		_ = SaveSession(envPass)
		return envPass, nil
	}
	// Try loading from session cache
	if sessPass, err := LoadSession(); err == nil && sessPass != "" {
		activePassword = sessPass
		// Slide session window
		_ = SaveSession(activePassword)
		return sessPass, nil
	}
	// Prompt interactively if stdin is a terminal
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Enter master password: ")
		bytePassword, err := term.ReadPassword(fd)
		if err != nil {
			return "", fmt.Errorf("failed to read password: %w", err)
		}
		fmt.Fprintln(os.Stderr)
		pass := string(bytePassword)
		activePassword = pass
		_ = SaveSession(pass)
		return pass, nil
	}
	return "", errors.New("master password required but not supplied (set PLAID_CLI_PASSWORD or run in a terminal)")
}

// GetPassword retrieves the master password from memory, environment, session cache,
// or by prompting interactively. Exported for use by sibling packages (e.g. rules).
func GetPassword() (string, error) {
	return getPasswordOrPrompt()
}

// GetDir returns the path to the ~/.plaid-cli directory.
func GetDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get user home directory: %w", err)
	}
	dir := filepath.Join(home, DirName)
	return dir, nil
}

// LoadConfig reads the configuration file from ~/.plaid-cli/config.json.
func LoadConfig() (*Config, error) {
	dir, err := GetDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("configuration file does not exist, please run 'plaid-cli configure'")
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Check if encrypted
	if IsEncrypted(data) {
		password, err := getPasswordOrPrompt()
		if err != nil {
			return nil, fmt.Errorf("failed to load encrypted config: %w", err)
		}
		decrypted, err := Decrypt(data, password)
		if err != nil {
			activePassword = ""
			_ = ClearSession()
			return nil, fmt.Errorf("failed to decrypt config: %w", err)
		}
		data = decrypted
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Auto-migration: if we have legacy single-item credentials but no Items list, populate it.
	if cfg.AccessToken != "" && len(cfg.Items) == 0 {
		cfg.Items = append(cfg.Items, LinkedItem{
			ItemID:      cfg.ItemID,
			AccessToken: cfg.AccessToken,
		})
		_ = cfg.SaveConfig()
	}

	return &cfg, nil
}

// SaveConfig writes the configuration to ~/.plaid-cli/config.json.
func (c *Config) SaveConfig() error {
	dir, err := GetDir()
	if err != nil {
		return err
	}

	// Create directory if not exists
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	path := filepath.Join(dir, ConfigFile)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize config: %w", err)
	}

	if c.Secure {
		password, err := getPasswordOrPrompt()
		if err != nil {
			return fmt.Errorf("failed to encrypt config: %w", err)
		}
		encrypted, err := Encrypt(data, password)
		if err != nil {
			return fmt.Errorf("failed to encrypt config: %w", err)
		}
		data = encrypted
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// LoadCache reads the cache file from ~/.plaid-cli/cache.json.
// If the cache does not exist, it returns an empty Cache object without error.
func LoadCache() (*Cache, error) {
	dir, err := GetDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, CacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Cache{
				Cursors:      make(map[string]string),
				Transactions: []plaid.Transaction{},
				Overrides:    make(map[string]Override),
			}, nil
		}
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	if IsEncrypted(data) {
		password, err := getPasswordOrPrompt()
		if err != nil {
			return nil, fmt.Errorf("failed to load encrypted cache: %w", err)
		}
		decrypted, err := Decrypt(data, password)
		if err != nil {
			activePassword = ""
			_ = ClearSession()
			return nil, fmt.Errorf("failed to decrypt cache: %w", err)
		}
		data = decrypted
	}

	var cache Cache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("failed to parse cache file: %w", err)
	}

	if cache.Cursors == nil {
		cache.Cursors = make(map[string]string)
	}

	if cache.Overrides == nil {
		cache.Overrides = make(map[string]Override)
	}

	// Auto-migration: if we have legacy single cursor, migrate it to the Cursors map using config's legacy ItemID.
	if cache.Cursor != "" && len(cache.Cursors) == 0 {
		cfg, err := LoadConfig()
		if err == nil && cfg.ItemID != "" {
			cache.Cursors[cfg.ItemID] = cache.Cursor
			cache.Cursor = ""
			_ = cache.SaveCache()
		}
	}

	return &cache, nil
}

// SaveCache writes the transaction cache to ~/.plaid-cli/cache.json.
func (c *Cache) SaveCache() error {
	dir, err := GetDir()
	if err != nil {
		return err
	}

	// Create directory if not exists
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	path := filepath.Join(dir, CacheFile)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize cache: %w", err)
	}

	// Check if config specifies secure
	cfg, err := LoadConfig()
	if err == nil && cfg.Secure {
		password, err := getPasswordOrPrompt()
		if err != nil {
			return fmt.Errorf("failed to encrypt cache: %w", err)
		}
		encrypted, err := Encrypt(data, password)
		if err != nil {
			return fmt.Errorf("failed to encrypt cache: %w", err)
		}
		data = encrypted
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}
