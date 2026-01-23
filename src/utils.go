package main

import (
	"strings"
)

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
