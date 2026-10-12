package repository

import (
	"reflect"
	"testing"
)

// M-14 follow-up: Insert, BatchInsert and GetLastHash appended (or read
// the head for appending) outside the chain's advisory lock — exactly the
// read-head-then-insert pattern that forked the chain across replicas.
// The only way onto the chain is AppendChained (and the erasure path).
func TestRepository_NoUnlockedAppendPaths(t *testing.T) {
	rt := reflect.TypeOf(&Repository{})
	for _, name := range []string{"Insert", "BatchInsert", "GetLastHash"} {
		if _, ok := rt.MethodByName(name); ok {
			t.Errorf("Repository.%s bypasses the chain lock; append through AppendChained", name)
		}
	}
	if _, ok := rt.MethodByName("AppendChained"); !ok {
		t.Fatal("AppendChained must exist")
	}
}
