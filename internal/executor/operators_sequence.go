package executor

// operators_sequence.go — PostgreSQL sequence implementation.
//
// Implements the process-global sequence registry and the sequence
// manipulation functions (nextval, currval, setval, lastval).
//
// Sequences are named objects with an auto-incrementing int64 counter.
// The registry is shared across connections (process-global) because
// sequences are database objects, not session-local objects. Per-session
// state (currval / lastval) is stored in the executor Context.
//
// M0097-0009.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// seqState holds the mutable state of one sequence.
type seqState struct {
	current   atomic.Int64
	start     int64
	increment int64
	min       int64
	max       int64
	cache     int64 // CACHE n (preallocation size); PG default 1
	cycle     bool
	called    atomic.Bool // true after first nextval or setval
	ownedBy   string      // "table.column" set by ALTER SEQUENCE ... OWNED BY
	schema    string      // schema name (default "public")
	seqName   string      // bare sequence name (no schema prefix)
	dataType  string      // "smallint", "integer", or "bigint" (default)
	temporary bool        // true for TEMPORARY sequences (allowed in READ ONLY txns)
	dbOid     uint32      // owning database oid (M0122-0007 4e); immutable after creation
	mu        sync.Mutex  // serialises nextval

	// Restart persistence (RecordKindSequenceState — see wal/recovery.go).
	// colSpelling is the serial spelling ("bigserial", ...) when this
	// sequence backs a SERIAL column; identityKind is 1/2 for GENERATED
	// BY DEFAULT/ALWAYS identity columns. Both ride the WAL record so
	// startup replay can restore the owning column's catalog markers.
	// logHorizon is the highest (lowest, for descending sequences) value
	// covered by a WAL record: nextval pre-logs 32 values ahead (upstream
	// SEQ_LOG_VALS, postgres/src/backend/commands/sequence.c) and only
	// re-logs when a fetched value crosses the horizon. Guarded by mu.
	colSpelling  string
	identityKind byte
	logHorizon   int64
	hasLogged    bool
}

// nextVal atomically advances the sequence and returns the new value.
func (s *seqState) nextVal() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.current.Load()
	next := cur + s.increment
	seqName := s.schema + "." + s.seqName
	if s.schema == "" || s.schema == "public" {
		seqName = s.seqName
	}
	if s.increment > 0 {
		if next > s.max {
			if s.cycle {
				next = s.min
			} else {
				return 0, fmt.Errorf("nextval: reached maximum value of sequence %q (%d)", seqName, s.max)
			}
		}
	} else {
		if next < s.min {
			if s.cycle {
				next = s.max
			} else {
				return 0, fmt.Errorf("nextval: reached minimum value of sequence %q (%d)", seqName, s.min)
			}
		}
	}
	s.current.Store(next)
	s.called.Store(true)
	return next, nil
}

// seqRegistry is the process-global sequence store, namespaced by dbOid
// (M0122-0007 4e) — the key is "<dbOid>:<normalised name>" so that two
// distinct databases can each have their own same-named sequence without
// aliasing onto one shared counter.
var seqRegistry sync.Map // map[string]*seqState

// seqKey normalises a sequence name and dbOid to a registry key.
func seqKey(name string, dbOid uint32) string {
	return fmt.Sprintf("%d:%s", dbOid, strings.ToLower(strings.TrimSpace(name)))
}

// resolveSeqDBOid returns the target dbOid for a seqRegistry entry point: the
// first element of dbOid if the caller supplied one, else DefaultDBOid —
// mirrors catalog.resolveDBOid's trailing-variadic convention (M0122-0007
// slice 4b-ii) so every pre-4e call site (which passes none) keeps resolving
// to exactly the DefaultDBOid it implicitly used before this namespacing.
func resolveSeqDBOid(dbOid []uint32) uint32 {
	if len(dbOid) > 0 {
		return dbOid[0]
	}
	return catalog.DefaultDBOid
}

// RegisterSequence creates or replaces a sequence in the registry.
// Called by CREATE SEQUENCE and by SERIAL column initialisation.
func RegisterSequence(name string, start, increment, min, max int64, cycle bool, dbOid ...uint32) {
	oid := resolveSeqDBOid(dbOid)
	schema, bare := splitSeqName(strings.ToLower(strings.TrimSpace(name)))
	s := &seqState{
		start:     start,
		increment: increment,
		min:       min,
		max:       max,
		cache:     1, // PG default; overridden by SetSequenceCache for CACHE n
		cycle:     cycle,
		schema:    schema,
		seqName:   bare,
		dataType:  "bigint",
		dbOid:     oid,
	}
	// current starts at start-increment so that the first nextval returns start.
	s.current.Store(start - increment)
	seqRegistry.Store(seqKey(name, oid), s)
}

