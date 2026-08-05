package statefile

import (
	"errors"
	"testing"
)

func TestFailureHealthExposesOnlyCategoryAndTime(t *testing.T) {
	ClearLastFailure()
	t.Cleanup(ClearLastFailure)
	if _, exists := LastFailure(); exists {
		t.Fatal("failure health was not cleared")
	}
	_ = wrap(CategoryCapacity, "write", "/private/path", errors.New("private detail"))
	snapshot, exists := LastFailure()
	if !exists || snapshot.Category != CategoryCapacity || snapshot.At.IsZero() {
		t.Fatalf("failure snapshot=%#v exists=%v", snapshot, exists)
	}
}
