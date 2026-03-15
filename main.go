package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
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
	records []EmailRecord
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
		req.records = append(req.records, EmailRecord{
			Email: row[0],
			Name:  row[1],
		})

		records = records[1:]
	}

	fmt.Println(req)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
