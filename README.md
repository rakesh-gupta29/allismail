# AllIsMail

Upload a CSV of emails, get back a verdict on each one. Runs them through syntax checks, disposable domain detection, MX lookups, and live SMTP probing — cheapest checks first, skips the rest if something fails early.

## Setup
```bash
git clone https://github.com/yourname/allismail
cd allismail

# Grab the disposable domains list
curl -o disposable_domains.txt \
  https://raw.githubusercontent.com/disposable-email-domains/disposable-email-domains/master/disposable_email_blocklist.conf

go mod tidy
go run main.go
```

Server runs at `http://localhost:4000`. You'll need port 25 open for SMTP checks — most cloud providers block it by default.

## Usage

Drop a CSV with `Email` and `Name` columns on the UI, or hit the API directly:
```bash
curl -s -X POST http://localhost:4000/verify \
  -F "file=@your_list.csv" | jq .
```

Each result comes back with `IsValid` and an `Errors` array explaining why something failed — disposable domain, no MX records, SMTP rejection, etc.

## Heads up

Gmail, Outlook, and Yahoo accept every `RCPT TO` as an anti-harvesting thing, so those will always come back as `domain_accepts_all`. Not a bug, just how it is.
