package client

import (
	"context"
	"errors"
	"fmt"
	"plaid-cli/pkg/config"
	"time"

	"github.com/plaid/plaid-go/v20/plaid"
)

// NewPlaidClient initializes a new Plaid client using the provided configuration.
func NewPlaidClient(cfg *config.Config) (*plaid.APIClient, error) {
	if cfg.ClientID == "" || cfg.Secret == "" {
		return nil, fmt.Errorf("client ID and secret must not be empty")
	}

	configuration := plaid.NewConfiguration()
	configuration.AddDefaultHeader("PLAID-CLIENT-ID", cfg.ClientID)
	configuration.AddDefaultHeader("PLAID-SECRET", cfg.Secret)

	switch cfg.Environment {
	case "sandbox":
		configuration.UseEnvironment(plaid.Sandbox)
	case "production":
		configuration.UseEnvironment(plaid.Production)
	default:
		return nil, fmt.Errorf("unsupported Plaid environment: %s", cfg.Environment)
	}

	return plaid.NewAPIClient(configuration), nil
}

// CreateLinkToken generates a Link token for the authentication flow.
func CreateLinkToken(client *plaid.APIClient, redirectURI string) (string, error) {
	ctx := context.Background()

	// Unique client user ID
	user := plaid.LinkTokenCreateRequestUser{
		ClientUserId: "plaid-cli-user-id",
	}

	// Build the link token create request
	request := plaid.NewLinkTokenCreateRequest(
		"Plaid CLI Tool",
		"en",
		[]plaid.CountryCode{plaid.COUNTRYCODE_US, plaid.COUNTRYCODE_CA},
		user,
	)

	// We request 'transactions' product
	request.SetProducts([]plaid.Products{plaid.PRODUCTS_TRANSACTIONS})

	// Request up to 730 days (2 years) of historical transaction data
	transactionsConfig := plaid.NewLinkTokenTransactions()
	transactionsConfig.SetDaysRequested(730)
	request.SetTransactions(*transactionsConfig)

	// Liabilities and Investments are requested as "required if supported" rather
	// than as primary products: institutions that don't offer them still link
	// successfully, while supported institutions initialize that data for the
	// `liabilities` and `investments` commands.
	// https://plaid.com/docs/api/link/#link-token-create-request-required-if-supported-products
	request.SetRequiredIfSupportedProducts([]plaid.Products{
		plaid.PRODUCTS_LIABILITIES,
		plaid.PRODUCTS_INVESTMENTS,
	})

	// Redirect URI is optional but helpful if configured
	if redirectURI != "" {
		request.SetRedirectUri(redirectURI)
	}

	resp, _, err := client.PlaidApi.LinkTokenCreate(ctx).LinkTokenCreateRequest(*request).Execute()
	if err != nil {
		return "", formatError(err)
	}

	return resp.GetLinkToken(), nil
}

// CreateUpdateLinkToken generates a Link token in update mode for an existing Item.
// It is used to re-authenticate an Item and add consent for the given products to one
// that was originally linked without them. Setting the access_token ties the Link
// session to the existing Item; on completion the access_token is unchanged, so the
// caller must NOT exchange a public token afterward.
//
// The products are requested via `additional_consented_products` (with the primary
// `products` array omitted, per update-mode rules). This is the documented resolution
// for the ADDITIONAL_CONSENT_REQUIRED error raised under Data Transparency Messaging:
// consent is collected during the re-link, and the products are not billed until
// their endpoints are called. The caller must pass only products the institution
// actually supports — unlike required_if_supported_products, this field is validated
// at token-create time and Plaid rejects the request if any product is unsupported.
// See: https://plaid.com/docs/link/update-mode/ (Adding consented products) and
// https://plaid.com/docs/link/data-transparency-messaging-migration-guide/
func CreateUpdateLinkToken(client *plaid.APIClient, accessToken, redirectURI string, products []plaid.Products) (string, error) {
	ctx := context.Background()

	user := plaid.LinkTokenCreateRequestUser{
		ClientUserId: "plaid-cli-user-id",
	}

	request := plaid.NewLinkTokenCreateRequest(
		"Plaid CLI Tool",
		"en",
		[]plaid.CountryCode{plaid.COUNTRYCODE_US, plaid.COUNTRYCODE_CA},
		user,
	)

	// Update mode: reference the existing Item by its access token and omit the
	// primary `products` array. Consent for the products to add is collected via
	// additional_consented_products; they are billed only when their endpoints run.
	request.SetAccessToken(accessToken)
	request.SetAdditionalConsentedProducts(products)

	if redirectURI != "" {
		request.SetRedirectUri(redirectURI)
	}

	resp, _, err := client.PlaidApi.LinkTokenCreate(ctx).LinkTokenCreateRequest(*request).Execute()
	if err != nil {
		return "", formatError(err)
	}

	return resp.GetLinkToken(), nil
}

