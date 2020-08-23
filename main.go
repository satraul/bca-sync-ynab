package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"go.bmvs.io/ynab/api"
	"go.bmvs.io/ynab/api/account"
	"go.bmvs.io/ynab/api/category"
	"go.bmvs.io/ynab/api/transaction"

	"github.com/mitchellh/hashstructure"
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
	var (
		adjust         bool
		delete         bool
		noninteractive bool
		nostore        bool
		reset          bool
		accountName    string
		budget         string
		password       string
		token          string
		username       string
	)

	app := &cli.App{
		Compiled:             time.Now(),
		Copyright:            "(c) 2020 Ahmad Satryaji Aulia",
		Description:          "Synchronize your BCA transactions with YNAB",
		EnableBashCompletion: true,
		Version:              "v1.0.0",
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
				Name:        "balance-adjust",
				Value:       true,
				Usage:       "create balance adjustment if applicable after creating transactions",
				Destination: &adjust,
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
				start = time.Now().AddDate(0, 0, -27)
				end   = time.Now()
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
				var (
					t        = trx.Date
					miliunit = trx.Amount.Mul(decimal.NewFromInt(1000)).IntPart()
					payee    = trx.Payee
					memo     = trx.Description
					hash, _  = hashstructure.Hash(trx, nil)
					importid = strconv.FormatUint(hash, 10)
				)
				if t.IsZero() {
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

			if adjust {
				bal, err := bc.BalanceInquiry(ctx, auth)
				if err != nil {
					return errors.Wrap(err, "failed to get bca balance")
				}
				anew, err := yc.Account().GetAccount(budget, a.ID)
				if err != nil {
					return errors.Wrap(err, "failed to get ynab account")
				}
				if bal.Balance.Mul(decimal.NewFromInt(1000)).IntPart() != anew.Balance {
					var (
						miliunit = bal.Balance.Mul(decimal.NewFromInt(1000)).IntPart() - anew.Balance
						payee    = "Manual Balance Adjustment"
					)

					c, err := func() (*category.Category, error) {
						cs, err := yc.Category().GetCategories(budget, nil)
						if err != nil {
							return nil, errors.Wrap(err, "failed to get categories")
						}

						for _, group := range cs.GroupWithCategories {
							for _, c := range group.Categories {
								if c.Name == "Immediate Income SubCategory" {
									return c, nil
								}
							}
						}
						return nil, errors.New("couldnt find to be budgeted category")
					}()
					if err != nil {
						return err
					}

					resp, err := yc.Transaction().CreateTransaction(budget, transaction.PayloadTransaction{
						AccountID: a.ID,
						Date: api.Date{
							Time: time.Now(),
						},
						Amount:     miliunit,
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

					fmt.Printf("balance adjustment transaction %v successfully created\n", resp.Transaction.ID)
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

func readConfig(noninteractive bool, nostore bool, c *config) error {
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
