package util

import "time"

const isoLayout = time.RFC3339

// FormatTime formats t as an RFC3339 string in UTC.
func FormatTime(t time.Time) string {
	return t.UTC().Format(isoLayout)
}

// ParseTime parses an RFC3339 string into a time.Time.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(isoLayout, s)
}

// IsExpired returns true if t is before the current wall clock time.
func IsExpired(t time.Time) bool {
	return time.Now().After(t)
}

// AddDuration returns t + d.
func AddDuration(t time.Time, d time.Duration) time.Time {
	return t.Add(d)
}
