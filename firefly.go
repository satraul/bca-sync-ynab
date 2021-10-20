package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/satraul/bca-go"
	"github.com/satraul/gofirefly"
)

func createFireflyTransactions(trxs []bca.Entry, ctx context.Context) error {
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

	for _, trx := range trxs {
		fftrx := []gofirefly.TransactionSplitStore{
			toFireflyTrx(trx),
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
			return fmt.Errorf("status code not OK with body %q", string(b))
		}
	}

	return nil
}

func toFireflyTrx(trx bca.Entry) gofirefly.TransactionSplitStore {
	var (
		trxType         string
		date            = trx.Date
		sourceName      string
		destinationName string
	)

	switch trx.Type {
	case "DB":
		trxType = "deposit"
		sourceName = trx.Payee
		destinationName = "BCA"
	default:
		trxType = "withdrawal"
		sourceName = "BCA"
		destinationName = trx.Payee
	}

	if trx.Date.IsZero() {
		date = time.Now()
	}
	date = clearDate(date)

	fftrx := gofirefly.TransactionSplitStore{
		Type:            trxType,
		Date:            trx.Date,
		Amount:          trx.Amount.String(),
		Description:     trx.Description,
		SourceName:      *gofirefly.NewNullableString(&sourceName),
		DestinationName: *gofirefly.NewNullableString(&destinationName),
	}
	return fftrx
}