// SetSequenceDataType records the declared data type of a sequence (e.g. from
// CREATE SEQUENCE ... AS smallint). M0097-0068.
func SetSequenceDataType(name, dataType string, dbOid ...uint32) {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return
	}
	s := v.(*seqState)
	s.mu.Lock()
	s.dataType = strings.ToLower(dataType)
	s.mu.Unlock()
}

// SetSequenceCache records the declared CACHE size of a sequence (e.g. from
// CREATE/ALTER SEQUENCE ... CACHE n). Values < 1 are clamped to 1, matching PG's
// minimum. DU-002 slice 130.
func SetSequenceCache(name string, cache int64, dbOid ...uint32) {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return
	}
	if cache < 1 {
		cache = 1
	}
	s := v.(*seqState)
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()
}

// SetSequenceTemporary marks a sequence as temporary (allowed in READ ONLY txns).
// M0097-0024.
func SetSequenceTemporary(name string, tmp bool, dbOid ...uint32) {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return
	}
	s := v.(*seqState)
	s.mu.Lock()
	s.temporary = tmp
	s.mu.Unlock()
}

// IsSequenceTemporary returns true if the named sequence is temporary.
func IsSequenceTemporary(name string, dbOid ...uint32) bool {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return false
	}
	s := v.(*seqState)
	s.mu.Lock()
	tmp := s.temporary
	s.mu.Unlock()
	return tmp
}

// splitSeqName splits "schema.name" into (schema, name).
// If no schema prefix is present, schema defaults to "public".
func splitSeqName(name string) (schema, bare string) {
	if i := strings.Index(name, "."); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "public", name
}

// LookupSequence returns the seqState for name, or nil if not found.
// Tries both the bare name and the "public.<name>" qualified form to handle
// sequences registered with or without schema prefix.
func LookupSequence(name string, dbOid ...uint32) *seqState {
	oid := resolveSeqDBOid(dbOid)
	nameKey := strings.ToLower(strings.TrimSpace(name))
	if v, ok := seqRegistry.Load(seqKey(nameKey, oid)); ok {
		return v.(*seqState)
	}
	// Try "public.<bare>" if input has no schema, or bare if input has "public." prefix.
	if strings.Contains(nameKey, ".") {
		// Strip schema prefix and retry with bare name.
		if _, bare, ok2 := strings.Cut(nameKey, "."); ok2 {
			if v, ok := seqRegistry.Load(seqKey(bare, oid)); ok {
				return v.(*seqState)
			}
		}
	} else {
		// Try with "public." prefix.
		if v, ok := seqRegistry.Load(seqKey("public."+nameKey, oid)); ok {
			return v.(*seqState)
		}
	}
	return nil
}

// DropSequence removes a sequence from the registry. M0097-0038.
func DropSequence(name string, dbOid ...uint32) bool {
	_, loaded := seqRegistry.LoadAndDelete(seqKey(name, resolveSeqDBOid(dbOid)))
	return loaded
}

// RenameSequence moves a sequence from oldName to newName in the registry.
// Updates the internal schema/seqName fields. Returns false if oldName is not found.
// M0097-0024.
func RenameSequence(oldName, newName string, dbOid ...uint32) bool {
	oid := resolveSeqDBOid(dbOid)
	v, loaded := seqRegistry.LoadAndDelete(seqKey(oldName, oid))
	if !loaded {
		return false
	}
	s := v.(*seqState)
	s.mu.Lock()
	schema, bare := splitSeqName(strings.ToLower(strings.TrimSpace(newName)))
	s.schema = schema
	s.seqName = bare
	s.mu.Unlock()
	seqRegistry.Store(seqKey(newName, oid), s)
	return true
}

// SequenceRowData returns (lastValue, logCnt, isCalled) for SELECT * FROM seq.
// lastValue is the last returned value when called; when not yet called it is
// the value the next nextval will return (start for a fresh sequence, or N
// after setval(N,false) / RESTART WITH N) — i.e. the on-disk last_value pg_dump
// reads from the sequence relation.
// logCnt is 32 when called (mirrors PG's write-ahead log cache size), 0 otherwise.
// Returns ok=false if the sequence does not exist. M0097-0024.
func SequenceRowData(name string, dbOid ...uint32) (lastValue int64, logCnt int64, isCalled bool, ok bool) {
	v, exists := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !exists {
		return 0, 0, false, false
	}
	s := v.(*seqState)
	s.mu.Lock()
	isCalled = s.called.Load()
	if isCalled {
		lastValue = s.current.Load()
		logCnt = 32
	} else {
		// Not-yet-called: last_value is the value the next nextval will
		// return. The registry stores `current = nextTarget - increment`
		// (RegisterSequence seeds start-increment; setval(N,false) /
		// RESTART WITH N seed N-increment), so the on-disk last_value is
		// `current + increment`. For a fresh sequence this equals start;
		// after setval('seq', N, false) it equals N — matching what real
		// pg_dump reads from the sequence relation (`SELECT last_value`).
		// Returning the bare `start` here silently dropped any non-default
		// setval(N,false) / RESTART WITH N, corrupting the dumped setval.
		lastValue = s.current.Load() + s.increment
		logCnt = 0
	}
	s.mu.Unlock()
	return lastValue, logCnt, isCalled, true
}

