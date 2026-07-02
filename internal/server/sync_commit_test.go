package server

import (
	"testing"

	"github.com/goopg/goopg/internal/config"
)

// TestSessionAsyncCommit pins the M0117-0007 Part B contract: only the
// literal `synchronous_commit` value "off" (or its boolean-ish spellings)
// enables async commit. "local" — which sessionSyncCommitMode collapses
// together with "off" for the *remote*-wait decision — must NOT: a "local"
// commit still requires the local WAL flush, only skipping the (separate)
// standby-ack wait.
func TestSessionAsyncCommit(t *testing.T) {
	cases := []struct {
		name string
		set  string // "" ⇒ never call Set (GUC left at its default "on")
		want bool
	}{
		{name: "off", set: "off", want: true},
		{name: "OFF-case-insensitive", set: "OFF", want: true},
		{name: "false", set: "false", want: true},
		{name: "0", set: "0", want: true},
		{name: "no", set: "no", want: true},
		{name: "local-still-flushes", set: "local", want: false},
		{name: "on", set: "on", want: false},
		{name: "remote_write", set: "remote_write", want: false},
		{name: "remote_apply", set: "remote_apply", want: false},
		{name: "unset-default", set: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
			if tc.set != "" {
				if err := sess.Set("synchronous_commit", tc.set, false); err != nil {
					t.Fatalf("Set(synchronous_commit=%q): %v", tc.set, err)
				}
			}
			if got := sessionAsyncCommit(sess); got != tc.want {
				t.Errorf("sessionAsyncCommit(synchronous_commit=%q) = %v, want %v", tc.set, got, tc.want)
			}
		})
	}
	if got := sessionAsyncCommit(nil); got != false {
		t.Errorf("sessionAsyncCommit(nil) = %v, want false", got)
	}
}
