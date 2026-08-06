package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSendReceiptSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "send-api-state.json")
	store, err := NewSendReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.Reserve("dashboard", "request-key-0001", receiptTestHash("a"))
	if err != nil || reservation.Duplicate {
		t.Fatalf("reserve=%#v err=%v", reservation, err)
	}
	if err := store.Complete(reservation.ID, ReceiptSucceeded); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewSendReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := restarted.Reserve("dashboard", "request-key-0001", receiptTestHash("a"))
	if err != nil || !duplicate.Duplicate || duplicate.Outcome != string(ReceiptSucceeded) {
		t.Fatalf("duplicate=%#v err=%v", duplicate, err)
	}
	if _, err := restarted.Reserve("dashboard", "request-key-0001", receiptTestHash("b")); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error=%v", err)
	}
}

func TestSendReceiptConcurrentReserveHasSingleWinner(t *testing.T) {
	store, err := NewSendReceiptStore(filepath.Join(t.TempDir(), "send-api-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wait sync.WaitGroup
	wait.Add(workers)
	results := make(chan ReceiptReservation, workers)
	errorsChannel := make(chan error, workers)
	for range workers {
		go func() {
			defer wait.Done()
			result, reserveErr := store.Reserve("dashboard", "request-key-0001", receiptTestHash("same"))
			results <- result
			errorsChannel <- reserveErr
		}()
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	winners := 0
	for result := range results {
		if !result.Duplicate {
			winners++
		}
	}
	for reserveErr := range errorsChannel {
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
	}
	if winners != 1 {
		t.Fatalf("reservation winners=%d, want 1", winners)
	}
}

func TestSendReceiptRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "send-api-state.json")
	data := `{"version":1,"receipts":{},"unknown":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSendReceiptStore(path); err == nil {
		t.Fatal("unknown send receipt schema was accepted")
	}
}

func receiptTestHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
