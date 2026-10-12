package misc

import (
	"errors"
	"testing"
)

// TestSetPrivilegeSuset pins M0146-0158: set_config_option's PGC_SUSET
// check (guc.c) — SET, SET LOCAL, RESET and a routine's SET clause of a
// superuser-only parameter need a superuser or SET on the parameter, the
// check follows a SECURITY DEFINER override, user-settable parameters are
// unaffected, and RESET ALL (ResetAllOptions) is not checked.
func TestSetPrivilegeSuset(t *testing.T) {
	sess := NewSessionRegistry(BuildDefaultRegistry())
	if err := sess.Set("log_min_messages", "debug1", false); err != nil {
		t.Fatal(err)
	}
	role := "u"
	granted := false
	sess.SetPrivilegeHook(func() string { return role },
		func(r, name string) bool { return granted && r == "u" && name == "log_min_messages" })

	denied := func(what string, err error) {
		t.Helper()
		var verr *ValidationError
		if !errors.As(err, &verr) || verr.SQLState() != "42501" || verr.Msg != `permission denied to set parameter "log_min_messages"` {
			t.Errorf("%s: err = %v, want 42501 permission denied", what, err)
		}
	}
	denied("SET", sess.Set("log_min_messages", "debug2", false))
	denied("SET LOCAL", sess.Set("log_min_messages", "debug2", true))
	denied("SET TO DEFAULT", sess.Set("log_min_messages", "DEFAULT", false))
	denied("RESET", sess.Reset("log_min_messages"))
	denied("CheckSet", sess.CheckSet("log_min_messages", "debug2"))
	denied("CheckSetPrivilege", sess.CheckSetPrivilege("log_min_messages"))
	if _, err := sess.PushSaveLevel([]string{"log_min_messages=debug2"}); err == nil {
		t.Error("a routine SET clause bypassed the check")
	}
	if err := sess.Set("work_mem", "5MB", false); err != nil {
		t.Errorf("user-settable parameter: %v", err)
	}

	// A superuser-owned SECURITY DEFINER routine may.
	sess.PushRole("")
	if err := sess.Set("log_min_messages", "debug2", true); err != nil {
		t.Errorf("superuser override: %v", err)
	}
	sess.PopRole()
	denied("after PopRole", sess.Set("log_min_messages", "debug3", false))

	// SET granted on the parameter.
	granted = true
	if err := sess.Set("log_min_messages", "debug3", false); err != nil {
		t.Errorf("with SET granted: %v", err)
	}
	granted = false

	// RESET ALL resets it regardless.
	sess.ResetAll()
	if _, v, _ := sess.Get("log_min_messages"); v != "warning" {
		t.Errorf("RESET ALL left log_min_messages = %q, want warning", v)
	}
	role = ""
	if err := sess.Set("log_min_messages", "debug1", false); err != nil {
		t.Errorf("superuser session: %v", err)
	}
}
