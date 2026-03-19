# AllIsMail

A bulk email validation service written in Go. Upload a CSV of email addresses and AllIsMail runs them through a chain of validators — from syntax checking to live SMTP probing — to determine whether each address can actually receive mail.

## What it does

AllIsMail validates emails through six layers, ordered from cheapest to most expensive:

```
1. Format validation     — syntax, length limits, TLD presence
2. Disposable detection  — checks against ~3,000 known disposable domains
3. Role address check    — flags addresses like admin@, noreply@, support@
4. MX lookup             — DNS query to confirm the domain has mail servers
5. Catch-all detection   — SMTP probe to check if the server accepts everything
6. SMTP verification     — live SMTP check to confirm the specific mailbox exists
```

Each validator runs in order. If a cheap check fails, the expensive network checks are skipped.

## Project structure

```
allismail/
├── main.go                  — HTTP server, routing, worker pool, all validators
├── index.html               — frontend UI
├── disposable_domains.txt   — blocklist of ~3,000 disposable email domains
└── go.mod
```

## Prerequisites

- Go 1.21 or later
- Port 25 unblocked (required for SMTP validation)
  - On AWS: submit a support request to remove email sending limitations
  - On local machine: works out of the box

## Setup

**1. Clone the repo**

```bash
git clone https://github.com/yourname/allismail
cd allismail
```

**2. Download the disposable domains list**

```bash
curl -o disposable_domains.txt \
  https://raw.githubusercontent.com/disposable-email-domains/disposable-email-domains/master/disposable_email_blocklist.conf
```

**3. Install dependencies**

```bash
go mod tidy
```

**4. Run the server**

```bash
go run main.go
```

Server starts at `http://localhost:4000`.

## Usage

### Via the UI

Open `http://localhost:4000` in your browser. Upload a CSV file and click **Upload & Verify**. Results appear inline with per-row copy buttons and a bulk export option.

### CSV format

The CSV must have `Email` and `Name` column headers. Column order does not matter — the server finds columns by name.

```csv
Email,Name
bob@example.com,Bob Smith
alice@gmail.com,Alice Jones
test@mailinator.com,Test User
```

### Via curl

```bash
curl -s -X POST http://localhost:4000/verify \
  -F "file=@your_list.csv" | jq .
```

### Response format

```json
[
  {
    "Record": {
      "Email": "bob@gmail.com",
      "Name": "Bob Smith"
    },
    "Errors": [],
    "IsValid": true
  },
  {
    "Record": {
      "Email": "test@mailinator.com",
      "Name": "Test User"
    },
    "Errors": ["disposable email domain: mailinator.com"],
    "IsValid": false
  }
]
```

## Validator chain

### 1. Format validation
Checks email syntax using Go's `net/mail.ParseAddress` plus additional RFC 5321 rules:
- Rejects display name format (`Bob <bob@example.com>`)
- Local part must be ≤ 64 characters
- Full address must be ≤ 254 characters
- Domain must have a valid TLD

### 2. Disposable domain detection
Checks the domain against a community-maintained blocklist of ~3,000 disposable and temporary email providers (mailinator.com, guerrillamail.com, tempmail.com etc).

The list is embedded into the binary at compile time using Go's `//go:embed` directive — no file I/O at runtime, loaded into a `map[string]struct{}` at startup for O(1) lookups.

To update the list:
```bash
curl -o disposable_domains.txt \
  https://raw.githubusercontent.com/disposable-email-domains/disposable-email-domains/master/disposable_email_blocklist.conf
```
Then rebuild.

### 3. Role address detection
Flags addresses where the local part is a role rather than a real person:
```
admin, support, info, sales, contact, noreply, no-reply
```
These are valid deliverable addresses but not real individuals.

### 4. MX lookup
Fires a DNS query asking "does this domain have mail servers configured?" Three failure cases are handled:
- `NXDOMAIN` — domain does not exist at all
- Empty result — domain exists but has no MX records
- Null MX (RFC 7505) — domain explicitly publishes `.` as its mail server, meaning it intentionally accepts no email (e.g. `example.com`)

On success, the primary MX host is stored in the validation context and reused by the SMTP validators — no duplicate DNS lookups.

### 5. Catch-all detection
Opens an SMTP connection to the mail server and sends a `RCPT TO` for an address guaranteed not to exist:
```
doesnotexist123456789@domain.com
```
If the server responds `250`, it accepts all email regardless of whether the mailbox exists. The email is marked `risky` (not `invalid`) — mail gets delivered, but the specific mailbox cannot be verified. The SMTP check is skipped.

If the server responds `550`, it validates mailboxes individually and the SMTP check can be trusted.

### 6. SMTP verification
Opens an SMTP connection and issues `RCPT TO` for the real address. The conversation:
```
EHLO allismail.com
MAIL FROM:<verify@allismail.com>
RCPT TO:<the-address-being-checked@domain.com>
```
Response codes:
```
250 → mailbox exists     → valid
550 → no such user       → invalid (smtp_reject)
0   → connection failed  → uncertain (smtp_timeout)
```

**Note:** Gmail, Outlook, and Yahoo return `250` for all addresses (catch-all) as an anti-harvesting measure. These will be marked `domain_accepts_all`. This is expected behaviour, not a bug.

**Note:** Port 25 is blocked by default on most cloud providers (AWS, GCP, DigitalOcean). SMTP validation requires port 25 to be unblocked. Format, disposable, role, and MX validation work without it.

## Architecture

### Worker pool
Records are processed concurrently using a worker pool sized to `min(numRecords, runtime.NumCPU())`. Each worker pulls from a jobs channel and sends results to a results channel.

```
records → jobs channel → N workers → results channel → response
```

### Validation context
Each email gets a `ValidationContext` that travels through the entire validator chain:

```go
type ValidationContext struct {
    Record     EmailRecord
    MXHost     string   // set by validateMX, read by catch-all and SMTP
    IsCatchAll bool     // set by validateCatchAll, skips SMTP if true
}
```

This means the MX DNS lookup happens once and is reused by both SMTP validators — no duplicate network calls.

## Frontend

The UI (`index.html`) is a single self-contained HTML file served by the Go server. Features:
- CSV upload with file details card
- Per-row copy button for individual email addresses
- Copy all emails button (respects active search filter)
- Search/filter by name or email
- Export CSV button — downloads `Name, Email, IsValid` (Errors field excluded)
- Toast notifications on copy/export actions

Since the list is embedded at compile time, a new file only takes effect after a rebuild and redeploy.

## Limitations

- **Large providers** (Gmail, Outlook, Yahoo) cannot be SMTP-verified — they accept all RCPT TO as an anti-harvesting measure. Results for these domains will be `domain_accepts_all`.
- **Port 25 blocking** on cloud providers prevents SMTP validation. Format + disposable + MX still work.
- **SMTP is slow** — each connection takes 1–5 seconds. For large lists, the worker pool helps but total processing time scales with list size.
- **Greylisting** — some servers temporarily reject unknown senders with `4xx` codes. These appear as `smtp_timeout`. A retry with backoff would improve accuracy but is not currently implemented.
