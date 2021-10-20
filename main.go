package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log" // TODO Implement https://godoc.org/github.com/apex/log/handlers/cli
	"net/http"
	"os"
	"time"

	"go.bmvs.io/ynab/api/transaction"

	"github.com/gocarina/gocsv"
	"github.com/satraul/bca-go"
	"go.bmvs.io/ynab"

	"github.com/pkg/errors"
	"github.com/shibukawa/configdir"
	"github.com/urfave/cli/v2"
)

var (
	errEmpty               = errors.New("empty input")
	errEmptyNonInteractive = errors.New("non-interactive but -u, -p and -t or environment variables not set")
	configDirs             = configdir.New("satraul", "bca-sync-ynab")
)

var (
	noadjust, delete, noninteractive, nostore, reset, csvFlag                bool
	accountName, budget, password, token, username, fireflyUrl, fireflyToken string
	days                                                                     int
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
			&cli.StringFlag{
				Name:        "firefly-url",
				Aliases:     []string{"f"},
				Usage:       "instead of creating ynab transactions, post to firefly iii url",
				Destination: &fireflyUrl,
			},
			&cli.StringFlag{
				Name:        "firefly-token",
				Aliases:     []string{"T"},
				Usage:       "firefly iii oauth token for use with -f / --firefly-url",
				Destination: &fireflyToken,
			},
			&cli.IntFlag{
				Name:        "days",
				Aliases:     []string{"n"},
				Value:       27,
				Usage:       "fetch transactions from n number of days ago (0 to 27 inclusive)",
				Destination: &days,
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

	if fireflyUrl != "" {
		err := createFireflyTransactions(trxs, ctx)
		if err != nil {
			return fmt.Errorf("failed to create firefly transactions: %w", err)
		}
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

func getBCATransactions(ctx context.Context, bc *bca.BCAApiService, auth []*http.Cookie) ([]bca.Entry, error) {
	if days > 27 {
		days = 27
	}
	if days < 0 {
		days = 0
	}
	var (
		end   = time.Now()
		start = end.AddDate(0, 0, -days)
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
