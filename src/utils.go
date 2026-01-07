package main

import (
	"strings"

	"github.com/lib/pq"
)

// Helper to get values from a map
func values[K comparable, V any](m map[K]V) []V {
	result := make([]V, 0, len(m))
	for _, v := range m {
		result = append(result, v)
	}
	return result
}

// Helper to check if error is a unique constraint violation
func isUniqueViolation(err error) bool {
	if pqErr, ok := err.(*pq.Error); ok {
		return pqErr.Code == "23505" // unique_violation
	}
	return false
}

// sanitizeString removes null bytes (0x00) from strings, which PostgreSQL doesn't allow in UTF-8
func sanitizeString(s string) string {
	return strings.ReplaceAll(s, "\x00", "")
}

func sanitizeStringPtr(s *string) *string {
	if s == nil {
		return nil
	}
	sanitized := sanitizeString(*s)
	return &sanitized
}
