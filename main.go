package main

import (
	"bufio"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

var roleBasedPrefixes = map[string]struct{}{
	"info":    {},
	"support": {},
	"admin":   {},
	"sales":   {},
	"contact": {},
}

func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(APIError{Code: code, Message: message})
}

func route(mux *http.ServeMux, method string, pattern string, h http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h(w, r)
	})
}

func emailParts(email string) (local, domain string) {
	parts := strings.SplitN(email, "@", 2)
	return strings.ToLower(parts[0]), strings.ToLower(parts[1])
}

type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type EmailRecord struct {
	Email string
	Name  string
}

type VerifyRequest struct {
	id      string
	Records []EmailRecord
}

type ValidationResult struct {
	Record  EmailRecord
	Errors  []string
	IsValid bool
}

type ValidationContext struct {
	Record     EmailRecord
	MXHost     string
	IsCatchAll bool
}

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type ValidatorFunc func(record *ValidationContext) error

var req VerifyRequest

var (
	limiters   = make(map[string]*ipLimiter)
	limitersMu sync.Mutex
)

func getLimiter(ip string) *rate.Limiter {
	limitersMu.Lock()

	defer limitersMu.Unlock()
	entry, exists := limiters[ip]
	if !exists {
		entry = &ipLimiter{
			limiter: rate.NewLimiter(rate.Limit(10), 20),
		}
		limiters[ip] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter

}

//go:embed disposable_domains.txt
var disposableDomainsRaw string

var disposableDomains map[string]struct{}

func init() {
	disposableDomains = make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(disposableDomainsRaw))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if line == "" || strings.HasPrefix(scanner.Text(), "#") {
			continue
		}
		disposableDomains[line] = struct{}{}
	}
}

func init() {
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			limitersMu.Lock()
			for ip, entry := range limiters {
				if time.Since(entry.lastSeen) > 5*time.Minute {
					delete(limiters, ip)
				}
			}
			limitersMu.Unlock()
		}
	}()
}

func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip the port so "1.2.3.4:5678" and "1.2.3.4:9999" share a bucket.
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			// why this line
			ip = r.RemoteAddr
			writeJSONError(w, http.StatusBadRequest, "Invalid remote address")
			return
		}
		if !getLimiter(ip).Allow() {
			writeJSONError(w, http.StatusTooManyRequests, "Too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "./index.html")
}

func verifyHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "File is too big")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "file not found")
		return
	}

	defer file.Close()

	reader := csv.NewReader(file)

	// read and discard the header
	if _, err := reader.Read(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read header")
		return
	}

	records, err := reader.ReadAll()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to pares the file")
		return
	}

	for _, row := range records {
		if len(row) < 2 {
			writeJSONError(w, http.StatusBadRequest, "bad row found")
			return
		}

		req.Records = append(req.Records, EmailRecord{
			Email: row[1],
			Name:  row[0],
		})

		records = records[1:]
	}

	validators := []ValidatorFunc{
		validateFormat,
		validateDisposable,
		validateRolesEmails,
		validateMX,
		validateCatchAll,
		validateSMTP,
	}

	numWorkers := min(len(req.Records), runtime.NumCPU())
	results := processRecords(req.Records, validators, numWorkers)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func processRecords(
	records []EmailRecord,
	validators []ValidatorFunc,
	numWorkers int,
) []ValidationResult {
	jobs := make(chan EmailRecord, len(records))
	results := make(chan ValidationResult, len(records))

	// worker pool
	var eg errgroup.Group

	for range numWorkers {
		eg.Go(func() error {
			for record := range jobs {
				result := runValidators(record, validators)
				results <- result
			}
			return nil
		})
	}

	// feeding the jobs
	for _, record := range records {
		jobs <- record
	}

	close(jobs)

	// close the results channel when all the tasks are done.
	go func() {
		eg.Wait()
		close(results)
	}()

	var output []ValidationResult
	for result := range results {
		output = append(output, result)
	}

	return output
}

