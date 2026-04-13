package util

// Contains reports whether s contains the value v.
func Contains[T comparable](s []T, v T) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

// Map applies fn to each element of s and returns the resulting slice.
func Map[T, U any](s []T, fn func(T) U) []U {
	out := make([]U, len(s))
	for i, v := range s {
		out[i] = fn(v)
	}
	return out
}

// Filter returns a new slice containing only the elements of s for which
// fn returns true.
func Filter[T any](s []T, fn func(T) bool) []T {
	out := make([]T, 0, len(s))
	for _, v := range s {
		if fn(v) {
			out = append(out, v)
		}
	}
	return out
}

// Unique returns a new slice with duplicate values removed, preserving
// the first occurrence of each element.
func Unique[T comparable](s []T) []T {
	seen := make(map[T]struct{}, len(s))
	out := make([]T, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// Chunk splits s into sub-slices of at most size n.
func Chunk[T any](s []T, n int) [][]T {
	if n <= 0 {
		return nil
	}
	var chunks [][]T
	for n < len(s) {
		s, chunks = s[n:], append(chunks, s[:n:n])
	}
	return append(chunks, s)
}

// Flatten merges a slice of slices into a single flat slice.
func Flatten[T any](ss [][]T) []T {
	var out []T
	for _, s := range ss {
		out = append(out, s...)
	}
	return out
}
