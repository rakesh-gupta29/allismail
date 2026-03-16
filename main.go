package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"
)

type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(APIError{Code: code, Message: message})
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

type ValidatorFunc func(record EmailRecord) error

func route(mux *http.ServeMux, method string, pattern string, h http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h(w, r)
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

	var req VerifyRequest

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

func validateFormat(record EmailRecord) error {
	fmt.Println("running the parse for", record.Email)
	addr, err := mail.ParseAddress(record.Email)
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

func validateDisposable(record EmailRecord) error {
	return nil
}

func runValidators(record EmailRecord, validators []ValidatorFunc) ValidationResult {
	var result ValidationResult

	result.IsValid = true
	result.Record = record

	for _, validator := range validators {
		if err := validator(record); err != nil {
			fmt.Println(err)
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

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" {
			writeJSONError(w, http.StatusNotFound, "Resource not found")
			return
		}
		mux.ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:    ":4000",
		Handler: handler,
	}

	fmt.Println("Server running on http://localhost:4000")
	if err := server.ListenAndServe(); err != nil {
		fmt.Println("Error starting server:", err)
	}
}
