package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/satraul/bca-go"
	"github.com/satraul/gofirefly"
	"github.com/shopspring/decimal"
)

const (
	reconciliationTimeLayout = "January 2, 2006"
)

func createFireflyTransactions(ctx context.Context, bal bca.Balance, trxs []bca.Entry) error {
	ff := gofirefly.NewAPIClient(&gofirefly.APIConfiguration{
		DefaultHeader: make(map[string]string),
		UserAgent:     "OpenAPI-Generator/1.0.0/go",
		Debug:         false,
		Servers: gofirefly.ServerConfigurations{
			{
				URL: fireflyUrl,
			},
		},
		OperationServers: map[string]gofirefly.ServerConfigurations{},
	})
	auth := context.WithValue(ctx, gofirefly.ContextAccessToken, fireflyToken)

	account, err := getFireflyAccount(ff, auth)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}

	for _, trx := range trxs {
		err := createFireflyTransaction(trx, account, ff, auth)
		if err != nil {
			return fmt.Errorf("failed to create firefly transaction: %w", err)
		}
	}

	fmt.Printf("%d firefly transaction(s) were successfully created\n", len(trxs))

	account, err = getFireflyAccountByID(ff, auth, account.Id)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}

	if !noadjust {
		ffBalance, err := decimal.NewFromString(*account.Attributes.CurrentBalance)
		if err != nil {
			return fmt.Errorf("cannot parse decimal from firefly balance: %w", err)
		}
		if bal.Balance.Equal(ffBalance) {
			return nil
		}
		err = createFireflyReconciliation(ffBalance, account.Id, bal, ff, auth)
		if err != nil {
			return fmt.Errorf("failed to create firefly reconciliation: %w", err)
		}
		fmt.Printf("firefly reconciliation successfully created\n")
	}

	return nil
}

func getFireflyAccount(ff *gofirefly.APIClient, auth context.Context) (*gofirefly.AccountRead, error) {
	ac, resp, err := ff.SearchApi.SearchAccounts(auth).
		Field("name").
		Query(accountName).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to search account %q", accountName)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status code not OK with query %q response %q", accountName, string(b))
	}
	if len(ac.Data) == 0 {
		return nil, fmt.Errorf("no accounts found with name %q", accountName)
	}
	return &ac.Data[0], nil
}

func getFireflyAccountByID(ff *gofirefly.APIClient, auth context.Context, id string) (*gofirefly.AccountRead, error) {
	ac, resp, err := ff.AccountsApi.GetAccount(auth, stringToInt32(id)).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to search account %q", accountName)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status code not OK with query %q response %q", accountName, string(b))
	}
	return &ac.Data, nil
}

func createFireflyReconciliation(ffBalance decimal.Decimal, accountID string, bal bca.Balance, ff *gofirefly.APIClient, auth context.Context) error {
	recAcc, err := getReconciliationAccount(ff, auth)
	if err != nil {
		return fmt.Errorf("failed to get reconciliation account: %w", err)
	}

	fftrx := toFireflyReconciliationTrx(ffBalance, bal, accountID, recAcc.Id)

	return storeTransaction(ff, auth, fftrx)
}

func createFireflyTransaction(trx bca.Entry, account *gofirefly.AccountRead, ff *gofirefly.APIClient, auth context.Context) error {
	fftrx := toFireflyTrx(trx, account.Id)

	return storeTransaction(ff, auth, fftrx)
}

func storeTransaction(ff *gofirefly.APIClient, auth context.Context, fftrx gofirefly.TransactionSplitStore) error {
	_, resp, err := ff.TransactionsApi.
		StoreTransaction(auth).
		TransactionStore(*gofirefly.NewTransactionStore([]gofirefly.TransactionSplitStore{fftrx})).
		Execute()

	if err != nil {
		b, _ := io.ReadAll(resp.Body)
		defer resp.Body.Close()
		rb, _ := json.Marshal(fftrx)
		return fmt.Errorf("err with request %q response %q: %w", string(rb), string(b), err)
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		defer resp.Body.Close()
		rb, _ := json.Marshal(fftrx)
		return fmt.Errorf("status code not OK with request %q response %q", string(rb), string(b))
	}
	return nil
}

func toFireflyReconciliationTrx(ffBalance decimal.Decimal, bal bca.Balance, accountID, recAccID string) gofirefly.TransactionSplitStore {
	amount := bal.Balance.Sub(ffBalance)
	t := time.Now()
	to := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	from := to.AddDate(0, 0, -days)
	description := fmt.Sprintf("Reconciliation (%s to %s)", from.Format(reconciliationTimeLayout), to.Format(reconciliationTimeLayout))
	reconciled := true
	fftrx := gofirefly.TransactionSplitStore{
		Type:        "reconciliation",
		Date:        to,
		Amount:      amount.Abs().String(),
		Description: description,
		Reconciled:  &reconciled,
	}

	switch {
	case amount.IsPositive():
		fftrx.SourceId = *gofirefly.NewNullableString(&recAccID)
		fftrx.DestinationId = *gofirefly.NewNullableString(&accountID)
	default:
		fftrx.SourceId = *gofirefly.NewNullableString(&accountID)
		fftrx.DestinationId = *gofirefly.NewNullableString(&recAccID)
	}

	return fftrx
}

func toFireflyTrx(trx bca.Entry, accountID string) gofirefly.TransactionSplitStore {
	fftrx := gofirefly.TransactionSplitStore{}
	fftrx.Amount = trx.Amount.String()

	switch {
	case trx.Date.IsZero():
		fftrx.Date = time.Now()
	default:
		fftrx.Date = trx.Date
	}

	switch trx.Type {
	case "DB":
		fftrx.Type = "withdrawal"
		fftrx.SourceId = *gofirefly.NewNullableString(&accountID)
		fftrx.DestinationName = *gofirefly.NewNullableString(&trx.Payee)
	default:
		fftrx.Type = "deposit"
		fftrx.SourceName = *gofirefly.NewNullableString(&trx.Payee)
		fftrx.DestinationId = *gofirefly.NewNullableString(&accountID)
	}

	switch {
	case trx.Description != "":
		fftrx.Description = trx.Description
	default:
		fftrx.Description = trx.Payee
	}

	return fftrx
}

func getReconciliationAccount(ff *gofirefly.APIClient, auth context.Context) (*gofirefly.AccountRead, error) {
	ac, resp, err := ff.SearchApi.SearchAccounts(auth).
		Field("name").
		Query(accountName).
		Type_(gofirefly.ACCOUNT_RECONCILIATION_ACCOUNT).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to search reconciliation account %q", accountName)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status code not OK with query %q response %q", accountName, string(b))
	}
	if len(ac.Data) == 0 {
		return nil, fmt.Errorf("no accounts found with name %q", accountName)
	}
	return &ac.Data[0], nil
}

func stringToInt32(s string) int32 {
	i, _ := strconv.Atoi(s)
	return int32(i)
}
