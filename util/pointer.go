package util

// Ptr returns a pointer to the given value. Useful for creating optional
// struct fields inline without a named variable.
//
//	user.MiddleName = util.Ptr("John")
func Ptr[T any](v T) *T {
	return &v
}

// PtrVal dereferences p if non-nil, otherwise returns def.
//
//	name := util.PtrVal(user.MiddleName, "")
func PtrVal[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// PtrEqual reports whether two pointers are equal: both nil, or both
// non-nil and pointing to equal values.
func PtrEqual[T comparable](a, b *T) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