// InstitutionSupportedProducts returns the list of Plaid products the given
// institution supports, via /institutions/get_by_id. Update mode uses this to
// request consent only for products the institution actually offers.
func InstitutionSupportedProducts(client *plaid.APIClient, institutionID string) ([]plaid.Products, error) {
	ctx := context.Background()

	resp, _, err := client.PlaidApi.InstitutionsGetById(ctx).
		InstitutionsGetByIdRequest(*plaid.NewInstitutionsGetByIdRequest(
			institutionID,
			[]plaid.CountryCode{plaid.COUNTRYCODE_US, plaid.COUNTRYCODE_CA},
		)).Execute()
	if err != nil {
		return nil, formatError(err)
	}

	return resp.Institution.Products, nil
}

// ExchangePublicToken exchanges the public token from Plaid Link for a permanent access token and item ID.
func ExchangePublicToken(client *plaid.APIClient, publicToken string) (string, string, error) {
	ctx := context.Background()

	request := plaid.NewItemPublicTokenExchangeRequest(publicToken)
	resp, _, err := client.PlaidApi.ItemPublicTokenExchange(ctx).ItemPublicTokenExchangeRequest(*request).Execute()
	if err != nil {
		return "", "", formatError(err)
	}

	return resp.GetAccessToken(), resp.GetItemId(), nil
}

// FetchAccounts retrieves the list of accounts associated with the stored AccessToken.
func FetchAccounts(client *plaid.APIClient, accessToken string) ([]plaid.AccountBase, error) {
	ctx := context.Background()

	request := plaid.NewAccountsGetRequest(accessToken)
	resp, _, err := client.PlaidApi.AccountsGet(ctx).AccountsGetRequest(*request).Execute()
	if err != nil {
		return nil, formatError(err)
	}

	return resp.GetAccounts(), nil
}

// SyncTransactionsPage fetches a single page of transaction changes starting from the cursor.
func SyncTransactionsPage(client *plaid.APIClient, accessToken string, cursor string) (string, []plaid.Transaction, []plaid.Transaction, []plaid.RemovedTransaction, bool, error) {
	ctx := context.Background()

	request := plaid.NewTransactionsSyncRequest(accessToken)
	if cursor != "" {
		request.SetCursor(cursor)
	}
	// Explicitly request personal_finance_category, Plaid's current categorization
	// taxonomy (the legacy `category` field is deprecated). Included with the base
	// Transactions product at no extra cost.
	options := plaid.NewTransactionsSyncRequestOptions()
	options.SetIncludePersonalFinanceCategory(true)
	request.SetOptions(*options)

	resp, _, err := client.PlaidApi.TransactionsSync(ctx).TransactionsSyncRequest(*request).Execute()
	if err != nil {
		return "", nil, nil, nil, false, formatError(err)
	}

	return resp.GetNextCursor(), resp.GetAdded(), resp.GetModified(), resp.GetRemoved(), resp.GetHasMore(), nil
}

