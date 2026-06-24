package executor

import "testing"

type fakeSessionIdentity struct{ id uint64 }

func (f fakeSessionIdentity) UniqueID() uint64 { return f.id }

// TestSessionTempOwner verifies the executor's derivation of a temporary
// relation's owning-session token from the per-connection session identity
// (design 0118-0036). The token is what execCreateTable stamps onto
// catalog.Table.TempOwner for a CREATE TEMPORARY TABLE.
func TestSessionTempOwner(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		if got := sessionTempOwner(nil); got != "" {
			t.Fatalf("nil ctx: got %q, want empty", got)
		}
	})

	t.Run("no session identity", func(t *testing.T) {
		ctx := &Context{}
		if got := sessionTempOwner(ctx); got != "" {
			t.Fatalf("got %q, want empty (no identity)", got)
		}
	})

	t.Run("session identity yields stable distinct token", func(t *testing.T) {
		c1 := &Context{AdvisorySessionIdentity: fakeSessionIdentity{id: 7}}
		c2 := &Context{AdvisorySessionIdentity: fakeSessionIdentity{id: 42}}
		if got := sessionTempOwner(c1); got != "s7" {
			t.Fatalf("c1 token = %q, want s7", got)
		}
		if got := sessionTempOwner(c2); got != "s42" {
			t.Fatalf("c2 token = %q, want s42", got)
		}
		if sessionTempOwner(c1) == sessionTempOwner(c2) {
			t.Fatalf("two sessions produced the same token")
		}
	})

	t.Run("zero id is treated as no session", func(t *testing.T) {
		ctx := &Context{AdvisorySessionIdentity: fakeSessionIdentity{id: 0}}
		if got := sessionTempOwner(ctx); got != "" {
			t.Fatalf("got %q, want empty for id=0", got)
		}
	})
}
