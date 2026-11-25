package initdb

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/goopg/goopg/internal/executor"
)

// pgAuthMembersEntry is one bootstrap pg_auth_members row.
type pgAuthMembersEntry struct {
	OID, RoleID, Member, Grantor uint32
	Admin, Inherit, Set          bool
}

// pgAuthMembersInitialEntries returns the three memberships a PostgreSQL 18.3
// initdb creates: system_functions.sql runs
//
//	GRANT pg_read_all_settings TO pg_monitor;
//	GRANT pg_read_all_stats TO pg_monitor;
//	GRANT pg_stat_scan_tables TO pg_monitor;
//
// as the bootstrap superuser (grantor 10, INHERIT TRUE, SET TRUE — the PG 16+
// defaults). OIDs 10308..10310 are the ones a PG 18.3 initdb assigns
// (measured on a scratch initdb); predefined role OIDs are pg_authid.dat's.
// Without these rows a member of pg_monitor on a PG node inherits none of the
// three roles' privileges (M0149-0001g slice 10).
func pgAuthMembersInitialEntries() []pgAuthMembersEntry {
	const (
		pgMonitor          = 3373
		pgReadAllSettings  = 3374
		pgReadAllStats     = 3375
		pgStatScanTables   = 3377
		bootstrapSuperuser = 10
	)
	return []pgAuthMembersEntry{
		{10308, pgReadAllSettings, pgMonitor, bootstrapSuperuser, false, true, true},
		{10309, pgReadAllStats, pgMonitor, bootstrapSuperuser, false, true, true},
		{10310, pgStatScanTables, pgMonitor, bootstrapSuperuser, false, true, true},
	}
}

// bootstrapPgAuthMembers writes the shared pg_auth_members heap (global/1261)
// and its four indexes from pgAuthMembersInitialEntries:
// 6303 (oid), 2694 (roleid, member, grantor), 2695 (member, roleid, grantor),
// 6302 (grantor) — pg_auth_members.h.
func bootstrapPgAuthMembers(dataDir string) error {
	entries := pgAuthMembersInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = executor.Row{
			executor.NewIntDatum(int64(e.OID)),
			executor.NewIntDatum(int64(e.RoleID)),
			executor.NewIntDatum(int64(e.Member)),
			executor.NewIntDatum(int64(e.Grantor)),
			executor.NewBoolDatum(e.Admin),
			executor.NewBoolDatum(e.Inherit),
			executor.NewBoolDatum(e.Set),
		}
	}
	tids, err := writeSharedHeapRows(dataDir, "1261", executor.PGAuthMembersColumnsPG18(), rows)
	if err != nil {
		return fmt.Errorf("pg_auth_members heap: %w", err)
	}
	type item struct {
		key [3]uint32
		tid heapTID
	}
	build := func(oid uint32, nkeys int, keyOf func(pgAuthMembersEntry) [3]uint32) error {
		items := make([]item, len(entries))
		for i, e := range entries {
			items[i] = item{keyOf(e), tids[i]}
		}
		sort.Slice(items, func(i, j int) bool {
			for k := 0; k < nkeys; k++ {
				if items[i].key[k] != items[j].key[k] {
					return items[i].key[k] < items[j].key[k]
				}
			}
			return false
		})
		tuples := make([][]byte, len(items))
		for i, it := range items {
			tuples[i] = pgBuildIndexTupleOidKeys(it.tid.Block, it.tid.Offset, it.key[:nkeys])
		}
		file, err := pgBuildBtreeBulkLoadSized(tuples, len(tuples[0]), uint16(nkeys))
		if err != nil {
			return fmt.Errorf("index %d: %w", oid, err)
		}
		return os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(uint64(oid), 10)), file, 0o600)
	}
	if err := build(6303, 1, func(e pgAuthMembersEntry) [3]uint32 { return [3]uint32{e.OID} }); err != nil {
		return err
	}
	if err := build(2694, 3, func(e pgAuthMembersEntry) [3]uint32 { return [3]uint32{e.RoleID, e.Member, e.Grantor} }); err != nil {
		return err
	}
	if err := build(2695, 3, func(e pgAuthMembersEntry) [3]uint32 { return [3]uint32{e.Member, e.RoleID, e.Grantor} }); err != nil {
		return err
	}
	return build(6302, 1, func(e pgAuthMembersEntry) [3]uint32 { return [3]uint32{e.Grantor} })
}

// pgBuildIndexTupleOidKeys encodes an IndexTuple keyed on len(keys) oid_ops
// columns: 8-byte header, 4 bytes per key, MAXALIGNed.
func pgBuildIndexTupleOidKeys(heapBlk uint32, heapOff uint16, keys []uint32) []byte {
	const hoff = 8
	size := (hoff + 4*len(keys) + 7) &^ 7
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&indexSizeMask)
	for i, k := range keys {
		le.PutUint32(out[hoff+4*i:], k)
	}
	return out
}
