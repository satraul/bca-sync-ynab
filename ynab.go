package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.bmvs.io/ynab/api"
	"go.bmvs.io/ynab/api/account"
	"go.bmvs.io/ynab/api/category"
	"go.bmvs.io/ynab/api/transaction"

	"github.com/cnf/structhash"
	"github.com/satraul/bca-go"
	"github.com/shopspring/decimal"
	"go.bmvs.io/ynab"

	"github.com/pkg/errors"
)

func createYNABTransactions(yc ynab.ClientServicer, trxs []bca.Entry, account *account.Account, budget string) error {
	ps := make([]transaction.PayloadTransaction, 0)
	for _, trx := range trxs {
		ps = append(ps, toPayloadTransaction(trx, account.ID))
	}

	resp, err := yc.Transaction().CreateTransactions(budget, ps)
	if err != nil {
		return err
	}
	if len(resp.DuplicateImportIDs) > 0 {
		fmt.Printf("%d transaction(s) already exists\n", len(resp.DuplicateImportIDs))
	}
	fmt.Printf("%d transaction(s) were successfully created\n", len(resp.TransactionIDs))
	return nil
}

func getYNABAccount(yc ynab.ClientServicer, budget string, accountName string) (*account.Account, error) {
	accs, err := yc.Account().GetAccounts(budget, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ynab accounts. try -r")
	}

	for _, acc := range accs.Accounts {
		if acc.Name == accountName {
			return acc, nil
		}
	}
	return nil, errors.New("couldnt find account " + accountName)
}

func createYNABBalancaAdjustment(bal bca.Balance, ctx context.Context, auth []*http.Cookie, yc ynab.ClientServicer, budget string, a *account.Account) error {
	anew, err := yc.Account().GetAccount(budget, a.ID)
	if err != nil {
		return errors.Wrap(err, "failed to get ynab account")
	}
	delta := bal.Balance.IntPart()*1000 - anew.Balance
	if delta != 0 {
		var (
			payee = "Automated Balance Adjustment"
		)

		c, err := func() (*category.Category, error) {
			cs, err := yc.Category().GetCategories(budget, nil)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get categories")
			}

			for _, group := range cs.GroupWithCategories {
				for _, c := range group.Categories {
					if c.Name == "Inflows" {
						return c, nil
					}
				}
			}
			return nil, errors.New("couldnt find to be budgeted category")
		}()
		if err != nil {
			return err
		}

		_, err = yc.Transaction().CreateTransaction(budget, transaction.PayloadTransaction{
			AccountID: a.ID,
			Date: api.Date{
				Time: time.Now(),
			},
			Amount:     delta,
			Cleared:    transaction.ClearingStatusReconciled,
			Approved:   true,
			PayeeID:    nil,
			PayeeName:  &payee,
			CategoryID: &c.ID,
			Memo:       nil,
			FlagColor:  nil,
			ImportID:   nil,
		})
		if err != nil {
			return errors.Wrap(err, "failed to create balance adjustment transaction")
		}

		fmt.Printf("balance adjustment transaction successfully created\n")
	}
	return nil
}

func toPayloadTransaction(trx bca.Entry, accountID string) transaction.PayloadTransaction {
	// description unreliable for hash
	desc := trx.Description
	trx.Description = ""
	// use predicted clearance date for PEND transactions hash

	if trx.Date.IsZero() {
		trx.Date = clearDate(time.Now())
	}

	var (
		t           = trx.Date
		miliunit    = trx.Amount.Mul(decimal.NewFromInt(1000)).IntPart()
		payee       = trx.Payee
		memo        = desc
		importid, _ = structhash.Hash(trx, 1)
	)
	if t.After(time.Now()) {
		t = time.Now()
	}
	if trx.Type == "DB" {
		miliunit = -miliunit
	}
	p := transaction.PayloadTransaction{
		AccountID: accountID,
		Date: api.Date{
			Time: t,
		},
		Amount:     miliunit,
		Cleared:    transaction.ClearingStatusCleared,
		Approved:   true,
		PayeeID:    nil,
		PayeeName:  &payee,
		CategoryID: nil,
		Memo:       &memo,
		FlagColor:  nil,
		ImportID:   &importid,
	}
	return p
}
