package executor

// operators_security_label_test.go pins the DU-002 slice 438 fix: `SECURITY
// LABEL [FOR provider] ON <object> IS 'text'` previously parsed into a plain
// CompatNoopStmt and silently succeeded. Real PostgreSQL's ExecSecLabelStmt
// (postgres/src/backend/commands/seclabel.c) checks its (empty, on goopg —
// there is no C-extension mechanism to load a provider) label_provider_list
// BEFORE ever resolving the target object, so every SECURITY LABEL statement
// always raises one of two ERRCODE_INVALID_PARAMETER_VALUE (22023) errors.
// Verified live against a scratch PostgreSQL 18.3 instance
// (postgres/local_install) before implementing.

import (
	"errors"
	"testing"
)

func TestSecurityLabelAlwaysErrors(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		message string
	}{
		{
			"bare form, no FOR clause",
			`SECURITY LABEL ON TABLE seclbl_t IS 'x'`,
			"no security label providers have been loaded",
		},
		{
			"FOR provider clause",
			`SECURITY LABEL FOR selinux ON TABLE seclbl_t IS 'system_u:object_r:sepgsql_table_t:s0'`,
			`security label provider "selinux" is not loaded`,
		},
		{
			"unqualified provider identifier",
			`SECURITY LABEL FOR sepgsql ON COLUMN seclbl_t.a IS NULL`,
			`security label provider "sepgsql" is not loaded`,
		},
		{
			"target object need not even exist — the provider check fires first",
			`SECURITY LABEL FOR selinux ON TABLE nosuchtable IS 'x'`,
			`security label provider "selinux" is not loaded`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			if err := runDDL(t, ctx, `CREATE TABLE seclbl_t (a integer)`); err != nil {
				t.Fatalf("setup CREATE TABLE: %v", err)
			}

			err := runDDL(t, ctx, tc.sql)
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
			}
			if ee.Code != "22023" {
				t.Errorf("Code=%q want %q", ee.Code, "22023")
			}
			if ee.Message != tc.message {
				t.Errorf("Message=%q want %q", ee.Message, tc.message)
			}
		})
	}
}