func validateFormat(ctx *ValidationContext) error {
	addr, err := mail.ParseAddress(ctx.Record.Email)
	if err != nil {
		return fmt.Errorf("invalid email format for %w", err)
	}

	// mail.ParseAddress accepts "John Doe <john@example.com>" — we only want
	// the bare address form, so reject anything with a display name
	if addr.Name != "" {
		return fmt.Errorf("invalid email format: display names are not allowed")
	}
	parts := strings.SplitN(addr.Address, "@", 2)

	local, domain := parts[0], parts[1]

	if len(local) > 64 {
		return fmt.Errorf("invalid length of the email")
	}

	if len(addr.Address) > 254 {
		return fmt.Errorf("invalid email format: address exceeds 254 characters")
	}

	dotIdx := strings.LastIndex(domain, ".")
	if dotIdx == -1 || dotIdx == len(domain)-1 {
		return fmt.Errorf("invalid email format: domain missing or has no TLD")
	}

	return nil
}

func validateDisposable(ctx *ValidationContext) error {
	_, domain := emailParts(ctx.Record.Email)
	if _, found := disposableDomains[domain]; found {
		return fmt.Errorf("disposable email domain: %s", domain)
	}
	return nil
}

func validateRolesEmails(ctx *ValidationContext) error {
	local, _ := emailParts(ctx.Record.Email)
	if _, found := roleBasedPrefixes[local]; found {
		return fmt.Errorf("role_based")
	}
	return nil
}

func validateMX(ctx *ValidationContext) error {
	_, domain := emailParts(ctx.Record.Email)
	mxs, err := net.LookupMX(domain)
	if err != nil {
		return fmt.Errorf("no mail servers found for domain: %s", domain)
	}

	if len(mxs) == 0 {
		return fmt.Errorf("no mail servers found for domain: %s", domain)
	}

	// null MX record (RFC 7505) — domain explicitly accepts no email
	if len(mxs) == 1 && (mxs[0].Host == "." || mxs[0].Host == "") {
		return fmt.Errorf("domain does not accept email: %s", domain)
	}

	// host ends with a '.' so we have to trim that.
	ctx.MXHost = strings.TrimSuffix(mxs[0].Host, ".")

	return nil
}

func validateCatchAll(ctx *ValidationContext) error {
	// check if the validateMX failed and at this step,
	// there is no point in getting ahead
	if ctx.MXHost == "" {
		return nil
	}

	_, domain := emailParts(ctx.Record.Email)
	fakeAddr := "doesnotexist123456789@" + domain
	code := smtpProbe(ctx.MXHost, fakeAddr)
	if code == 250 {
		ctx.IsCatchAll = true
		return fmt.Errorf("domain_accepts_all")
	}

	return nil
}

func validateSMTP(ctx *ValidationContext) error {
	if ctx.MXHost == "" {
		return nil
	}
	if ctx.IsCatchAll {
		return nil
	}
	code := smtpProbe(ctx.MXHost, ctx.Record.Email)
	switch code {
	case 250:
		return nil
	case 550:
		return fmt.Errorf("smtp_reject")
	default:
		return fmt.Errorf("smtp_timeout")
	}
}

func smtpProbe(mxHost, toAddr string) int {
	conn, err := smtp.Dial(mxHost + ":25")
	if err != nil {
		return 0
	}
	defer conn.Close()

	if err := conn.Hello("allismail.com"); err != nil {
		return 0
	}
	if err := conn.Mail("verify@allismail.com"); err != nil {
		return 0
	}
	if err := conn.Rcpt(toAddr); err != nil {
		if strings.HasPrefix(err.Error(), "550") {
			return 550
		}
		return 0
	}

	// reset is the standard practice as per convention; not mandatory though
	// we could have just closed the connection and be done with that.
	conn.Reset()

	return 250
}

func runValidators(record EmailRecord, validators []ValidatorFunc) ValidationResult {
	var result ValidationResult

	result.IsValid = true
	result.Record = record

	ctx := &ValidationContext{
		Record: record,
	}

	for _, validator := range validators {
		if err := validator(ctx); err != nil {
			result.Errors = append(result.Errors, err.Error())
			result.IsValid = false
		}
	}
	return result
}

func main() {
	mux := http.NewServeMux()

	route(mux, http.MethodGet, "/", homeHandler)
	route(mux, http.MethodPost, "/verify", verifyHandler)

	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" {
			writeJSONError(w, http.StatusNotFound, "Resource not found")
			return
		}
		mux.ServeHTTP(w, r)
	})

	handler := rateLimitMiddleware(base)

	server := &http.Server{
		Addr:    ":4000",
		Handler: handler,
	}

	fmt.Println("Server running on http://localhost:4000")
	if err := server.ListenAndServe(); err != nil {
		fmt.Println("Error starting server:", err)
	}
}
