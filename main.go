package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log" // TODO Implement https://godoc.org/github.com/apex/log/handlers/cli
	"net/http"
	"os"
	"reflect"
	"syscall"
	"time"

	"go.bmvs.io/ynab/api"
	"go.bmvs.io/ynab/api/account"
	"go.bmvs.io/ynab/api/category"
	"go.bmvs.io/ynab/api/transaction"

	"github.com/cnf/structhash"
	"github.com/gocarina/gocsv"
	"github.com/satraul/bca-go"
	"github.com/shopspring/decimal"
	"go.bmvs.io/ynab"

	"github.com/pkg/errors"
	"github.com/shibukawa/configdir"
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	errEmpty               = errors.New("empty input")
	errEmptyNonInteractive = errors.New("non-interactive but -u, -p and -t or environment variables not set")
	configDirs             = configdir.New("satraul", "bca-sync-ynab")
)

type config struct {
	BCAUser     string `json:"bcaUser"`
	BCAPassword string `json:"bcaPassword"`
	YNABToken   string `json:"ynabToken"`
}

var (
	noadjust, delete, noninteractive, nostore, reset, csvFlag bool
	accountName, budget, password, token, username            string
)

func main() {
	time.Local = time.FixedZone("WIB", +7*60*60)

	app := &cli.App{
		Compiled:             time.Now(),
		Copyright:            "(c) 2020 Ahmad Satryaji Aulia",
		Description:          "Synchronize your BCA transactions with YNAB",
		EnableBashCompletion: true,
		Version:              "v1.2.0",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "username",
				Aliases:     []string{"u"},
				Usage:       "username for klikbca https://klikbca.com/. can be set from environment variable",
				Destination: &username,
				EnvVars:     []string{"BCA_USERNAME"},
				DefaultText: "-",
			},
			&cli.StringFlag{
				Name:        "password",
				Aliases:     []string{"p"},
				Usage:       "password for klikbca https://klikbca.com/. can be set from environment variable",
				Destination: &password,
				EnvVars:     []string{"BCA_PASSWORD"},
				DefaultText: "-",
			},
			&cli.StringFlag{
				Name:        "token",
				Aliases:     []string{"t"},
				Usage:       "ynab personal access token https://app.youneedabudget.com/settings/developer. can be set from environment variable",
				Destination: &token,
				EnvVars:     []string{"YNAB_TOKEN"},
				DefaultText: "-",
			},
			&cli.StringFlag{
				Name:        "account",
				Aliases:     []string{"a"},
				Value:       "BCA",
				Usage:       "ynab account name",
				Destination: &accountName,
			},
			&cli.StringFlag{
				Name:        "budget",
				Aliases:     []string{"b"},
				Value:       "last-used",
				Usage:       "ynab budget ID",
				Destination: &budget,
			},
			&cli.BoolFlag{
				Name:        "reset",
				Aliases:     []string{"r"},
				Value:       false,
				Usage:       "reset credentials anew",
				Destination: &reset,
			},
			&cli.BoolFlag{
				Name:        "delete",
				Aliases:     []string{"d"},
				Value:       false,
				Usage:       "delete credentials",
				Destination: &delete,
			},
			&cli.BoolFlag{
				Name:        "no-adjust",
				Value:       false,
				Usage:       "don't create balance adjustment if applicable after creating transactions",
				Destination: &noadjust,
			},
			&cli.BoolFlag{
				Name:        "no-store",
				Value:       false,
				Usage:       "don't store credentials",
				Destination: &nostore,
			},
			&cli.BoolFlag{
				Name:        "non-interactive",
				Value:       false,
				Usage:       "do not read from stdin and do not read/store credentials file. used with -u, -p and -t or environment variables",
				Destination: &noninteractive,
			},
			&cli.BoolFlag{
				Name:        "csv",
				Value:       false,
				Usage:       "instead of creating ynab transactions, generate a csv",
				Destination: &csvFlag,
			},
		},
		Action: actionFunc,
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func actionFunc(c *cli.Context) error {
	config, err := getOrDeleteConfig(username, password, token, delete, noninteractive, reset, nostore)
	if err != nil {
		return err
	}
	if config == nil {
		return nil
	}

	var (
		bc  = bca.NewAPIClient(bca.NewConfiguration())
		ctx = c.Context
		ip  = getPublicIP()
	)

	auth, err := bc.Login(ctx, config.BCAUser, config.BCAPassword, ip)
	if err != nil {
		return errors.Wrap(err, "failed to get bca login")
	}
	defer bc.Logout(ctx, auth)

	trxs, err := getBCATransactions(ctx, bc, auth)
	if err != nil {
		return err
	}

	if csvFlag {
		trxCsv, err := transactionsToCsv(trxs)
		if err != nil {
			return fmt.Errorf("enable to csv marshal string: %w", err)
		}
		fmt.Print(trxCsv)
		return nil
	}

	var (
		yc = ynab.NewClient(config.YNABToken)
	)

	a, err := getYNABAccount(yc, budget, accountName)
	if err != nil {
		return err
	}

	if err := createYNABTransactions(yc, trxs, a, budget); err != nil {
		return fmt.Errorf("failed to create ynab transactions: %w", err)
	}

	if !noadjust {
		if err := createYNABBalancaAdjustment(bc, ctx, auth, yc, budget, a); err != nil {
			return fmt.Errorf("failed to create balance adjustment: %w", err)
		}
	}

	return nil
}

