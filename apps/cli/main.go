package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/engine"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scraper <job.json>")
		os.Exit(2)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}

	var definition contracts.JobDefinition
	if err := json.Unmarshal(data, &definition); err != nil {
		panic(err)
	}

	store := storage.NewMemoryStore()
	runtime := engine.New(store)
	job, err := runtime.Start(context.Background(), definition)
	if err != nil {
		panic(err)
	}

	out, _ := json.MarshalIndent(job, "", "  ")
	fmt.Println(string(out))
}
