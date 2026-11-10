package httpx

import (
	"net/http"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/filter"
	"github.com/datakaveri/dx-common-go/platform/paging"
)

// ListContract is implemented by a paginated list request type to bind its
// query contract — paging plus a declared filter spec — as part of NORMAL
// binding.
//
// This is the composable counterpart to Binder. Binder replaces binding
// wholesale, which forces a self-binding list request to re-resolve the Actor,
// path values and pagination by hand — the exact duplicated seam that let
// dx-user-go's organisation-request list reject a valid multi-value filter. A
// type implementing ListContract instead keeps ordinary tag binding (embedded
// Actor, `path:"…"` fields) and has this run AFTER it: the query is parsed and
// strictly validated against the union of the paging names and the spec's names.
//
// A request type must not implement both Binder and ListContract; Binder wins
// and the contract would never run.
type ListContract interface {
	// FilterSpec is the operation's immutable filter contract. Build it once
	// (a package-level filter.MustNew) and return the same value every call.
	FilterSpec() *filter.Spec
	// SetList receives the validated paging parameters and parsed filters. The
	// handler then reads them, or hands them to its service as a scoped list
	// request; it never sees a DB column or a raw condition.
	SetList(paging.Params, filter.Request)
}

// bindListContract parses and validates the query for a ListContract request.
// It runs after tag binding, so Actor and path values are already populated.
func bindListContract(r *http.Request, lc ListContract) error {
	spec := lc.FilterSpec()
	if spec == nil {
		return errors.Internal("httpx: ListContract returned a nil FilterSpec")
	}
	// paging.Strict does both jobs in one pass: it parses page/size/sort/cursor
	// AND rejects any query parameter outside the union of the paging names and
	// the spec's declared filter names — the single strict namespace check the
	// list-query contract requires.
	params, err := paging.Strict(r, spec.Names()...)
	if err != nil {
		return errors.Validation(err.Error())
	}
	fr, err := spec.Parse(r.URL.Query())
	if err != nil {
		return err
	}
	lc.SetList(params, fr)
	return nil
}
