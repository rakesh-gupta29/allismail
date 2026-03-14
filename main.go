package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// --- Error helpers ---

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
	// TODO: parse CSV, run email validation
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