// SequenceOwnedBy returns the "table.column" owner string for a sequence (empty if unowned).
func SequenceOwnedBy(name string, dbOid ...uint32) string {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return ""
	}
	s := v.(*seqState)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownedBy
}

// FindSequenceOwnedBy searches seqRegistry for a sequence in dbOid's database
// whose ownedBy field matches owner ("table.column"). Returns the sequence
// name, or "" if not found.
func FindSequenceOwnedBy(owner string, dbOid ...uint32) string {
	oid := resolveSeqDBOid(dbOid)
	owner = strings.ToLower(owner)
	var found string
	seqRegistry.Range(func(k, v any) bool {
		s := v.(*seqState)
		if s.dbOid != oid {
			return true
		}
		s.mu.Lock()
		ob := s.ownedBy
		nm := s.seqName
		s.mu.Unlock()
		if ob == owner {
			found = nm
			return false
		}
		return true
	})
	return found
}

// SetSequenceOwnedBy records that the sequence with the given name is owned by
// "table.column" (as produced by ALTER SEQUENCE ... OWNED BY). Pass "" to clear.
func SetSequenceOwnedBy(name, owner string, dbOid ...uint32) {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return
	}
	s := v.(*seqState)
	s.mu.Lock()
	s.ownedBy = strings.ToLower(owner)
	s.mu.Unlock()
}

// DropSequencesOwnedByTable drops all sequences in dbOid's database whose
// ownedBy field starts with "tableName." (case-insensitive). Called by DROP
// TABLE to cascade-drop owned sequences.
func DropSequencesOwnedByTable(tableName string, dbOid ...uint32) []string {
	oid := resolveSeqDBOid(dbOid)
	prefix := strings.ToLower(tableName) + "."
	oidPrefix := fmt.Sprintf("%d:", oid)
	var dropped []string
	seqRegistry.Range(func(k, v any) bool {
		s := v.(*seqState)
		if s.dbOid != oid {
			return true
		}
		s.mu.Lock()
		owned := s.ownedBy
		s.mu.Unlock()
		if strings.HasPrefix(owned, prefix) {
			seqRegistry.Delete(k)
			dropped = append(dropped, strings.TrimPrefix(k.(string), oidPrefix))
		}
		return true
	})
	return dropped
}

// ResetSequence resets a sequence to its start value (equivalent to TRUNCATE ... RESTART IDENTITY).
// Returns false if the sequence does not exist.
func ResetSequence(name string, dbOid ...uint32) bool {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return false
	}
	s := v.(*seqState)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.Store(s.start - s.increment)
	return true
}

// GetSequenceCurrentValue returns the raw internal current counter of the sequence.
// The next nextval() call will return current+increment. Returns (0, false) if not found.
func GetSequenceCurrentValue(name string, dbOid ...uint32) (int64, bool) {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return 0, false
	}
	return v.(*seqState).current.Load(), true
}

// ─── Restart persistence (RecordKindSequenceState / RecordKindDropSequence) ───
//
// goopg's sequence registry is in-memory only, so every state change that must
// survive a restart is WAL-logged as a full-state snapshot and re-applied by
// internal/initdb/sequence_ddl_recovery.go. nextval pre-logs 32 values ahead
// (upstream SEQ_LOG_VALS, postgres/src/backend/commands/sequence.c) so an
// insert-heavy workload pays one tiny record per 32 fetches; a crash loses at
// most the pre-logged gap, exactly like PostgreSQL.

// SetSequenceColumnMarker records that the sequence backs a SERIAL column
// (spelling = "serial"/"bigserial"/... as stored in the column's catalog
// type) or an identity column (identityKind 1 = BY DEFAULT, 2 = ALWAYS).
// The marker rides the sequence's WAL state record so startup replay can
// restore the owning column's catalog markers, which the INSERT
// auto-increment path keys on.
func SetSequenceColumnMarker(name, spelling string, identityKind byte, dbOid ...uint32) {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return
	}
	s := v.(*seqState)
	s.mu.Lock()
	s.colSpelling = spelling
	s.identityKind = identityKind
	s.mu.Unlock()
}

// payloadLocked builds the WAL snapshot for s with the given counter state.
// Caller must hold s.mu.
func (s *seqState) payloadLocked(current int64, called bool) wal.SequenceStatePayload {
	name := s.seqName
	if s.schema != "" && s.schema != "public" {
		name = s.schema + "." + s.seqName
	}
	return wal.SequenceStatePayload{
		Name:         name,
		Start:        s.start,
		Increment:    s.increment,
		Min:          s.min,
		Max:          s.max,
		Cache:        s.cache,
		Current:      current,
		Cycle:        s.cycle,
		Called:       called,
		DataType:     s.dataType,
		OwnedBy:      s.ownedBy,
		ColSpelling:  s.colSpelling,
		IdentityKind: s.identityKind,
		DBOid:        s.dbOid,
	}
}

