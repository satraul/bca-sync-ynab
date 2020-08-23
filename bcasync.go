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
	"syscall"
	"time"

	"github.com/satraul/bca-go"

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
		delete         bool
		reset          bool
		noninteractive bool
		nostore        bool
		username       string
		password       string
		token          string
	)

	app := &cli.App{
		Compiled:             time.Now(),
		Copyright:            "(c) 2020 Ahmad Satryaji Aulia",
		EnableBashCompletion: true,
		Version:              "v1.0.0",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:        "reset",
				Aliases:     []string{"r"},
				Value:       false,
				Usage:       "reset credentials anew (default: false)",
				Destination: &reset,
			},
			&cli.BoolFlag{
				Name:        "delete",
				Aliases:     []string{"d"},
				Value:       false,
				Usage:       "delete credentials (default: false)",
				Destination: &delete,
			},
			&cli.StringFlag{
				Name:        "username",
				Aliases:     []string{"u"},
				Usage:       "username for klikbca https://klikbca.com/. can be set from BCA_USERNAME environment variable",
				Destination: &username,
				EnvVars:     []string{"BCA_USERNAME"},
			},
			&cli.StringFlag{
				Name:        "password",
				Aliases:     []string{"p"},
				Usage:       "password for klikbca https://klikbca.com/. can be set from BCA_PASSWORD environment variable",
				Destination: &password,
				EnvVars:     []string{"BCA_PASSWORD"},
			},
			&cli.StringFlag{
				Name:        "token",
				Aliases:     []string{"t"},
				Usage:       "personal access token for ynab https://app.youneedabudget.com/settings/developer. can be set from YNAB_TOKEN environment variable",
				Destination: &token,
				EnvVars:     []string{"YNAB_TOKEN"},
			},
			&cli.BoolFlag{
				Name:        "non-interactive",
				Value:       false,
				Usage:       "do not read from stdin and do not read/store credentials file. used with -u, -p and -t or environment variables (default: false)",
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
				api = bca.NewAPIClient(bca.NewConfiguration())
				ctx = context.Background()
				ip  = getPublicIP()
			)

			auth, err := api.Login(ctx, config.BCAUser, config.BCAPassword, ip)
			if err != nil {
				return errors.Wrap(err, "failed to login")
			}

			trxs, err := api.AccountStatementView(ctx, time.Now().AddDate(0, 0, -7), time.Now(), auth)
			if err != nil {
				return errors.Wrap(err, "failed to get transactions. try bcasync -r")
			}
			for _, trx := range trxs {
				// TODO Import transactions to ynab using https://github.com/brunomvsouza/ynab.go
				fmt.Printf("%+v\n", trx)
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
	reader := bufio.NewReader(os.Stdin)

	if isZero(c.BCAUser) {
		if noninteractive {
			panic(errEmpty)
		}

		fmt.Print("Enter BCA Username (https://klikbca.com/): ")
		username, err := reader.ReadString('\n')
		if err != nil {
			panic(err)
		}
		c.BCAUser = username

		if isZero(c.BCAUser) {
			panic(errEmpty)
		}
	}

	if isZero(c.BCAPassword) {
		if noninteractive {
			panic(errEmpty)
		}
		fmt.Print("Enter BCA Password (https://klikbca.com/): ")
		bytePassword, err := terminal.ReadPassword(int(syscall.Stdin))
		if err != nil {
			panic(err)
		}
		c.BCAPassword = string(bytePassword)

		if isZero(c.BCAPassword) {
			panic(errEmpty)
		}
	}

	if isZero(c.BCAPassword) {
		if noninteractive {
			panic(errEmpty)
		}
		fmt.Print("Enter YNAB Personal Access Token (https://app.youneedabudget.com/settings/developer): ")
		byteToken, err := terminal.ReadPassword(int(syscall.Stdin))
		if err != nil {
			panic(err)
		}
		c.YNABToken = string(byteToken)

		if isZero(c.YNABToken) {
			panic(errEmpty)
		}
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
