package db

import (
	"strings"
)

// prefixed qualifies every column in a comma-separated list with a table name,
// so a shared column list can be reused in queries that join.
func prefixed(columns string, table string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = table + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// nullStr maps an empty string to a NULL column value.
//
// Returns *string rather than sql.NullString because pgx handles pointers
// natively and it matches the field types in internal/model.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
