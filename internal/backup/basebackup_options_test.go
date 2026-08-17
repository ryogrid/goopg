package backup

import (
	"testing"
)

// TestBaseBackupParseOptions exercises both PG17+ parenthesized
// option grammar and the legacy whitespace-separated form.
func TestBaseBackupParseOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want baseBackupOptions
	}{
		{
			name: "empty",
			in:   "",
			want: baseBackupOptions{},
		},
		{
			name: "parenthesized",
			in:   "(LABEL 'pg_basebackup base backup', PROGRESS, WAIT 0, TARGET 'client', MANIFEST 'yes')",
			want: baseBackupOptions{Label: "pg_basebackup base backup", Progress: true, Manifest: "yes", Target: "client", Wait: 0},
		},
		{
			name: "legacy",
			in:   "LABEL 'tag' PROGRESS",
			want: baseBackupOptions{Label: "tag", Progress: true},
		},
		{
			name: "unknown_key_tolerated",
			in:   "(LABEL 'x', SOMETHING_NEW 'y')",
			want: baseBackupOptions{Label: "x"},
		},
		{
			// pg_basebackup -X fetch sends a bare `WAL` boolean option.
			name: "wal_fetch_new_syntax",
			in:   "(LABEL 'x', WAL, MANIFEST 'no')",
			want: baseBackupOptions{Label: "x", Manifest: "no", IncludeWAL: true},
		},
		{
			// Legacy walsender clients send `WAL` as a bare keyword.
			name: "wal_fetch_legacy",
			in:   "LABEL 'tag' PROGRESS WAL",
			want: baseBackupOptions{Label: "tag", Progress: true, IncludeWAL: true},
		},
		{
			// An explicit false value disables WAL inclusion.
			name: "wal_explicit_false",
			in:   "(WAL 'f')",
			want: baseBackupOptions{IncludeWAL: false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBaseBackupOptions(tc.in)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Errorf("parseBaseBackupOptions(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
