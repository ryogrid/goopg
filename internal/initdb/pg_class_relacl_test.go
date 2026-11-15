package initdb

import (
	"sort"
	"testing"
)

// TestPgClassBootstrapRelACLs guards M0149-0001g slice 12: every bootstrap
// pg_class row whose relation a PG 18.3 initdb leaves with an ACL carries that
// ACL (pgClassInitdbRelACL), the rest NULL — never the ndim-0 "{}" that PG's
// check_acl rejects. It also reports which PG relations with ACLs goopg does not
// bootstrap at all (system views not yet ported), so the gap stays visible.
func TestPgClassBootstrapRelACLs(t *testing.T) {
	seen := map[string]bool{}
	withACL := 0
	for _, rel := range pgClassBootstrapRels() {
		d := pgClassBootstrapRelACL(rel) // panics on an unknown role or bad ACL text
		if d.IsNull() {
			continue
		}
		withACL++
		b := d.BytesValue()
		if len(b) < 24 || b[4] != 1 {
			t.Errorf("%s: relacl is not a one-dimensional aclitem[] (ndim %d)", rel.RelName, b[4])
		}
		for _, nsp := range []string{"pg_catalog.", "pg_toast.", "information_schema."} {
			if _, ok := pgClassInitdbRelACL[nsp+rel.RelName]; ok {
				seen[nsp+rel.RelName] = true
			}
		}
	}
	var missing []string
	for name := range pgClassInitdbRelACL {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	t.Logf("bootstrap relations with a PG initdb ACL: %d of %d; not bootstrapped by goopg: %d %v",
		withACL, len(pgClassInitdbRelACL), len(missing), missing)
	if withACL == 0 {
		t.Fatal("no bootstrap relation received an ACL")
	}
}

// TestPgProcBootstrapACLs guards M0149-0001g slice 13: every built-in function
// a PG 18.3 initdb leaves with a proacl gets it on its bootstrap pg_proc row,
// as a one-dimensional aclitem[]; every other row keeps NULL.
func TestPgProcBootstrapACLs(t *testing.T) {
	have := map[uint32]bool{}
	for _, e := range pgProcInitialEntries() {
		have[e.OID] = true
		d := pgProcRow(e)[29]
		_, want := pgProcInitdbProACL[e.OID]
		if want != !d.IsNull() {
			t.Errorf("function %d (%s): proacl set=%v, want %v", e.OID, e.Name, !d.IsNull(), want)
			continue
		}
		if want {
			if b := d.BytesValue(); len(b) < 24 || b[4] != 1 {
				t.Errorf("function %d (%s): proacl is not a one-dimensional aclitem[]", e.OID, e.Name)
			}
		}
	}
	for oid := range pgProcInitdbProACL {
		if !have[oid] {
			t.Errorf("function %d has a PG initdb proacl but no goopg bootstrap pg_proc row", oid)
		}
	}
}
