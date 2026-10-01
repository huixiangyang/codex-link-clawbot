package statefile

import (
	"errors"
	"testing"
)

func TestWrappedErrorPreservesCategoryAndCause(t *testing.T) {
	cause := errors.New("private detail")
	err := wrap(CategoryCapacity, "write", "/private/path", cause)
	if ErrorCategory(err) != CategoryCapacity || !errors.Is(err, cause) {
		t.Fatalf("wrapped error lost category or cause: %v", err)
	}
	if wrap(CategoryCapacity, "write", "/private/path", nil) != nil {
		t.Fatal("successful operation produced an error")
	}
}
