package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil" // TODO Implement https://godoc.org/github.com/apex/log/handlers/cli
	"net/http"
	"os"
	"reflect"
	"syscall"

	"github.com/pkg/errors"
	"github.com/shibukawa/configdir"
	"golang.org/x/crypto/ssh/terminal"
)

type config struct {
	BCAUser     string `json:"bcaUser"`
	BCAPassword string `json:"bcaPassword"`
	YNABToken   string `json:"ynabToken"`
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