// FetchLiabilities retrieves liability accounts (credit cards, student loans, and
// mortgages) for the given access token via /liabilities/get. The returned response
// also carries the AccountBase list so callers can render account labels without a
// separate /accounts/get call.
func FetchLiabilities(client *plaid.APIClient, accessToken string) (*plaid.LiabilitiesGetResponse, error) {
	ctx := context.Background()

	request := plaid.NewLiabilitiesGetRequest(accessToken)
	resp, _, err := client.PlaidApi.LiabilitiesGet(ctx).LiabilitiesGetRequest(*request).Execute()
	if err != nil {
		return nil, formatError(err)
	}

	return &resp, nil
}

// FetchHoldings retrieves current investment holdings (positions) for the given
// access token via /investments/holdings/get. The response carries the holdings,
// the securities they reference, and the AccountBase list for label rendering.
func FetchHoldings(client *plaid.APIClient, accessToken string) (*plaid.InvestmentsHoldingsGetResponse, error) {
	ctx := context.Background()

	request := plaid.NewInvestmentsHoldingsGetRequest(accessToken)
	resp, _, err := client.PlaidApi.InvestmentsHoldingsGet(ctx).InvestmentsHoldingsGetRequest(*request).Execute()
	if err != nil {
		return nil, formatError(err)
	}

	return &resp, nil
}

// FetchInvestmentTransactions retrieves investment activity (buys, sells,
// dividends, fees, transfers) between startDate and endDate (inclusive,
// YYYY-MM-DD) via /investments/transactions/get. The endpoint is offset-paginated
// rather than cursor-based, so this walks every page within the window. It returns
// the accumulated transactions, the deduped securities they reference, and the
// AccountBase list for label rendering.
func FetchInvestmentTransactions(client *plaid.APIClient, accessToken, startDate, endDate string) ([]plaid.InvestmentTransaction, []plaid.Security, []plaid.AccountBase, error) {
	ctx := context.Background()

	const pageSize int32 = 500
	var (
		all      []plaid.InvestmentTransaction
		accounts []plaid.AccountBase
		offset   int32
	)
	secMap := make(map[string]plaid.Security)

	for {
		request := plaid.NewInvestmentsTransactionsGetRequest(accessToken, startDate, endDate)
		options := plaid.NewInvestmentsTransactionsGetRequestOptions()
		count := pageSize
		off := offset
		options.Count = &count
		options.Offset = &off
		request.Options = options

		resp, _, err := client.PlaidApi.InvestmentsTransactionsGet(ctx).InvestmentsTransactionsGetRequest(*request).Execute()
		if err != nil {
			return nil, nil, nil, formatError(err)
		}

		if offset == 0 {
			accounts = resp.Accounts
		}
		for _, s := range resp.Securities {
			secMap[s.SecurityId] = s
		}
		all = append(all, resp.InvestmentTransactions...)

		offset += int32(len(resp.InvestmentTransactions))
		if len(resp.InvestmentTransactions) == 0 || offset >= resp.TotalInvestmentTransactions {
			break
		}
	}

	securities := make([]plaid.Security, 0, len(secMap))
	for _, s := range secMap {
		securities = append(securities, s)
	}

	return all, securities, accounts, nil
}

// RemoveItem invalidates the access token server-side via /item/remove.
func RemoveItem(client *plaid.APIClient, accessToken string) error {
	ctx := context.Background()
	request := plaid.NewItemRemoveRequest(accessToken)
	_, _, err := client.PlaidApi.ItemRemove(ctx).ItemRemoveRequest(*request).Execute()
	if err != nil {
		return formatError(err)
	}
	return nil
}

// ItemStatus summarizes an Item's data freshness and health from /item/get.
type ItemStatus struct {
	// TransactionsLastSuccessfulUpdate is when Plaid last successfully pulled
	// transactions from the institution (nil if unknown).
	TransactionsLastSuccessfulUpdate *time.Time
	// TransactionsLastFailedUpdate is when Plaid's last attempt failed (nil if none).
	TransactionsLastFailedUpdate *time.Time
	// ErrorCode is the Item-level error (e.g. ITEM_LOGIN_REQUIRED), or "" if healthy.
	ErrorCode string
}

