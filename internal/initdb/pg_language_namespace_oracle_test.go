package initdb

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// TestPgLanguageMatchesPostgresBKI pins pg_language (oid lanname lanowner
// lanispl lanpltrusted lanplcallfoid laninline lanvalidator; lanacl NULL) to
// postgres.bki (M0149-0001g slice 11). sql's laninline had been 2511, which is
// pg_cursor — a PG node would have called it as sql's DO-block handler.
func TestPgLanguageMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_language") {
		if f[8] != "_null_" {
			t.Fatalf("pg_language %s: bki lanacl %q, test expects _null_", f[1], f[8])
		}
		want[f[0]] = strings.Join(f[:8], " ")
	}
	got := map[string]string{}
	for _, e := range pgLanguageInitialEntries() {
		if e.OID == pgPlpgsqlLanguage.OID {
			continue // created after bootstrap by CREATE EXTENSION plpgsql (slice 29)
		}
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %s %d %s %s %d %d %d", e.OID, e.LanName, e.LanOwner,
			boolChar(e.LanIspl), boolChar(e.LanPltrusted), e.LanPlcallfoid, e.LanInline, e.LanValidator)
	}
	compareBKIRows(t, "pg_language", want, got)
}

// TestPgNamespaceRowsMatchPG18Initdb pins the four bootstrap schemas to a PG
// 18.3 initdb's final state: postgres.bki's owners (public → pg_database_owner
// 6171) plus setup_privileges' / information_schema.sql's GRANT USAGE TO PUBLIC.
// nspacl must be NULL or a real ACL — never the empty array, which on a PG
// node denies even the owner USAGE.
func TestPgNamespaceRowsMatchPG18Initdb(t *testing.T) {
	type aclItem struct{ grantee, grantor uint32 }
	want := map[uint32]struct {
		owner uint32
		acl   []aclItem // nil = NULL
	}{
		11:    {10, []aclItem{{10, 10}, {0, 10}}},
		99:    {10, nil},
		2200:  {6171, []aclItem{{6171, 6171}, {0, 6171}}},
		13273: {10, []aclItem{{10, 10}, {0, 10}}},
	}
	for _, e := range pgNamespaceInitialEntries() {
		w, ok := want[e.OID]
		if !ok {
			t.Errorf("unexpected bootstrap namespace %d %s", e.OID, e.NspName)
			continue
		}
		if e.NspOwner != w.owner {
			t.Errorf("%s: nspowner %d, want %d", e.NspName, e.NspOwner, w.owner)
		}
		row, err := pgNamespaceRow(e)
		if err != nil {
			t.Fatalf("%s: %v", e.NspName, err)
		}
		acl := row[3]
		if w.acl == nil {
			if !acl.IsNull() {
				t.Errorf("%s: nspacl not NULL", e.NspName)
			}
			continue
		}
		blob := acl.BytesValue()
		const hdr = 24 // varlena + ndim + dataoffset + elemtype + dims + lbound
		if len(blob) != hdr+16*len(w.acl) || binary.LittleEndian.Uint32(blob[16:20]) != uint32(len(w.acl)) {
			t.Fatalf("%s: nspacl blob len %d, want %d items", e.NspName, len(blob), len(w.acl))
		}
		for i, it := range w.acl {
			b := blob[hdr+16*i:]
			gt, gr, mode := binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint32(b[4:8]), binary.LittleEndian.Uint64(b[8:16])
			if gt != it.grantee || gr != it.grantor {
				t.Errorf("%s acl[%d]: grantee/grantor %d/%d, want %d/%d", e.NspName, i, gt, gr, it.grantee, it.grantor)
			}
			// U = ACL_USAGE (1<<8), C = ACL_CREATE (1<<9); the owner holds UC, PUBLIC U.
			wantMode := uint64(1 << 8)
			if it.grantee != 0 {
				wantMode |= 1 << 9
			}
			if mode != wantMode {
				t.Errorf("%s acl[%d]: privileges %#x, want %#x", e.NspName, i, mode, wantMode)
			}
		}
	}
}
