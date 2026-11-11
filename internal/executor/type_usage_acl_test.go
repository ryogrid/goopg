package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestTypeUsagePrivilege pins M0148-0001j3's pg_type_aclcheck(ACL_USAGE)
// sites (PG 18.3 privileges.sql): once USAGE is revoked from PUBLIC, a role
// without it gets 42501 "permission denied for type …" from CREATE TABLE
// (plain, array and OF columns), CREATE DOMAIN, CREATE FUNCTION and CREATE
// TYPE; a grantee and the superuser pass. A GRANT after REVOKE … FROM PUBLIC
// must not resurrect the PUBLIC entry, and GRANT on an array type or ON
// DOMAIN for a non-domain fail as in ExecGrant_Type.
func TestTypeUsagePrivilege(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TYPE pt AS (a int, b text)",
		"REVOKE USAGE ON TYPE pt FROM PUBLIC",
		"GRANT USAGE ON TYPE pt TO u2",
		"CREATE DOMAIN pd AS int",
		"REVOKE USAGE ON DOMAIN pd FROM PUBLIC",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	im := ctx.Catalog.(*catalog.InMemory)
	ct := im.LookupCompositeType("pt")
	if acl := im.TypeACLText(ct.OID); strings.Contains(acl, "{=U") || strings.Contains(acl, ",=U") {
		t.Errorf("typacl after REVOKE FROM PUBLIC + GRANT = %s; PUBLIC must stay revoked", acl)
	}
	var ee *ExecError
	for q, code := range map[string]string{
		"GRANT USAGE ON TYPE _pt TO u2":  "0LP01",
		"GRANT USAGE ON DOMAIN pt TO u2": "42809",
		"GRANT USAGE ON DOMAIN pd TO u2": "",
	} {
		err := runDDL(t, ctx, q)
		if code == "" {
			if err != nil {
				t.Errorf("%s: %v", q, err)
			}
			continue
		}
		if !errors.As(err, &ee) || ee.Code != code {
			t.Errorf("%s: err = %v, want %s", q, err, code)
		}
	}

	ctx.NonSuperuserRole = "u1"
	for q, typ := range map[string]string{
		"CREATE TABLE t1 (a int, b pd)":                                   "pd",
		"CREATE TABLE t2 (a int[], b pt[])":                               "pt",
		"CREATE TABLE t3 OF pt":                                           "pt",
		"CREATE DOMAIN pd2 AS pd":                                         "pd",
		"CREATE TYPE t4 AS (a int, b pd)":                                 "pd",
		"CREATE FUNCTION f5(a pd) RETURNS int LANGUAGE sql AS 'SELECT 1'": "pd",
		"CREATE FUNCTION f6(b int) RETURNS pd LANGUAGE sql AS 'SELECT 1'": "pd",
	} {
		err := runDDL(t, ctx, q)
		if !errors.As(err, &ee) || ee.Code != "42501" || ee.Message != "permission denied for type "+typ {
			t.Errorf("as u1, %s: err = %v, want 42501 permission denied for type %s", q, err, typ)
		}
	}
	if err := runDDL(t, ctx, "REVOKE ALL ON TYPE pt FROM PUBLIC"); !errors.As(err, &ee) || ee.Code != "42501" {
		t.Errorf("as u1, REVOKE on pt: err = %v, want 42501", err)
	}

	ctx.NonSuperuserRole = "u2"
	for _, q := range []string{
		"CREATE TABLE t7 (a int, b pd)",
		"CREATE TABLE t8 OF pt",
		"CREATE DOMAIN pd3 AS pd",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Errorf("as u2 (granted), %s: %v", q, err)
		}
	}
	ctx.TakeWarnings()
	if err := runDDL(t, ctx, "REVOKE ALL ON TYPE pt FROM PUBLIC"); err != nil {
		t.Errorf("as u2, REVOKE on pt: %v", err)
	}
	if w := ctx.TakeWarnings(); len(w) != 1 || w[0] != `no privileges could be revoked for "pt"` {
		t.Errorf("as u2, REVOKE warnings = %q, want PG's no-privileges WARNING", w)
	}
}
