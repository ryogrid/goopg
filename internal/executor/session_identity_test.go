package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestContextSessionUserNameFallback pins the pre-M0134-0009 fallback: a
// Context with no SessionUser set (built outside the postmaster — unit
// tests, internal callers) still reports "postgres", matching the old
// hardcoded-literal behaviour for the default case.
func TestContextSessionUserNameFallback(t *testing.T) {
	var ctx Context
	if got := ctx.SessionUserName(); got != "postgres" {
		t.Fatalf("SessionUserName() with empty SessionUser = %q, want %q", got, "postgres")
	}
	if got := ctx.EffectiveUserName(); got != "postgres" {
		t.Fatalf("EffectiveUserName() with empty SessionUser = %q, want %q", got, "postgres")
	}
	// Nil-safe like the rest of Context's zero-value helpers.
	var nilCtx *Context
	if got := nilCtx.SessionUserName(); got != "postgres" {
		t.Fatalf("nil Context.SessionUserName() = %q, want %q", got, "postgres")
	}
	if got := nilCtx.EffectiveUserName(); got != "postgres" {
		t.Fatalf("nil Context.EffectiveUserName() = %q, want %q", got, "postgres")
	}
}

// TestContextSessionUserNameHonoursLoginUser pins session_user() honouring
// the connected login role when no SET ROLE / SET SESSION AUTHORIZATION has
// run yet.
func TestContextSessionUserNameHonoursLoginUser(t *testing.T) {
	ctx := Context{SessionUser: "alice"}
	if got := ctx.SessionUserName(); got != "alice" {
		t.Fatalf("SessionUserName() = %q, want %q", got, "alice")
	}
	if got := ctx.EffectiveUserName(); got != "alice" {
		t.Fatalf("EffectiveUserName() with no SET ROLE active = %q, want %q", got, "alice")
	}
}

// TestContextEffectiveUserNameSetRoleDistinctFromSessionUser pins PG's core
// current_user/current_role vs session_user distinction (miscinit.c
// GetCurrentRoleId vs GetSessionUserId): current_user follows an active SET
// ROLE, session_user never does. M0134-0009 acceptance criterion 3.
func TestContextEffectiveUserNameSetRoleDistinctFromSessionUser(t *testing.T) {
	ctx := Context{
		SessionUser:      "alice",
		NonSuperuserRole: "bob",
		SetRoleIsActive:  true,
	}
	if got := ctx.EffectiveUserName(); got != "bob" {
		t.Fatalf("EffectiveUserName() with SET ROLE active = %q, want %q", got, "bob")
	}
	if got := ctx.SessionUserName(); got != "alice" {
		t.Fatalf("SessionUserName() must stay the login role even with SET ROLE active = %q, want %q", got, "alice")
	}

	// EffectiveUserName consults NonSuperuserRole whenever it is non-empty
	// (the postmaster's SetRole/SetSessionAuthorization closures keep it in
	// sync with whichever mechanism is active — see the RESET ROLE no-op
	// tests in internal/postmaster, which exercise SetRoleIsActive's actual
	// job: gating whether RESET ROLE is allowed to clear NonSuperuserRole).
	ctx2 := Context{SessionUser: "alice"}
	if got := ctx2.EffectiveUserName(); got != "alice" {
		t.Fatalf("EffectiveUserName() with no role override = %q, want %q", got, "alice")
	}
}

// TestCurrentUserFunctionsResolveSessionIdentity pins the expr.go fix
// (M0134-0009): current_user()/current_role/user() resolve the effective
// role, session_user() resolves the login role, and they diverge exactly
// when a SET ROLE is active.
func TestCurrentUserFunctionsResolveSessionIdentity(t *testing.T) {
	ctx := &Context{
		SessionUser:      "alice",
		NonSuperuserRole: "bob",
		SetRoleIsActive:  true,
	}
	cases := []struct {
		fn   string
		want string
	}{
		{"current_user", "bob"},
		{"current_role", "bob"},
		{"user", "bob"},
		{"session_user", "alice"},
	}
	for _, c := range cases {
		fc := &optimizer.FuncCall{Name: c.fn}
		d, err := evalFuncCall(fc, nil, ctx)
		if err != nil {
			t.Fatalf("%s(): unexpected error: %v", c.fn, err)
		}
		if got := d.StringValue(); got != c.want {
			t.Fatalf("%s() = %q, want %q", c.fn, got, c.want)
		}
	}
}
