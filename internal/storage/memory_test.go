package storage_test

import (
	"testing"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/storagetest"
)

func TestMemoryStoreContract(t *testing.T) {
	storagetest.Run(t, func(*testing.T) storage.Store { return storage.NewMemoryStore() })
}
