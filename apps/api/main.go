package main

import (
	"encoding/json"
	"log"
	"net/http"
)

type health struct {
	Status string `json:"status"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(health{Status: "ok"})
	})

	log.Println("prompt-driven-scraper API listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