func getOrDeleteConfig(username string, password string, token string, delete bool, noninteractive bool, reset bool, nostore bool) (*config, error) {
	var (
		config = config{BCAUser: username, BCAPassword: password, YNABToken: token}
		folder = configDirs.QueryFolderContainsFile("credentials")
	)

	if delete {
		if folder != nil {
			if err := os.RemoveAll(folder.Path); err != nil {
				return nil, errors.Wrap(err, "failed to delete")
			}
			fmt.Printf("credentials file in %s has been deleted\n", folder.Path)
			return nil, nil
		}
		fmt.Println("credentials file already inexistant")
		return nil, nil
	}

	if noninteractive || reset || folder == nil {
		readConfig(noninteractive, nostore, &config)
	} else {
		data, _ := folder.ReadFile("credentials")
		json.Unmarshal(data, &config)
	}
	return &config, nil
}

func getBCATransactions(ctx context.Context, bc *bca.BCAApiService, auth []*http.Cookie) ([]bca.Entry, error) {
	var (
		end   = time.Now()
		start = end.AddDate(0, 0, -27)
	)
	trxs, err := bc.AccountStatementView(ctx, start, end, auth)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get bca transactions. try -r")
	}
	if len(trxs) == 0 {
		return nil, fmt.Errorf("no bca transactions from %s to %s\n", start, end)
	}
	return trxs, err
}

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

func createYNABBalancaAdjustment(bc *bca.BCAApiService, ctx context.Context, auth []*http.Cookie, yc ynab.ClientServicer, budget string, a *account.Account) error {
	bal, err := bc.BalanceInquiry(ctx, auth)
	if err != nil {
		return errors.Wrap(err, "failed to get bca balance")
	}
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

func transactionsToCsv(trxs []bca.Entry) (string, error) {
	gocsv.TagName = "json"
	gocsv.SetCSVWriter(func(out io.Writer) *gocsv.SafeCSVWriter {
		writer := csv.NewWriter(out)
		return gocsv.NewSafeCSVWriter(writer)
	})

	ps := make([]transaction.PayloadTransaction, 0)
	for _, trx := range trxs {
		ps = append(ps, toPayloadTransaction(trx, ""))
	}

	trxCsv, err := gocsv.MarshalString(&trxs)
	return trxCsv, err
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

// clearDate ref: https://cekmutasi.co.id/news/6/jadwal-jam-cut-off-jam-aktif-mutasi-ibanking
func clearDate(now time.Time) time.Time {
	rounded := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch now.Weekday() {
	case time.Friday:
		if now.Hour() < 22 {
			return rounded
		}
		return rounded.AddDate(0, 0, 3)
	case time.Saturday:
		return rounded.AddDate(0, 0, 2)
	case time.Sunday:
		return rounded.AddDate(0, 0, 1)
	default:
		if now.Hour() < 22 {
			return rounded
		}
		return rounded.AddDate(0, 0, 1)
	}
}

func readConfig(noninteractive, nostore bool, c *config) error {
	if isZero(c.BCAUser) {
		if noninteractive {
			panic(errEmpty)
		}

		fmt.Print("Enter KlikBCA Username: ")
		byteUser, _, err := bufio.NewReader(os.Stdin).ReadLine()
		if err != nil {
			panic(err)
		}
		c.BCAUser = string(byteUser)

		if isZero(c.BCAUser) {
			panic(errEmpty)
		}
	}

	if isZero(c.BCAPassword) {
		if noninteractive {
			panic(errEmpty)
		}
		fmt.Print("Enter KlikBCA Password: ")
		bytePassword, err := terminal.ReadPassword(int(syscall.Stdin))
		if err != nil {
			panic(err)
		}
		c.BCAPassword = string(bytePassword)

		if isZero(c.BCAPassword) {
			panic(errEmpty)
		}

		fmt.Println()
	}

	if isZero(c.YNABToken) {
		if noninteractive {
			panic(errEmpty)
		}
		fmt.Print("Enter YNAB Personal Access Token: ")
		byteToken, err := terminal.ReadPassword(int(syscall.Stdin))
		if err != nil {
			panic(err)
		}
		c.YNABToken = string(byteToken)

		if isZero(c.YNABToken) {
			panic(errEmpty)
		}

		fmt.Println()
	}

	if noninteractive || nostore {
		return nil
	}

	// store credentials to user configdir
	folders := configDirs.QueryFolders(configdir.Global)
	data, _ := json.Marshal(&c)
	folders[0].WriteFile("credentials", data)
	fmt.Printf("saved credentials to %s. use -d to delete or -r to reset anew\n", folders[0].Path)

	return nil
}

func isZero(i interface{}) bool {
	return reflect.ValueOf(i).IsZero()
}

// getPublicIP ref: https://gist.github.com/ankanch/8c8ec5aaf374039504946e7e2b2cdf7f
func getPublicIP() string {
	url := "https://api.ipify.org?format=text"

	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	ip, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	return string(ip)
}