// FetchItemStatus retrieves freshness and health information for an Item via
// /item/get, which is included with every product at no extra cost.
func FetchItemStatus(client *plaid.APIClient, accessToken string) (ItemStatus, error) {
	ctx := context.Background()

	resp, _, err := client.PlaidApi.ItemGet(ctx).ItemGetRequest(*plaid.NewItemGetRequest(accessToken)).Execute()
	if err != nil {
		return ItemStatus{}, formatError(err)
	}

	var st ItemStatus
	if resp.Item.Error.IsSet() && resp.Item.Error.Get() != nil {
		st.ErrorCode = resp.Item.Error.Get().ErrorCode
	}
	if resp.Status.IsSet() && resp.Status.Get() != nil {
		txStatus := resp.Status.Get().Transactions
		if txStatus.IsSet() && txStatus.Get() != nil {
			if t := txStatus.Get().LastSuccessfulUpdate.Get(); t != nil {
				st.TransactionsLastSuccessfulUpdate = t
			}
			if t := txStatus.Get().LastFailedUpdate.Get(); t != nil {
				st.TransactionsLastFailedUpdate = t
			}
		}
	}
	return st, nil
}

// GetInstitutionInfo returns the institution_id and display name for the given access token.
// A missing institution on the item is not an error — it returns empty strings.
func GetInstitutionInfo(client *plaid.APIClient, accessToken string) (institutionID, institutionName string, err error) {
	ctx := context.Background()

	itemResp, _, err := client.PlaidApi.ItemGet(ctx).ItemGetRequest(*plaid.NewItemGetRequest(accessToken)).Execute()
	if err != nil {
		return "", "", formatError(err)
	}

	if !itemResp.Item.InstitutionId.IsSet() || itemResp.Item.InstitutionId.Get() == nil {
		return "", "", nil
	}
	institutionID = *itemResp.Item.InstitutionId.Get()

	instResp, _, err := client.PlaidApi.InstitutionsGetById(ctx).
		InstitutionsGetByIdRequest(*plaid.NewInstitutionsGetByIdRequest(
			institutionID,
			[]plaid.CountryCode{plaid.COUNTRYCODE_US, plaid.COUNTRYCODE_CA},
		)).Execute()
	if err != nil {
		return institutionID, "", formatError(err)
	}

	return institutionID, instResp.Institution.Name, nil
}

// APIError is a structured Plaid API error. Callers can branch on Code via
// IsPlaidErrorCode (e.g. to retry TRANSACTIONS_SYNC_MUTATION_DURING_PAGINATION).
type APIError struct {
	Code    string
	Message string
	Type    string
	Display string
}

func (e *APIError) Error() string {
	display := e.Display
	if display == "" {
		display = "none"
	}
	return fmt.Sprintf("Plaid API Error [%s]: %s (Type: %s, Display: %s)", e.Code, e.Message, e.Type, display)
}

// IsPlaidErrorCode reports whether err wraps a Plaid APIError with the given error_code.
func IsPlaidErrorCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// formatError converts generic Plaid API client errors into structured *APIError values.
func formatError(err error) error {
	if err == nil {
		return nil
	}
	if plaidErr, convertErr := plaid.ToPlaidError(err); convertErr == nil {
		apiErr := &APIError{
			Code:    plaidErr.ErrorCode,
			Message: plaidErr.ErrorMessage,
			Type:    string(plaidErr.ErrorType),
		}
		if plaidErr.DisplayMessage.IsSet() && plaidErr.DisplayMessage.Get() != nil {
			apiErr.Display = *plaidErr.DisplayMessage.Get()
		}
		return apiErr
	}
	return err
}
