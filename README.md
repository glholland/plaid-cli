# Plaid API CLI Tool

A fast, lightweight, and secure Go-based command-line interface (CLI) tool for interacting with the Plaid API. Built using the official Plaid Go SDK and the Cobra CLI framework, this tool allows you to securely authenticate your bank accounts via Plaid Link (browser flow), list balances, and download/sync transaction history incrementally to a local cache.

---

## Inspiration

I noticed that Plaid started offering a ["Trial Plan"](https://support.plaid.com/hc/en-us/articles/39994173227159-What-is-the-Plaid-Trial-plan) which would enable hobbyists access to the production API for up to 10 financial accounts. I've been eyeing the use of Plaid to gain access to my own financial data but wasn't ever *that* interested in paying for it since I could always export CSVs manually for free.

With the introduction of the Trial Plan and Antigravity 2.0 dropping, I decided to see if I could utilize these tools to shape my ability to access my data in a programmatic way.

The CLI really doesn't do all that much but that's the point. It's meant to be as simple and secure as possible in feigning attempt at the [Unix Principle](https://en.wikipedia.org/wiki/Unix_philosophy) of doing one thing and doing it well.

---

## ✨ Features

- **Optional Local Encryption**: Store credentials and cached transactions in plaintext JSON or encrypted form using AES-256-GCM with a key derived from your master password via PBKDF2.
- **Secure Session Cache**: Reuse your master password for up to 15 minutes via a machine-local encrypted session file, or provide it explicitly with `PLAID_CLI_PASSWORD`.
- **Interactive Plaid Link Flow**: Spins up a temporary local HTTP server (`localhost:8080` by default), opens Plaid Link in your browser, and exchanges the returned public token securely.
- **Multi-Item Account Linking**: Re-running `login` adds or updates linked Plaid Items so you can aggregate data across multiple institutions.
- **Real-time Accounts & Balances**: Query active bank accounts, types, and current/available balances in a cleanly formatted terminal table.
- **Cursor-Based Sync**: Downloads historical transaction data and incremental updates (additions, modifications, and deletions) using Plaid's `/transactions/sync`, with cursors tracked per Plaid Item in the local cache. If transactions change mid-sync, it restarts automatically, and it never saves a half-finished sync.
- **Data Freshness Tracking**: `sync` records when each bank was last updated by Plaid and flags Items needing re-authentication. `accounts` shows a freshness table, and `transactions` warns on stderr when cached data is stale.
- **Modern Categories**: Uses Plaid's `personal_finance_category` taxonomy (e.g. `Food And Drink > Coffee`). Older rules written against the legacy categories keep matching.
- **Targeted Sync & Local Filtering**: Sync a specific item or account, then query cached transactions instantly with date, account, amount, search, and pending filters.
- **Table, JSON, and CSV Output**: Render transactions as terminal tables or export to JSON and CSV files for scripting and downstream analysis.

---

## 🚀 Getting Started

### 📋 Prerequisites

- **Go** installed on your system (v1.20+ recommended).
- **Plaid Developer Credentials**: Sign up at [plaid.com](https://dashboard.plaid.com/) and fetch your Client ID, Secret, and target environment (for example `sandbox` or `production`).

### 🔑 Plaid Developer Account Setup

To configure your Plaid account to work with this CLI tool, follow these steps:

1. **Obtain API Keys**:
   - Log in to your [Plaid Dashboard](https://dashboard.plaid.com/).
   - Enable the Trial Plan according to the [Plaid documentation](https://support.plaid.com/hc/en-us/articles/39994173227159-What-is-the-Plaid-Trial-plan).
   - Navigate to **Team Settings** > **Keys** to find your `client_id` and environment-specific API secrets (Sandbox, Development, or Production).

2. **Configure Allowed Redirect URIs (OAuth Required)**:
   - For security, institutions that use OAuth authentication (such as Chase, Wells Fargo, and Bank of America) in production or development require you to register a redirect URI.
   - Navigate to **Team Settings** > **API**.
   - Under **Allowed redirect URIs**, click **Configure** and add:

     ```text
      http://localhost:8080/
     ```

   - *Ensure the trailing slash is included exactly as shown.*

3. **Trial Production Access**:
   - If using Trial Production access, ensure your product selection in the dashboard covers `Transactions`. If you encounter a `PRODUCT_NOT_READY` or `INVALID_PRODUCT` error, verify that Transactions is enabled for your team.

### 📦 Install

#### Homebrew (macOS, Linux, WSL)

```bash
brew install glholland/tap/plaid-cli
```

#### Chocolatey (Windows)

```powershell
choco install plaid-cli
```

#### Direct download

Grab a prebuilt binary for your platform from the
[latest release](https://github.com/glholland/plaid-cli/releases/latest),
extract it, and place `plaid-cli` on your `PATH`.

#### Build from source

```bash
# Clone and navigate to the project
cd plaid-cli

# Tidy dependencies and fetch Go packages
go mod tidy

# Compile the binary
go build -o plaid-cli.exe
```

Verify the install with `plaid-cli --version`.

---

## 🛠️ Usage Guide

### 1. Configure Credentials

Store your Client ID, Secret, and Environment (typically `sandbox` or `production`):

```bash
.\plaid-cli.exe configure
```

You can also set credentials non-interactively using flags:

```bash
.\plaid-cli.exe configure --client-id "your_id" --secret "your_secret" --environment "sandbox"
```

To encrypt both `config.json` and `cache.json`, enable secure mode during configuration:

```bash
.\plaid-cli.exe configure --secure
```

For non-interactive encrypted workflows, provide the master password through `PLAID_CLI_PASSWORD`:

```powershell
$env:PLAID_CLI_PASSWORD = "your-master-password"
.\plaid-cli.exe configure --secure --client-id "your_id" --secret "your_secret" --environment "sandbox"
```

### 2. Login (Authenticate via Plaid Link)

Launches a temporary browser window to sign in with your financial institution:

```bash
.\plaid-cli.exe login
```

You can change the local callback port if `8080` is already in use:

```bash
.\plaid-cli.exe login --port 9090
```

* **Sandbox Auth**: Search for any bank (e.g. "Chase"), select it, and log in using the credentials:
  - **Username**: `user_good`
  - **Password**: `pass_good`
* Once authenticated, you will see a success message in the browser. The token is exchanged, the server shuts down, and the linked Plaid Item is saved.
* Running `login` again adds another linked institution instead of replacing previously saved Items.

### 3. List Accounts & Balances

Fetch accounts linked to all authenticated Plaid Items in your config:

```bash
.\plaid-cli.exe accounts
```

### 4. Sync Transactions

Download and merge the latest transaction changes (runs cursor-based pagination):

```bash
# Incremental sync (downloads changes since last sync)
.\plaid-cli.exe sync

# Full historical sync (resets the local cursor and caches all transactions from scratch)
.\plaid-cli.exe sync --reset

# Sync only one linked Plaid Item
.\plaid-cli.exe sync --item-id "item_id_here"

# Resolve an account to its parent Item and sync only that feed
.\plaid-cli.exe sync --account-id "account_id_here"
```

### 5. View, Filter, and Export Transactions

Filter your cached transactions locally without making additional Plaid API requests:

```bash
# List the last 10 transactions
.\plaid-cli.exe transactions --limit 10

# Filter by the last N days (e.g. 30 days)
.\plaid-cli.exe transactions --days 30

# Prompt interactively for last 30, 60, 90 days, or all time
# (Runs automatically in interactive terminals if no date flags are provided)
.\plaid-cli.exe transactions

# Filter transactions containing the name "Uber" (case-insensitive)
.\plaid-cli.exe transactions --search "Uber"

# Filter by date ranges (YYYY-MM-DD)
.\plaid-cli.exe transactions --start-date "2026-01-01" --end-date "2026-03-31"

# Filter by min/max transaction amounts
.\plaid-cli.exe transactions --min-amount 5.00 --max-amount 150.00

# Filter by account ID
.\plaid-cli.exe transactions --account-id "account_id_here"

# Show only pending transactions
.\plaid-cli.exe transactions --pending

# Export results to a JSON file
.\plaid-cli.exe transactions --format json --output transactions_report.json

# Export results to a CSV file
.\plaid-cli.exe transactions --format csv --output export.csv

# Write JSON to stdout for scripting
.\plaid-cli.exe transactions --days 30 --format json
```

If you do not provide `--days`, `--start-date`, or `--end-date` in an interactive terminal, the CLI prompts you to choose `30`, `60`, `90`, or all transactions.

---

## 📂 Configuration Storage

Configurations are stored in your user home directory:

- **Config file**: `~/.plaid-cli/config.json`
- **Cache file**: `~/.plaid-cli/cache.json`

Both files are automatically created with permissions restricted to the current system user.

When secure mode is enabled with `configure --secure`:

- `config.json` and `cache.json` are stored as encrypted JSON envelopes instead of plaintext records.
- The CLI prompts for your master password when it needs to read or write encrypted data, unless `PLAID_CLI_PASSWORD` is set.
- A machine-local encrypted session file is stored at `~/.plaid-cli/session.json` to cache the password for 15 minutes and reduce repeated prompts.
- If decryption fails, the cached session is cleared and you are prompted again on the next encrypted operation.

If secure mode is disabled, `config.json` and `cache.json` remain plaintext JSON files under the same directory.