// WALLogSequenceState snapshots the sequence's exact live state and appends a
// RecordKindSequenceState record. Called after any definition-level mutation
// (CREATE TABLE with SERIAL/IDENTITY, CREATE/ALTER SEQUENCE, setval, TRUNCATE
// ... RESTART IDENTITY). Temporary sequences are session-scoped and never
// logged. A nil ctx/WAL (tests, WAL-less servers) is a no-op.
func WALLogSequenceState(ctx *Context, name string) {
	if ctx == nil || ctx.WAL == nil {
		return
	}
	s := LookupSequence(name, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.temporary {
		s.mu.Unlock()
		return
	}
	cur := s.current.Load()
	p := s.payloadLocked(cur, s.called.Load())
	s.logHorizon = cur
	s.hasLogged = true
	s.mu.Unlock()
	// B1.3b: the COUNTER journals as XLOG_SEQ_LOG — a rewrite of the
	// sequence relation's single physical page (replacing the retired
	// RecordKindSequenceState). Best-effort like the walLog* helpers.
	WriteSequencePageAndLog(ctx, name, p.DBOid, p.Current, 0, p.Called)
	// B1.3 (doc 02c §3): the DEFINITION journals as a real pg_sequence heap
	// row (INSERT/UPDATE via the fingerprint-gated upsert; counter-only
	// snapshots skip).
	syncSequenceDefinitionToCatalogHeap(ctx, name, p)
}

// SnapshotSequenceState returns the sequence's exact live definitional state
// (start/increment/bounds/cache/cycle/current counter/called flag/ownership
// markers), or ok=false if no such non-temporary sequence exists under dbOid.
// Used by CREATE DATABASE ... TEMPLATE's sequence-clone path
// (internal/server/database_ddl.go) to capture a template sequence's state
// without exposing the unexported seqState type across the package boundary.
// A temporary sequence (session-scoped) is never a valid clone source —
// resolveCreateDatabaseTemplate rejects any template containing one before
// this is ever called, so ok=false here would only mean a concurrent DROP
// SEQUENCE raced the clone (the source-database busy guard makes this rare
// in practice, but the caller must still handle it explicitly).
func SnapshotSequenceState(name string, dbOid uint32) (wal.SequenceStatePayload, bool) {
	v, ok := seqRegistry.Load(seqKey(name, dbOid))
	if !ok {
		return wal.SequenceStatePayload{}, false
	}
	s := v.(*seqState)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.temporary {
		return wal.SequenceStatePayload{}, false
	}
	return s.payloadLocked(s.current.Load(), s.called.Load()), true
}

// maybePreLogNextval WAL-logs the sequence state with the counter advanced
// SEQ_LOG_VALS (32) values ahead of the just-fetched v, when v has crossed the
// previously logged horizon. Mirrors upstream sequence.c: replaying the
// pre-logged horizon never repeats a handed-out value; a crash wastes at most
// the 32-value gap. Cycled sequences may repeat values after a crash within
// the wrapped range (same caveat as PG's unlogged gap semantics; v0 accepts it).
func (s *seqState) maybePreLogNextval(ctx *Context, v int64) {
	if ctx == nil || ctx.WAL == nil {
		return
	}
	s.mu.Lock()
	if s.temporary {
		s.mu.Unlock()
		return
	}
	covered := s.hasLogged &&
		((s.increment > 0 && v <= s.logHorizon) || (s.increment < 0 && v >= s.logHorizon))
	if covered {
		s.mu.Unlock()
		return
	}
	const seqLogVals = 32 // SEQ_LOG_VALS, postgres/src/backend/commands/sequence.c
	horizon := v + seqLogVals*s.increment
	if s.increment > 0 && (horizon > s.max || horizon < v) {
		horizon = s.max
	} else if s.increment < 0 && (horizon < s.min || horizon > v) {
		horizon = s.min
	}
	p := s.payloadLocked(horizon, true)
	s.logHorizon = horizon
	s.hasLogged = true
	s.mu.Unlock()
	// B1.3b: the pre-log horizon rides XLOG_SEQ_LOG (crash restarts at the
	// horizon, skipping ≤ SEQ_LOG_VALS values — PG-identical).
	WriteSequencePageAndLog(ctx, p.Name, p.DBOid, p.Current, 0, p.Called)
}

// autoGenerateSerialValues fills nextval() values for SERIAL/BIGSERIAL/
// SMALLSERIAL and IDENTITY columns whose slot is still NULL and was NOT
// explicitly supplied by the INSERT (an explicit NULL must fall through to
// the NOT NULL check, mirroring PG). Shared by the plain-insert path
// (operators_storage.go) and the upsert path (operators_upsert.go) — the two
// are sibling paths and previously diverged: INSERT ... ON CONFLICT skipped
// auto-generation entirely, leaving NULL bigserial ids (surfaced by
// WordPress's option upserts writing 34 NULL wp_options.option_id rows).
// M0097-0009 / root-0020.
func autoGenerateSerialValues(ctx *Context, tableName string, cols []catalog.Column, row Row, missing []bool) {
	for i, col := range cols {
		if !row[i].IsNull() || !missing[i] {
			continue
		}
		seqName := ""
		switch strings.ToLower(col.Type.Name) {
		case "serial", "serial4", "bigserial", "serial8", "smallserial", "serial2":
			seqName = strings.ToLower(tableName) + "_" + strings.ToLower(col.Name) + "_seq"
			// If the standard tablename_colname_seq was renamed, look up by ownership.
			if LookupSequence(seqName, ctxSeqDBOid(ctx)) == nil {
				if owned := FindSequenceOwnedBy(strings.ToLower(tableName)+"."+strings.ToLower(col.Name), ctxSeqDBOid(ctx)); owned != "" {
					seqName = owned
				}
			}
		default:
			if col.IdentityColumn {
				seqName = strings.ToLower(tableName) + "_" + strings.ToLower(col.Name) + "_seq"
			}
		}
		if seqName == "" {
			continue
		}
		v, err := evalNextval([]Datum{NewStringDatum(seqName)}, ctx)
		if err == nil && !v.IsNull() {
			row[i] = v
		}
	}
}

// RestoreSequenceFromWAL re-registers a sequence from a replayed
// RecordKindSequenceState record (last record wins). Counter state is
// restored exactly as logged: Current is the pre-logged horizon for records
// emitted by nextval, or the exact value for create/alter/setval snapshots.
func RestoreSequenceFromWAL(p wal.SequenceStatePayload) {
	dbOid := catalog.NamespaceDBOid(p.DBOid)
	RegisterSequence(p.Name, p.Start, p.Increment, p.Min, p.Max, p.Cycle, dbOid)
	if p.Cache > 1 {
		SetSequenceCache(p.Name, p.Cache, dbOid)
	}
	if p.DataType != "" {
		SetSequenceDataType(p.Name, p.DataType, dbOid)
	}
	if p.OwnedBy != "" {
		SetSequenceOwnedBy(p.Name, p.OwnedBy, dbOid)
	}
	SetSequenceColumnMarker(p.Name, p.ColSpelling, p.IdentityKind, dbOid)
	s := LookupSequence(p.Name, dbOid)
	if s == nil {
		return
	}
	s.current.Store(p.Current)
	s.called.Store(p.Called)
	s.mu.Lock()
	s.logHorizon = p.Current
	s.hasLogged = true
	s.mu.Unlock()
}

// SetSequenceCurrentValue directly sets the internal counter (used for ROLLBACK of RESTART IDENTITY).
func SetSequenceCurrentValue(name string, val int64, dbOid ...uint32) bool {
	v, ok := seqRegistry.Load(seqKey(name, resolveSeqDBOid(dbOid)))
	if !ok {
		return false
	}
	v.(*seqState).current.Store(val)
	return true
}

// evalNextval implements nextval(sequence_name text) → int8.
// Advances the sequence and returns the new value. Also stores the
// value in ctx.LastSeqVal and ctx.CurrSeqVals for currval/lastval.
// seqNameFromDatum resolves a sequence name from a Datum argument.
// When the caller uses 'seqname'::regclass, the cast produces a KindInt OID;
// we resolve it back to a name via the catalog. Plain text args go through
// StringValue as before.
func seqNameFromDatum(d Datum, ctx *Context) string {
	if d.Kind == KindInt && ctx != nil && ctx.Catalog != nil {
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if tbl, found := im.LookupTableByOID(uint32(d.Int)); found && tbl != nil {
				return tbl.Name
			}
		}
	}
	return d.StringValue()
}

