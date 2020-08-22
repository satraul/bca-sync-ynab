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
	"syscall"
	"time"

	"github.com/satraul/bca-go"

	"github.com/pkg/errors"
	"github.com/shibukawa/configdir"
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	errEmpty   = errors.New("empty input")
	configDirs = configdir.New("satraul", "bca-sync-ynab")
)

type config struct {
	BCAUser     string `json:"bcaUser"`
	BCAPassword string `json:"bcaPassword"`
	YNABToken   string `json:"ynabToken"`
}

func main() {
	var (
		delete bool
		reset  bool
	)

	app := &cli.App{
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
		},
		Action: func(c *cli.Context) error {
			var config config
			folder := configDirs.QueryFolderContainsFile("credentials")

			if delete {
				if folder != nil {
					if err := os.RemoveAll(folder.Path); err != nil {
						return err
					}
					fmt.Println("credentials file has been deleted:", folder.Path)
					return nil
				}

				fmt.Println("credentials file already inexistant")
				return nil
			}

			if !reset || folder != nil {
				data, _ := folder.ReadFile("credentials")
				json.Unmarshal(data, &config)
			} else {
				readConfig(&config)
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
				return errors.Wrap(err, "failed to get transactions. if recurring, try bcasync --reset")
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

func readConfig(v interface{}) error {
	var config config

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter BCA Username (https://klikbca.com/): ")
	username, err := reader.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if username == "" {
		panic(errEmpty)
	}
	config.BCAUser = username

	fmt.Print("Enter Password: ")
	bytePassword, err := terminal.ReadPassword(int(syscall.Stdin))
	if err != nil {
		panic(err)
	}
	if len(bytePassword) == 0 {
		panic(errEmpty)
	}
	config.BCAPassword = string(bytePassword)

	fmt.Print("Enter YNAB Personal Access Token (https://app.youneedabudget.com/settings/developer): ")
	byteToken, err := terminal.ReadPassword(int(syscall.Stdin))
	if err != nil {
		panic(err)
	}
	if len(byteToken) == 0 {
		panic(errEmpty)
	}
	config.YNABToken = string(byteToken)

	// Stores to user folder
	folders := configDirs.QueryFolders(configdir.Global)
	data, _ := json.Marshal(&config)
	folders[0].WriteFile("credentials", data)

	return json.Unmarshal(data, v)
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
