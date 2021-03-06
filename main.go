package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

func main() {
	time.Local = time.FixedZone("WIB", +7*60*60)

	var (
		noadjust, delete, noninteractive, nostore, reset bool
		accountName, budget, password, token, username   string
	)

	app := &cli.App{
		Compiled:             time.Now(),
		Copyright:            "(c) 2020 Ahmad Satryaji Aulia",
		Description:          "Synchronize your BCA transactions with YNAB",
		EnableBashCompletion: true,
		Version:              "v1.1.0",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "username",
				Aliases:     []string{"u"},
				Usage:       "username for klikbca https://klikbca.com/. can be set from environment variable",
				Destination: &username,
				EnvVars:     []string{"BCA_USERNAME"},
			},
			&cli.StringFlag{
				Name:        "password",
				Aliases:     []string{"p"},
				Usage:       "password for klikbca https://klikbca.com/. can be set from environment variable",
				Destination: &password,
				EnvVars:     []string{"BCA_PASSWORD"},
			},
			&cli.StringFlag{
				Name:        "token",
				Aliases:     []string{"t"},
				Usage:       "ynab personal access token https://app.youneedabudget.com/settings/developer. can be set from environment variable",
				Destination: &token,
				EnvVars:     []string{"YNAB_TOKEN"},
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
		},
		Action: func(c *cli.Context) error {
			var (
				config = config{BCAUser: username, BCAPassword: password, YNABToken: token}
				folder = configDirs.QueryFolderContainsFile("credentials")
			)

			if delete {
				if folder != nil {
					if err := os.RemoveAll(folder.Path); err != nil {
						return errors.Wrap(err, "failed to delete")
					}
					fmt.Printf("credentials file in %s has been deleted\n", folder.Path)
					return nil
				}
				fmt.Println("credentials file already inexistant")
				return nil
			}

			if noninteractive || reset || folder == nil {
				readConfig(noninteractive, nostore, &config)
			} else {
				data, _ := folder.ReadFile("credentials")
				json.Unmarshal(data, &config)
			}

			var (
				bc  = bca.NewAPIClient(bca.NewConfiguration())
				ctx = context.Background()
				ip  = getPublicIP()
			)

			auth, err := bc.Login(ctx, config.BCAUser, config.BCAPassword, ip)
			if err != nil {
				return errors.Wrap(err, "failed to get bca login")
			}
			defer bc.Logout(ctx, auth)

			var (
				end   = time.Now()
				start = end.AddDate(0, 0, -27) // TODO add flag for number of days and implement batch requests to get around 27-day range limitation
			)
			trxs, err := bc.AccountStatementView(ctx, start, end, auth)
			if err != nil {
				return errors.Wrap(err, "failed to get bca transactions. try -r")
			}
			if len(trxs) == 0 {
				fmt.Printf("no bca transactions from %s to %s\n", start, end)
				return nil
			}

			var (
				yc = ynab.NewClient(config.YNABToken)
			)

			a, err := func() (*account.Account, error) {
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
			}()
			if err != nil {
				return err
			}

			ps := make([]transaction.PayloadTransaction, 0)
			for _, trx := range trxs {
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
					AccountID: a.ID,
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

				ps = append(ps, p)
			}

			resp, err := yc.Transaction().CreateTransactions(budget, ps)
			if err != nil {
				return err
			}
			if len(resp.DuplicateImportIDs) > 0 {
				fmt.Printf("%d transaction(s) already exists\n", len(resp.DuplicateImportIDs))
			}
			fmt.Printf("%d transaction(s) were successfully created\n", len(resp.TransactionIDs))

			if !noadjust {
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
			}

			return nil
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
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
