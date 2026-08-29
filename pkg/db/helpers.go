package db

import (
	"strings"
)

// prefixed qualifies each column in a list so it can be reused in a join.
func prefixed(columns string, table string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = table + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// nullStr maps an empty string to a NULL column value.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
