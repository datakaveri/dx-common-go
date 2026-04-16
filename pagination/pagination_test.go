package pagination

import (
	"testing"
)

func TestRequest_Validate_DefaultValues(t *testing.T) {
	req := Request{Page: 0, PageSize: 0}
	req.Validate()

	if req.Page != 1 {
		t.Fatalf("expected page=1, got %d", req.Page)
	}

	if req.PageSize != 10 {
		t.Fatalf("expected pageSize=10, got %d", req.PageSize)
	}
}

func TestRequest_Validate_LargePage Size(t *testing.T) {
	req := Request{Page: 1, PageSize: 200}
	req.Validate()

	if req.PageSize != 100 {
		t.Fatalf("expected max pageSize=100, got %d", req.PageSize)
	}
}

func TestRequest_Offset(t *testing.T) {
	tests := []struct {
		page     int
		pageSize int
		expected int
	}{
		{1, 10, 0},      // First page
		{2, 10, 10},     // Second page
		{3, 10, 20},     // Third page
		{1, 100, 0},     // Different page size
		{2, 100, 100},   // Different page size
	}

	for _, tt := range tests {
		req := Request{Page: tt.page, PageSize: tt.pageSize}
		offset := req.Offset()
		if offset != tt.expected {
			t.Fatalf("page=%d,pageSize=%d: expected offset=%d, got %d",
				tt.page, tt.pageSize, tt.expected, offset)
		}
	}
}

func TestResponse_NewResponse(t *testing.T) {
	req := Request{Page: 1, PageSize: 10}
	resp := NewResponse(req, 250)

	if resp.Page != 1 {
		t.Fatalf("expected page=1, got %d", resp.Page)
	}

	if resp.PageSize != 10 {
		t.Fatalf("expected pageSize=10, got %d", resp.PageSize)
	}

	if resp.Total != 250 {
		t.Fatalf("expected total=250, got %d", resp.Total)
	}

	if resp.TotalPages != 25 {
		t.Fatalf("expected totalPages=25, got %d", resp.TotalPages)
	}

	if !resp.HasNext {
		t.Fatal("expected hasNext=true for first page of multiple")
	}

	if resp.HasPrev {
		t.Fatal("expected hasPrev=false for first page")
	}
}

func TestResponse_FirstPage(t *testing.T) {
	req := Request{Page: 1, PageSize: 10}
	resp := NewResponse(req, 250)

	if resp.HasPrev {
		t.Fatal("first page should not have previous")
	}
	if !resp.HasNext {
		t.Fatal("first page should have next")
	}
}

func TestResponse_MiddlePage(t *testing.T) {
	req := Request{Page: 5, PageSize: 10}
	resp := NewResponse(req, 250)

	if !resp.HasPrev {
		t.Fatal("middle page should have previous")
	}
	if !resp.HasNext {
		t.Fatal("middle page should have next")
	}
}

func TestResponse_LastPage(t *testing.T) {
	req := Request{Page: 25, PageSize: 10}
	resp := NewResponse(req, 250)

	if !resp.HasPrev {
		t.Fatal("last page should have previous")
	}
	if resp.HasNext {
		t.Fatal("last page should not have next")
	}
}

func TestResponse_SinglePage(t *testing.T) {
	req := Request{Page: 1, PageSize: 10}
	resp := NewResponse(req, 5)

	if resp.TotalPages != 1 {
		t.Fatalf("expected totalPages=1, got %d", resp.TotalPages)
	}
	if resp.HasNext {
		t.Fatal("single page should not have next")
	}
	if resp.HasPrev {
		t.Fatal("single page should not have previous")
	}
}

func TestResponse_ZeroTotal(t *testing.T) {
	req := Request{Page: 1, PageSize: 10}
	resp := NewResponse(req, 0)

	if resp.TotalPages != 1 {
		t.Fatalf("expected totalPages=1 for zero total, got %d", resp.TotalPages)
	}
}

func TestParsePaginationParams_Valid(t *testing.T) {
	req := ParsePaginationParams("2", "20")

	if req.Page != 2 {
		t.Fatalf("expected page=2, got %d", req.Page)
	}

	if req.PageSize != 20 {
		t.Fatalf("expected pageSize=20, got %d", req.PageSize)
	}
}

func TestParsePaginationParams_Invalid(t *testing.T) {
	req := ParsePaginationParams("invalid", "also_invalid")

	if req.Page != 1 {
		t.Fatalf("expected default page=1, got %d", req.Page)
	}

	if req.PageSize != 10 {
		t.Fatalf("expected default pageSize=10, got %d", req.PageSize)
	}
}

func TestParsePaginationParams_Empty(t *testing.T) {
	req := ParsePaginationParams("", "")

	if req.Page != 1 {
		t.Fatalf("expected default page=1, got %d", req.Page)
	}

	if req.PageSize != 10 {
		t.Fatalf("expected default pageSize=10, got %d", req.PageSize)
	}
}

func TestParsePaginationParams_Negative(t *testing.T) {
	req := ParsePaginationParams("-5", "-20")

	if req.Page != 1 {
		t.Fatalf("expected default page=1 for negative, got %d", req.Page)
	}

	if req.PageSize != 10 {
		t.Fatalf("expected default pageSize=10 for negative, got %d", req.PageSize)
	}
}

func TestPaginatedResult_NewPaginatedResult(t *testing.T) {
	data := []string{"a", "b", "c"}
	req := Request{Page: 1, PageSize: 10}
	resp := NewResponse(req, 3)

	result := NewPaginatedResult(data, resp)

	if len(result.Data) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result.Data))
	}

	if result.Pagination.Page != 1 {
		t.Fatalf("expected page=1, got %d", result.Pagination.Page)
	}
}

func TestResponse_LargeDataset(t *testing.T) {
	req := Request{Page: 50, PageSize: 100}
	total := int64(10000)
	resp := NewResponse(req, total)

	if resp.TotalPages != 100 {
		t.Fatalf("expected 100 pages, got %d", resp.TotalPages)
	}

	expectedHasNext := req.Page < resp.TotalPages
	if resp.HasNext != expectedHasNext {
		t.Fatalf("expected hasNext=%v", expectedHasNext)
	}
}
