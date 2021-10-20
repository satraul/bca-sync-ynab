# bca-sync-ynab

bca-sync-ynab is a CLI that can synchronize your [BCA](https://www.bca.co.id/) transactions with [YNAB](https://www.youneedabudget.com/).

## Installation

Download a pre-built binary from [the Releases page](https://github.com/satraul/bca-sync-ynab/releases/latest).

Or build from source:

```bash
go get github.com/satraul/bca-sync-ynab
go install github.com/satraul/bca-sync-ynab
```

## Usage

Without any arguments `bca-sync-ynab` will interactively ask for credentials, sync your BCA transactions with YNAB and create a balance adjustment at the end.

By default, credentials will be stored in your user-level configuration folder. This behavior, and others, can be modified with flags:

```
   --username value, -u value       username for klikbca https://klikbca.com/. can be set from environment variable (default: -) [%BCA_USERNAME%]
   --password value, -p value       password for klikbca https://klikbca.com/. can be set from environment variable (default: -) [%BCA_PASSWORD%]
   --token value, -t value          ynab personal access token https://app.youneedabudget.com/settings/developer. can be set from environment variable (default: -) [%YNAB_TOKEN%]
   --account value, -a value        ynab account name (default: "BCA")
   --budget value, -b value         ynab budget ID (default: "last-used")
   --reset, -r                      reset credentials anew (default: false)
   --delete, -d                     delete credentials (default: false)
   --no-adjust                      don't create balance adjustment if applicable after creating transactions (default: false)
   --no-store                       don't store credentials (default: false)
   --non-interactive                do not read from stdin and do not read/store credentials file. used with -u, -p and -t or environment variables (default: false)
   --csv                            instead of creating ynab transactions, generate a csv (default: false)
   --firefly-url value, -f value    instead of creating ynab transactions, post to firefly iii url
   --firefly-token value, -T value  firefly iii oauth token for use with -f / --firefly-url
   --days value, -n value           fetch transactions from n number of days ago (0 to 27 inclusive) (default: 27)
   --help, -h                       show help (default: false)
   --version, -v                    print the version (default: false)
```

Example for non-interactive use:

```bash
bca-sync-ynab --non-interactive -u USERNAME -p PASSWORD -t TOKEN
```

## Contributing
Pull requests are welcome.

## License
[MIT](LICENSE)