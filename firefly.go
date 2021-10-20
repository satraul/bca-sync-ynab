package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
			return err
		}
	}

	if !noadjust {
		err := createFireflyReconciliation(account, bal, ff, auth)
		if err != nil {
			return err
		}
	}

	return nil
}

func createFireflyReconciliation(account *gofirefly.AccountRead, bal bca.Balance, ff *gofirefly.APIClient, auth context.Context) error {
	ffBalance, err := decimal.NewFromString(*account.Attributes.CurrentBalance)
	if err != nil {
		return fmt.Errorf("cannot parse decimal from firefly balance: %w", err)
	}
	if bal.Balance.Equal(ffBalance) {
		return nil
	}

	fftrx := []gofirefly.TransactionSplitStore{
		toFireflyReconciliationTrx(ffBalance, bal, account),
	}

	_, resp, err := ff.TransactionsApi.
		StoreTransaction(auth).
		TransactionStore(*gofirefly.NewTransactionStore(fftrx)).
		Execute()

	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		defer resp.Body.Close()
		rb, _ := json.Marshal(fftrx)
		return fmt.Errorf("status code not OK with request %q response %q", string(rb), string(b))
	}
	return nil
}

func createFireflyTransaction(trx bca.Entry, account *gofirefly.AccountRead, ff *gofirefly.APIClient, auth context.Context) error {
	fftrx := []gofirefly.TransactionSplitStore{
		toFireflyTrx(trx, account.Id),
	}

	_, resp, err := ff.TransactionsApi.
		StoreTransaction(auth).
		TransactionStore(*gofirefly.NewTransactionStore(fftrx)).
		Execute()

	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		defer resp.Body.Close()
		rb, _ := json.Marshal(fftrx)
		return fmt.Errorf("status code not OK with request %q response %q", string(rb), string(b))
	}
	return nil
}

func toFireflyReconciliationTrx(ffBalance decimal.Decimal, bal bca.Balance, account *gofirefly.AccountRead) gofirefly.TransactionSplitStore {
	amount := ffBalance.Sub(bal.Balance).String()
	t := time.Now()
	firstday := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local)
	lastday := firstday.AddDate(0, 1, 0).Add(time.Nanosecond * -1)
	description := fmt.Sprintf("Reconciliation (%s to %s)", firstday.Format(reconciliationTimeLayout), lastday.Format(reconciliationTimeLayout))
	destinationName := fmt.Sprintf("%s reconciliation (%s)", account.Attributes.CurrencyCode)
	fftrx := gofirefly.TransactionSplitStore{
		Type:            "reconciliation",
		Date:            lastday,
		Amount:          amount,
		Description:     description,
		SourceId:        *gofirefly.NewNullableString(&account.Id),
		DestinationId:   gofirefly.NullableString{},
		DestinationName: *gofirefly.NewNullableString(&destinationName),
	}
	return fftrx
}

func getFireflyAccount(ff *gofirefly.APIClient, auth context.Context) (*gofirefly.AccountRead, error) {
	ac, resp, err := ff.SearchApi.SearchAccounts(auth).
		Field("name").
		Query(accountName).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to search account %q", accountName)
	}
	if resp != nil {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status code not OK with query %q response %q", accountName, string(b))
	}
	if len(ac.Data) == 0 {
		return nil, fmt.Errorf("no accounts found with name %q", accountName)
	}
	return &ac.Data[0], nil
}

func toFireflyTrx(trx bca.Entry, accountID string) gofirefly.TransactionSplitStore {
	fftrx := gofirefly.TransactionSplitStore{
		Amount:      trx.Amount.String(),
		Description: trx.Description,
	}

	date := trx.Date
	if trx.Date.IsZero() {
		date = time.Now()
	}
	fftrx.Date = clearDate(date)

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

	return fftrx
}
