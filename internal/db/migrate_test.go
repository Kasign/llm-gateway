package db

import "testing"

func TestParseCreateTableName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stmt string
		want string
	}{
		{"CREATE TABLE IF NOT EXISTS api_keys (\n  id INT\n)", "api_keys"},
		{"create table models (id int)", "models"},
		{"ALTER TABLE models ADD COLUMN x INT", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := parseCreateTableName(tc.stmt); got != tc.want {
			t.Fatalf("parseCreateTableName(%q) = %q, want %q", tc.stmt, got, tc.want)
		}
	}
}