// ctxSeqDBOid resolves the seqRegistry dbOid for a Context, defaulting to
// DefaultDBOid when ctx is nil (e.g. a test calling an eval* function
// directly with no session).
func ctxSeqDBOid(ctx *Context) uint32 {
	if ctx == nil {
		return catalog.DefaultDBOid
	}
	return catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
}

func evalNextval(args []Datum, ctx *Context) (Datum, error) {
	if len(args) == 0 {
		return NullDatum, nil
	}
	if args[0].IsNull() {
		return NullDatum, nil
	}
	name := seqNameFromDatum(args[0], ctx)
	dbOid := ctxSeqDBOid(ctx)
	// Read-only transaction guard: non-temp sequences cannot be advanced.
	if ctx != nil && ctx.Session != nil && ctx.Session.IsReadOnlyTxn() && !IsSequenceTemporary(name, dbOid) {
		return Datum{}, &ExecError{Code: "25006", Message: "cannot execute nextval() in a read-only transaction"}
	}
	s := LookupSequence(name, dbOid)
	if s == nil {
		return Datum{}, &ExecError{Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", name)}
	}
	// nextval holds a RowExclusiveLock on the sequence relation (mirrors
	// PostgreSQL's lock_and_open_sequence), so it waits while another session is
	// mid-ALTER SEQUENCE (AccessExclusiveLock) and a later ALTER SEQUENCE waits
	// for an in-progress nextval. M0118-0008 (sequence-ddl isolation spec).
	if ctx != nil && ctx.Catalog != nil {
		on := parser.ObjectName{Name: name}
		if i := strings.LastIndex(name, "."); i >= 0 {
			on = parser.ObjectName{Schema: name[:i], Name: name[i+1:]}
		}
		if tbl, ok := ctx.Catalog.LookupTable(on, dbOid); ok {
			if err := ctx.acquireSequenceLockTxn(ctx.Catalog.RelFileNode(tbl)); err != nil {
				return Datum{}, err
			}
		}
	}
	v, err := s.nextVal()
	if err != nil {
		return Datum{}, &ExecError{Code: "2200H", Message: err.Error()}
	}
	// Restart persistence: pre-log the counter 32 values ahead when v crosses
	// the logged horizon (SEQ_LOG_VALS semantics, sequence.c).
	s.maybePreLogNextval(ctx, v)
	// Store for currval / lastval.
	if ctx != nil {
		if ctx.CurrSeqVals == nil {
			ctx.CurrSeqVals = make(map[string]int64)
		}
		ctx.CurrSeqVals[seqKey(name, dbOid)] = v
		ctx.LastSeqVal = v
		ctx.LastSeqSet = true
		ctx.LastSeqName = name
	}
	return Datum{Kind: KindInt, Int: v}, nil
}

// evalCurrval implements currval(sequence_name text) → int8.
// Returns the most recent value returned by nextval for this sequence
// in the current session. Errors if nextval has not been called.
func evalCurrval(args []Datum, ctx *Context) (Datum, error) {
	if len(args) == 0 {
		return NullDatum, nil
	}
	if args[0].IsNull() {
		return NullDatum, nil
	}
	name := seqNameFromDatum(args[0], ctx)
	if ctx == nil || ctx.CurrSeqVals == nil {
		return Datum{}, &ExecError{Code: "55000", Message: fmt.Sprintf("currval of sequence %q is not yet defined in this session", name)}
	}
	v, ok := ctx.CurrSeqVals[seqKey(name, ctxSeqDBOid(ctx))]
	if !ok {
		return Datum{}, &ExecError{Code: "55000", Message: fmt.Sprintf("currval of sequence %q is not yet defined in this session", name)}
	}
	return Datum{Kind: KindInt, Int: v}, nil
}

// evalSetval implements setval(sequence_name text, value int8 [, is_called bool]) → int8.
// Sets the current value of the sequence. If is_called is true (default),
// the next nextval will return value+increment. If false, nextval returns value.
func evalSetval(args []Datum, ctx *Context) (Datum, error) {
	if len(args) < 2 {
		return NullDatum, nil
	}
	if args[0].IsNull() || args[1].IsNull() {
		return NullDatum, nil
	}
	name := seqNameFromDatum(args[0], ctx)
	dbOid := ctxSeqDBOid(ctx)
	// Read-only transaction guard: non-temp sequences cannot be set.
	if ctx != nil && ctx.Session != nil && ctx.Session.IsReadOnlyTxn() && !IsSequenceTemporary(name, dbOid) {
		return Datum{}, &ExecError{Code: "25006", Message: "cannot execute setval() in a read-only transaction"}
	}
	value := args[1].Int
	isCalled := true
	if len(args) >= 3 && !args[2].IsNull() && args[2].Kind == KindBool {
		isCalled = args[2].BoolValue()
	}

	s := LookupSequence(name, dbOid)
	if s == nil {
		RegisterSequence(name, 1, 1, 1, 9223372036854775807, false, dbOid)
		s = LookupSequence(name, dbOid)
	}
	// Validate value is within sequence bounds.
	s.mu.Lock()
	seqMin, seqMax := s.min, s.max
	s.mu.Unlock()
	if value < seqMin || value > seqMax {
		return Datum{}, &ExecError{Code: "22003",
			Message: fmt.Sprintf("setval: value %d is out of bounds for sequence \"%s\" (%d..%d)", value, name, seqMin, seqMax)}
	}
	if isCalled {
		// Next nextval returns value+increment.
		s.current.Store(value)
		s.called.Store(true)
	} else {
		// Next nextval returns value.
		s.current.Store(value - s.increment)
		s.called.Store(false)
	}
	// Restart persistence: setval is a definition-level counter change —
	// log the exact new state (mirrors sequence.c do_setval's XLogInsert).
	WALLogSequenceState(ctx, name)
	// Update session state.
	if ctx != nil {
		if ctx.CurrSeqVals == nil {
			ctx.CurrSeqVals = make(map[string]int64)
		}
		ctx.CurrSeqVals[seqKey(name, dbOid)] = value
		ctx.LastSeqVal = value
		ctx.LastSeqSet = true
		ctx.LastSeqName = name
	}
	return Datum{Kind: KindInt, Int: value}, nil
}

// evalLastval implements lastval() → int8.
// Returns the most recent value returned by nextval in the current session.
func evalLastval(ctx *Context) (Datum, error) {
	if ctx == nil || !ctx.LastSeqSet {
		return Datum{}, &ExecError{Code: "55000", Message: "lastval is not yet defined in this session"}
	}
	// If the last-used sequence was dropped, lastval() must error.
	if ctx.LastSeqName != "" && LookupSequence(ctx.LastSeqName, ctxSeqDBOid(ctx)) == nil {
		ctx.LastSeqSet = false
		return Datum{}, &ExecError{Code: "55000", Message: "lastval is not yet defined in this session"}
	}
	return Datum{Kind: KindInt, Int: ctx.LastSeqVal}, nil
}

// UpdateSequenceParams applies ALTER SEQUENCE parameter changes to a sequence.
// All pointer fields: nil means "leave unchanged". restart=true resets current
// to start-increment (honoring newStart if also supplied). restartWith, if not
// nil, overrides the restart target. M0097-0068.
func UpdateSequenceParams(name string, increment, minVal, maxVal, startWith, restartWith, cache *int64,
	restart, cycle, noCycle bool, dbOid ...uint32) error {
	s := LookupSequence(name, resolveSeqDBOid(dbOid))
	if s == nil {
		return fmt.Errorf("sequence %q does not exist", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if increment != nil {
		s.increment = *increment
	}
	if cache != nil {
		c := *cache
		if c < 1 {
			c = 1
		}
		s.cache = c
	}
	if minVal != nil {
		s.min = *minVal
	}
	if maxVal != nil {
		s.max = *maxVal
	}
	if cycle {
		s.cycle = true
	} else if noCycle {
		s.cycle = false
	}
	if startWith != nil {
		s.start = *startWith
	}
	if restartWith != nil {
		// RESTART WITH n: set current so next nextval returns n.
		s.current.Store(*restartWith - s.increment)
		s.called.Store(false)
	} else if restart {
		// RESTART: reset to stored start.
		s.current.Store(s.start - s.increment)
		s.called.Store(false)
	}
	return nil
}

// SeqInfo is a snapshot of a single sequence's state, used by pg_sequences.
type SeqInfo struct {
	Schema    string
	Name      string
	DataType  string // "smallint", "integer", or "bigint"
	Start     int64
	Min       int64
	Max       int64
	Increment int64
	Cycle     bool
	LastValue int64
	Called    bool // false → last_value is NULL in pg_sequences
}

// AllSequenceInfos returns a snapshot of all registered sequences.
// Called by the pg_sequences virtual table VirtualRows callback.
func AllSequenceInfos(dbOid ...uint32) []SeqInfo {
	oid := resolveSeqDBOid(dbOid)
	var out []SeqInfo
	seqRegistry.Range(func(_, v any) bool {
		s := v.(*seqState)
		if s.dbOid != oid {
			return true
		}
		s.mu.Lock()
		dt := s.dataType
		if dt == "" {
			dt = "bigint"
		}
		info := SeqInfo{
			Schema:    s.schema,
			Name:      s.seqName,
			DataType:  dt,
			Start:     s.start,
			Min:       s.min,
			Max:       s.max,
			Increment: s.increment,
			Cycle:     s.cycle,
			Called:    s.called.Load(),
			LastValue: s.current.Load(),
		}
		s.mu.Unlock()
		out = append(out, info)
		return true
	})
	return out
}

func sortedSequenceInfos(dbOid uint32) []SeqInfo {
	infos := AllSequenceInfos(dbOid)
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Schema != infos[j].Schema {
			return infos[i].Schema < infos[j].Schema
		}
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// PGSequencesRows builds dbOid's own row-set for the pg_catalog.pg_sequences
// virtual view (registered by internal/initdb's registerPgSequencesView,
// which calls this with catalog.DefaultDBOid for the unwired/test-fixture
// default path — a per-connection dbOid is wired in via
// internal/server/dispatch.go's wireExtensionRows, consumed by
// internal/executor/operators.go's valuesOp.Open). Unlike pg_sequence
// (singular, a real catalog.InMemory-backed table needing the
// PGSequenceRowsForDBOid cross-package interface), pg_sequences reads
// straight from this package's own seqRegistry via AllSequenceInfos, so no
// catalog indirection is needed. M0122-0007 4e follow-up 35 (deferred by
// follow-up 34).
func PGSequencesRows(dbOid uint32) [][]string {
	infos := sortedSequenceInfos(dbOid)
	rows := make([][]string, len(infos))
	for i, seq := range infos {
		base := []string{
			seq.Schema,
			seq.Name,
			"", // sequenceowner — not tracked
			seq.DataType,
			fmt.Sprintf("%d", seq.Start),
			fmt.Sprintf("%d", seq.Min),
			fmt.Sprintf("%d", seq.Max),
			fmt.Sprintf("%d", seq.Increment),
			boolTextSeq(seq.Cycle),
			"1", // cache_size — not tracked, default 1
		}
		if seq.Called {
			// Append last_value; omitting it leaves last_value as NULL.
			base = append(base, fmt.Sprintf("%d", seq.LastValue))
		}
		rows[i] = base
	}
	return rows
}

// InformationSchemaSequencesRows builds dbOid's own row-set for the
// information_schema.sequences virtual view (registered by
// internal/initdb's registerInformationSchemaSequencesView). Mirrors
// PGSequencesRows above. M0122-0007 4e follow-up 35.
func InformationSchemaSequencesRows(dbOid uint32) [][]string {
	infos := sortedSequenceInfos(dbOid)
	rows := make([][]string, len(infos))
	for i, seq := range infos {
		dt, prec := seqDataTypePrecision(seq.DataType)
		cycleOpt := "NO"
		if seq.Cycle {
			cycleOpt = "YES"
		}
		rows[i] = []string{
			"postgres", // sequence_catalog = current database
			seq.Schema,
			seq.Name,
			dt,
			fmt.Sprintf("%d", prec),
			"2", // numeric_precision_radix — always binary
			"0", // numeric_scale — always 0 for integer types
			fmt.Sprintf("%d", seq.Start),
			fmt.Sprintf("%d", seq.Min),
			fmt.Sprintf("%d", seq.Max),
			fmt.Sprintf("%d", seq.Increment),
			cycleOpt,
		}
	}
	return rows
}

// seqDataTypePrecision maps a sequence data type name to the canonical
// PostgreSQL type name and its numeric_precision value. Moved from
// internal/initdb's information_schema_sequences_view.go when its row
// building moved here (M0122-0007 4e follow-up 35).
func seqDataTypePrecision(dt string) (typeName string, precision int) {
	switch dt {
	case "smallint", "int2":
		return "smallint", 16
	case "integer", "int4", "int":
		return "integer", 32
	default: // bigint / int8 (default)
		return "bigint", 64
	}
}

// boolTextSeq formats a bool as PostgreSQL's text-mode boolean output ("t"/"f").
// Local to avoid depending on internal/initdb's boolText helper (executor
// cannot import initdb — would create an import cycle).
func boolTextSeq(b bool) string {
	if b {
		return "t"
	}
	return "f"
}

// init wires the catalog's pg_sequence (singular) row builder to the executor's
// sequence registry. The catalog owns OID resolution (seqrelid); the executor
// owns the per-sequence parameters. M0110-0001 (DU-002 slice 115).
func init() {
	catalog.SequenceParamsFunc = sequenceParamsForCatalog
}

// sequenceParamsForCatalog returns the pg_sequence parameter row for the named
// sequence in dbOid's registry, or ok=false if no such sequence is
// registered there. Called by the catalog's PGSequenceRowsForDBOid/
// PGDependRowsForDBOid for each IsSequence relation. M0122-0007 4e follow-up
// 34 added the dbOid parameter (previously implicitly DefaultDBOid via
// LookupSequence's zero-value variadic).
func sequenceParamsForCatalog(qualifiedName string, dbOid uint32) (catalog.SeqParams, bool) {
	s := LookupSequence(qualifiedName, dbOid)
	if s == nil {
		return catalog.SeqParams{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := s.dataType
	if dt == "" {
		dt = "bigint"
	}
	cache := s.cache
	if cache < 1 {
		cache = 1 // PG default; guards sequences registered before cache tracking
	}
	return catalog.SeqParams{
		TypeOID:   seqTypeOID(dt),
		Start:     s.start,
		Increment: s.increment,
		Max:       s.max,
		Min:       s.min,
		Cache:     cache,
		Cycle:     s.cycle,
		OwnedBy:   s.ownedBy, // "table.column" for OWNED BY sequences; "" otherwise
	}, true
}

// seqTypeOID maps a sequence's declared data type to its pg_type OID, which
// pg_dump renders via format_type(seqtypid, NULL). bigint is the default.
func seqTypeOID(dataType string) uint32 {
	switch dataType {
	case "smallint", "int2":
		return 21
	case "integer", "int", "int4":
		return 23
	default: // "bigint", "int8"
		return 20
	}
}
