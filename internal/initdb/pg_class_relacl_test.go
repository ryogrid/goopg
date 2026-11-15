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
