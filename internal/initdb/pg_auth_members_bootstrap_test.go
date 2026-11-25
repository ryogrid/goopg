package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPgAuthMembersBootstrapRows guards M0149-0001g slice 10: initdb writes
// PostgreSQL 18.3's three predefined memberships (pg_monitor ⊃
// pg_read_all_settings / pg_read_all_stats / pg_stat_scan_tables) to the shared
// pg_auth_members heap and builds all four indexes, so a PG node on the
// directory resolves pg_monitor's inherited privileges.
func TestPgAuthMembersBootstrapRows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	heap, err := os.ReadFile(filepath.Join(dir, "global", "1261"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range pgAuthMembersInitialEntries() {
		needle := make([]byte, 16)
		binary.LittleEndian.PutUint32(needle[0:], e.OID)
		binary.LittleEndian.PutUint32(needle[4:], e.RoleID)
		binary.LittleEndian.PutUint32(needle[8:], e.Member)
		binary.LittleEndian.PutUint32(needle[12:], e.Grantor)
		if !bytes.Contains(heap, needle) {
			t.Errorf("global/1261: row oid=%d (role %d member %d) not found", e.OID, e.RoleID, e.Member)
		}
	}
	for _, idx := range []string{"6303", "2694", "2695", "6302"} {
		raw, err := os.ReadFile(filepath.Join(dir, "global", idx))
		if err != nil {
			t.Errorf("index %s: %v", idx, err)
			continue
		}
		if len(raw) < 2*storage.BlockSize {
			t.Errorf("index %s: %d bytes, want a metapage plus a populated leaf", idx, len(raw))
			continue
		}
		root := binary.LittleEndian.Uint32(raw[storage.SizeOfPageHeaderData+8:])
		if root == 0 {
			t.Errorf("index %s: btm_root=0 (empty)", idx)
		}
	}
}
