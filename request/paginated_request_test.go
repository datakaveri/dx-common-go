package request

import (
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/database/postgres/query"
)

func req(rawQuery string) *Builder {
	r := httptest.NewRequest("GET", "/x?"+rawQuery, nil)
	return From(r)
}

func TestBuild_PaginationDefaults(t *testing.T) {
	pr, err := req("").Build()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if pr.Page != 1 || pr.Size != 10 {
		t.Fatalf("defaults wrong: page=%d size=%d", pr.Page, pr.Size)
	}
	if pr.Offset() != 0 || pr.Limit() != 10 {
		t.Fatalf("offset/limit wrong: %d/%d", pr.Offset(), pr.Limit())
	}
}

func TestBuild_PageSizeOffset(t *testing.T) {
	pr, _ := req("page=3&size=20").Build()
	if pr.Offset() != 40 || pr.Limit() != 20 {
		t.Fatalf("offset=%d limit=%d", pr.Offset(), pr.Limit())
	}
}

func TestBuild_RejectsUnknownParam(t *testing.T) {
	_, err := req("bogus=1").Build()
	if err == nil {
		t.Fatal("expected error for unknown query param")
	}
}

func TestBuild_AllowParams(t *testing.T) {
	// A bespoke param ("choice") is rejected by default but accepted once
	// whitelisted via AllowParams; the handler reads it itself.
	if _, err := req("choice=PENDING").Build(); err == nil {
		t.Fatal("expected unknown 'choice' to be rejected")
	}
	pr, err := req("choice=PENDING&page=2&size=5").AllowParams("choice").Build()
	if err != nil {
		t.Fatalf("AllowParams should accept 'choice': %v", err)
	}
	if pr.Page != 2 || pr.Size != 5 {
		t.Fatalf("pagination wrong: page=%d size=%d", pr.Page, pr.Size)
	}
}

func TestBuild_FilterAllowlistMapping(t *testing.T) {
	pr, err := req("status=ACTIVE&status=CLOSED").
		AllowedFiltersDBMap(map[string]string{"status": "c_status"}).
		Build()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	v, ok := pr.Filters["c_status"]
	if !ok {
		t.Fatalf("expected mapped db column c_status, got %v", pr.Filters)
	}
	if vs, ok := v.([]string); !ok || len(vs) != 2 {
		t.Fatalf("expected 2 values, got %v", v)
	}
}

func TestBuild_SortAllowlistAndMapping(t *testing.T) {
	pr, err := req("sort=createdAt:desc;title:asc").
		AllowedSortFields("createdAt", "title").
		APIToDBMap(map[string]string{"createdAt": "created_at", "title": "title"}).
		Build()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(pr.OrderBy) != 2 {
		t.Fatalf("expected 2 order clauses, got %d", len(pr.OrderBy))
	}
	if pr.OrderBy[0].Column != "created_at" || !pr.OrderBy[0].Desc {
		t.Fatalf("first order wrong: %+v", pr.OrderBy[0])
	}
	if pr.OrderBy[1].Column != "title" || pr.OrderBy[1].Desc {
		t.Fatalf("second order wrong: %+v", pr.OrderBy[1])
	}
}

func TestBuild_RejectsDisallowedSortField(t *testing.T) {
	_, err := req("sort=secret:asc").AllowedSortFields("title").Build()
	if err == nil {
		t.Fatal("expected error for disallowed sort field")
	}
}

func TestBuild_Cursor(t *testing.T) {
	pr, err := req("cursor=xyz&size=5").Build()
	if err != nil {
		t.Fatalf("cursor must be an allowed param and parse: %v", err)
	}
	if pr.Cursor != "xyz" {
		t.Errorf("Cursor = %q, want xyz", pr.Cursor)
	}
}

func TestBuild_DefaultSortApplied(t *testing.T) {
	pr, _ := req("").DefaultSort("created_at", "desc").Build()
	if len(pr.OrderBy) != 1 || pr.OrderBy[0].Column != "created_at" || !pr.OrderBy[0].Desc {
		t.Fatalf("default sort not applied: %+v", pr.OrderBy)
	}
}

// TestBuild_TieBreak pins ROADMAP P1-6: a unique tie-breaker is appended to
// every resolved sort so a non-unique leading key (created_at) cannot let two
// requests for the same page return different rows.
func TestBuild_TieBreak(t *testing.T) {
	t.Run("appended to the default sort, aligned with its direction", func(t *testing.T) {
		pr, err := req("").DefaultSort("created_at", "desc").TieBreak("id").Build()
		if err != nil {
			t.Fatal(err)
		}
		if len(pr.OrderBy) != 2 {
			t.Fatalf("want [created_at, id], got %+v", pr.OrderBy)
		}
		if pr.OrderBy[0].Column != "created_at" || !pr.OrderBy[0].Desc {
			t.Fatalf("primary key wrong: %+v", pr.OrderBy[0])
		}
		if pr.OrderBy[1].Column != "id" || !pr.OrderBy[1].Desc {
			t.Fatalf("tie-break should be id desc (aligned with created_at desc): %+v", pr.OrderBy[1])
		}
	})

	t.Run("appended to a caller sort", func(t *testing.T) {
		pr, err := req("sort=title:asc").AllowedSortFields("title").TieBreak("id").Build()
		if err != nil {
			t.Fatal(err)
		}
		if len(pr.OrderBy) != 2 || pr.OrderBy[1].Column != "id" || pr.OrderBy[1].Desc {
			t.Fatalf("want [title asc, id asc], got %+v", pr.OrderBy)
		}
	})

	t.Run("not double-appended when the sort already ends on it", func(t *testing.T) {
		pr, err := req("sort=id:desc").AllowedSortFields("id").TieBreak("id").Build()
		if err != nil {
			t.Fatal(err)
		}
		if len(pr.OrderBy) != 1 || pr.OrderBy[0].Column != "id" {
			t.Fatalf("must not double-sort on the tie-break, got %+v", pr.OrderBy)
		}
	})

	t.Run("a lone tie-break gives a stable order with no default sort", func(t *testing.T) {
		pr, err := req("").TieBreak("id").Build()
		if err != nil {
			t.Fatal(err)
		}
		if len(pr.OrderBy) != 1 || pr.OrderBy[0].Column != "id" {
			t.Fatalf("want a lone id tie-break, got %+v", pr.OrderBy)
		}
	})
}

func TestConditions_RendersFiltersAndFuzzy(t *testing.T) {
	pr, _ := req("status=ACTIVE&q=climate").
		AllowedFiltersDBMap(map[string]string{"status": "status"}).
		FuzzyFiltersDBMap(map[string]string{"q": "title"}).
		Build()
	conds := pr.Conditions()
	where, args := query.BuildWhere(conds, 1)
	if where == "" || len(args) == 0 {
		t.Fatalf("expected rendered WHERE, got %q args=%v", where, args)
	}
}
