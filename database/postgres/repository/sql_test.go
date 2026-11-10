package repository

import (
	"context"
	"testing"
)

// fakeQueries stands in for a service's sqlc-generated Queries type.
type fakeQueries struct {
	name string
}

func TestNewWithSQL_AccessorAndEmbeddedCRUD(t *testing.T) {
	q := fakeQueries{name: "policy-queries"}
	repo := NewWithSQL[widget](nil, q, WithTable[widget]("widgets"), WithID[widget]("id"))

	if repo.SQL() != q {
		t.Fatalf("SQL() = %+v, want %+v", repo.SQL(), q)
	}
	if repo.dao.TableName != "widgets" {
		t.Fatalf("embedded Base's TableName = %q, want %q", repo.dao.TableName, "widgets")
	}

	// Embedded *Base[R]'s CRUD path is reachable without panicking on
	// construction — actual query execution needs a real pool, out of scope
	// for this unit test (it's exercised by dao's own tests).
	if repo.Query(context.Background()) == nil {
		t.Fatal("embedded Base.Query returned nil")
	}
}
