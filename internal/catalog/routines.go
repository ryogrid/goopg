package catalog

// User-defined routine registry (M0015 Stage A step 2). Pure
// in-memory v0 — persistence lands when the on-disk catalog work
// reaches `pg_proc` (currently TBD; mirrors pg_class persistence,
// which itself is M0007+ scope).
//
// The registry's job at this step is purely catalog-side bookkeeping:
// CreateFunctionStmt / DropFunctionStmt have a place to land their
// state, the analyzer / executor get a clean lookup surface for the
// next slice, and `pg_catalog.pg_proc` virtual-view introspection
// works.
//
// The registry is process-local; routines created in one session
// are visible to all sessions immediately (matches the in-memory
// pg_class semantics goopg already ships).
//
// See docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/parser"
)

// Routine is one user-defined routine (function in Stage A;
// procedures join in Stage B). Stored verbatim — the analyzer /
// PL/pgSQL interpreter consumes ArgTypes / ReturnType / Language /
// Body once those slices land.
type Routine struct {
	OID             uint32
	Schema          string
	Name            string
	ArgNames        []string // parallel to ArgTypes; empty string for positional-only args
	ArgTypes        []Type
	ArgModes        []string // parallel to ArgTypes; "i"=IN, "o"=OUT, "b"=INOUT, "v"=VARIADIC; nil=all IN
	ArgDefaults     []string // parallel to ArgTypes; raw SQL expression for DEFAULT, "" = no default
	ReturnType      Type
	ReturnsSet      bool   // RETURNS SETOF ... M0097-0020
	ReturnsTable    bool   // RETURNS TABLE (...) — table cols stored as trailing OUT args
	Language        string // lower-cased
	Body            string // raw routine source between the dollar-quote delimiters
	Strict          bool   // STRICT / CALLED ON NULL INPUT
	Volatile        string // "v"=volatile (default), "s"=stable, "i"=immutable
	Parallel        string // proparallel: "u"=unsafe (default), "s"=safe, "r"=restricted
	Cost            string // procost: COST n override; "" = language default (1 internal/C, else 100)
	Rows            string // prorows: ROWS n override for SRFs; "" = default (1000 for SRF, 0 otherwise)
	SecurityDefiner bool   // SECURITY DEFINER
	Leakproof       bool   // LEAKPROOF
	IsProcedure     bool   // true when created via CREATE PROCEDURE
	IsWindow        bool   // true when created with WINDOW attribute
	BeginAtomic     bool   // PG14 BEGIN ATOMIC ... END body (no AS keyword)
	IsReturnForm    bool   // PG14 RETURN expr body (stored as "SELECT expr" internally)
	KindChar        string // prokind: 'f'=function, 'p'=procedure, 'w'=window, 'a'=aggregate
	// Owner is pg_proc.proowner (role OID), settable via ALTER FUNCTION/
	// PROCEDURE/ROUTINE ... OWNER TO. 0 means "unset, defaults to the
	// bootstrap superuser" — see OwnerOrDefault. M0097-0150.
	Owner uint32

	// Config is pg_proc.proconfig: a list of "name=value" per-function GUC
	// overrides set via CREATE FUNCTION's SET clause or ALTER FUNCTION/
	// PROCEDURE/ROUTINE ... SET/RESET, in insertion order (mirrors
	// InMemory's role/database-level roleSettings shape). nil renders as SQL
	// NULL (matches upstream: a routine that never had a SET clause has no
	// proconfig row at all). Dump-fidelity only — goopg does not push/pop
	// these values around the routine's own execution (no per-call GUC
	// scoping exists yet); see docs/design/0122-0007-function-set-config.md.
	Config []string

	// Dependency tracking populated at CREATE FUNCTION time for information_schema views.
	SequenceDeps    []RoutineSeqDep   // sequences referenced via nextval/currval
	RoutineCallOIDs []uint32          // OIDs of routines called in body/defaults
	TableDeps       []RoutineTableRef // tables referenced in FROM clauses
	ColumnDeps      []RoutineColRef   // columns referenced in SELECT/WHERE clauses

	// DBOid is the database this routine was CREATE FUNCTION/PROCEDURE'd
	// under (mirrors catalog.Domain.DBOid/EnumType.DBOid). The registry key
	// folds this in (routineKey/nameKey) so two distinct databases may each
	// register a same-named routine without colliding. Zero is normalized to
	// DefaultDBOid by Create/CreateDuringRecovery, matching every other
	// dbOid-scoped registry's convention. M0119-0004 DU-002 follow-up
	// (M0122-0007 4e-shaped fix, mirrors follow-up 36/37's ForeignServer/
	// UserMapping DBOid fields).
	DBOid uint32
}

// OwnerOrDefault returns r.Owner, falling back to the bootstrap superuser OID
// (10) for routines created before ALTER FUNCTION ... OWNER TO ever ran.
// Mirrors UserAggregate.OwnerOrDefault. M0097-0150.
func (r *Routine) OwnerOrDefault() uint32 {
	if r.Owner == 0 {
		return 10
	}
	return r.Owner
}

// RoutineSeqDep records a sequence dependency of a routine.
type RoutineSeqDep struct {
	Schema string
	Name   string
}

// RoutineTableRef records a table/view dependency of a routine.
type RoutineTableRef struct {
	Schema string
	Name   string
}

// RoutineColRef records a column dependency of a routine.
type RoutineColRef struct {
	TableSchema string
	TableName   string
	ColumnName  string
}

// QualifiedName returns the upstream-style schema-qualified routine
// name for log lines and error messages.
func (r *Routine) QualifiedName() string {
	if r.Schema == "" {
		return r.Name
	}
	return r.Schema + "." + r.Name
}

// Signature is the upstream overload key: `(arg1_type,arg2_type,…)`.
// Stage A uses type-name comparison directly; the future overload
// resolver will normalise via the type system.
func (r *Routine) Signature() string {
	var parts []string
	for i, t := range r.ArgTypes {
		mode := "i"
		if i < len(r.ArgModes) && r.ArgModes[i] != "" {
			mode = r.ArgModes[i]
		}
		// OUT params are excluded from the signature (matches PostgreSQL's proargtypes).
		if mode == "o" {
			continue
		}
		parts = append(parts, strings.ToLower(t.Name))
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// Routines is the process-wide routine registry. Guarded by a
// single RWMutex — routine cardinality is tiny relative to table
// rowcounts.
type Routines struct {
	mu      sync.RWMutex
	byKey   map[string]*Routine // schema.name(argtypes) → routine
	byName  map[string][]string // schema.name → list of overload keys
	nextOID uint32
}

var (
	// ErrRoutineExists is returned by Create when a routine with
	// the same schema+name+signature already exists and the caller
	// did not request OR REPLACE.
	ErrRoutineExists = errors.New("function already exists with the same argument types")
	// ErrRoutineNotFound is returned by Drop / Lookup when the
	// requested signature does not resolve.
	ErrRoutineNotFound = errors.New("function does not exist")
	// ErrRoutineAmbiguous is returned by DropByName when more than
	// one overload exists for the bare name. Callers must supply
	// the argument list to disambiguate.
	ErrRoutineAmbiguous = errors.New("function name is not unique")
	// ErrRoutineKindChange is returned when CREATE OR REPLACE tries to change
	// a function to a procedure or vice versa.
	ErrRoutineKindChange = errors.New("cannot change routine kind")
)

// FirstRoutineOID is the first OID handed out for user routines —
// reserved low OIDs leave room for future built-in / catalog-seeded
// rows. Picked above FirstUserOID so it never collides with the
// table-OID space.
const FirstRoutineOID uint32 = 1 << 17

// NewRoutines returns an empty registry.
func NewRoutines() *Routines {
	return &Routines{
		byKey:   make(map[string]*Routine),
		byName:  make(map[string][]string),
		nextOID: FirstRoutineOID,
	}
}

// routineDBPrefix folds dbOid into a registry key, mirroring domainKey's
// "<dbOid>\x00" prefix convention (M0119-0004 DU-002 follow-up).
func routineDBPrefix(dbOid uint32) string {
	return strconv.FormatUint(uint64(dbOid), 10) + "\x00"
}

func routineKey(dbOid uint32, schema, name, signature string) string {
	return routineDBPrefix(dbOid) + schema + "." + name + signature
}

func nameKey(dbOid uint32, schema, name string) string {
	return routineDBPrefix(dbOid) + schema + "." + name
}

// Create registers a routine. With orReplace=false, returns
// ErrRoutineExists when a routine with the same signature is
// already registered. With orReplace=true, replaces the body /
// language / return type in-place (preserving the OID so any
// existing references stay valid).
func (rs *Routines) Create(r *Routine, orReplace bool) (*Routine, error) {
	if r == nil {
		return nil, fmt.Errorf("Routines.Create: nil routine")
	}
	if r.Name == "" {
		return nil, fmt.Errorf("Routines.Create: empty routine name")
	}
	schema := r.Schema
	if schema == "" {
		schema = "public"
	}
	clone := *r
	clone.Schema = schema
	clone.Language = strings.ToLower(clone.Language)
	if clone.DBOid == 0 {
		clone.DBOid = DefaultDBOid
	}
	signature := clone.Signature()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	k := routineKey(clone.DBOid, schema, clone.Name, signature)
	if existing, ok := rs.byKey[k]; ok {
		if !orReplace {
			return nil, fmt.Errorf("%w: %s%s", ErrRoutineExists, clone.QualifiedName(), signature)
		}
		// Cannot change a function to a procedure or vice versa, or change
		// a regular function to a window function (or vice versa).
		if existing.IsProcedure != clone.IsProcedure {
			return nil, fmt.Errorf("%w: %s", ErrRoutineKindChange, clone.QualifiedName())
		}
		if existing.IsWindow != clone.IsWindow {
			return nil, fmt.Errorf("%w: %s", ErrRoutineKindChange, clone.QualifiedName())
		}
		// CREATE OR REPLACE preserves the OID — upstream's contract.
		clone.OID = existing.OID
		rs.byKey[k] = &clone
		return &clone, nil
	}
	clone.OID = rs.nextOID
	rs.nextOID++
	rs.byKey[k] = &clone
	nk := nameKey(clone.DBOid, schema, clone.Name)
	rs.byName[nk] = append(rs.byName[nk], k)
	return &clone, nil
}

// CreateDuringRecovery is the idempotent version of Create used by the
// WAL-replay driver (internal/initdb/function_ddl_recovery.go). Unlike
// Create it takes the OID from the WAL record (so the recovered registry
// matches what the pre-crash server assigned) and advances nextOID past it
// so subsequent allocations do not collide. Re-applying a record for a
// signature that already exists (idempotent replay, or a CREATE OR REPLACE
// record following its own earlier CREATE) just overwrites it in place
// without duplicating the byName index entry. Mirrors
// RegisterUserAggregateDuringRecovery. Returns the stored routine pointer
// (not r itself — Create's own clone-on-write contract) so the caller can
// populate dependency-tracking fields on the object actually in the
// registry. DU-002 restart-persistence follow-up (M0119-0004, loop #71
// ledger resume point).
func (rs *Routines) CreateDuringRecovery(r *Routine) *Routine {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	schema := r.Schema
	if schema == "" {
		schema = "public"
	}
	clone := *r
	clone.Schema = schema
	clone.Language = strings.ToLower(clone.Language)
	// WAL payloads don't carry a dbOid yet (restart-persistence follow-up
	// above) — recovery always replays into DefaultDBOid, matching the
	// single-database replay convention CreateSequenceCatalogRelation
	// documents for the same reason.
	clone.DBOid = DefaultDBOid
	k := routineKey(clone.DBOid, schema, clone.Name, clone.Signature())
	if _, exists := rs.byKey[k]; !exists {
		nk := nameKey(clone.DBOid, schema, clone.Name)
		rs.byName[nk] = append(rs.byName[nk], k)
	}
	rs.byKey[k] = &clone
	if clone.OID >= rs.nextOID {
		rs.nextOID = clone.OID + 1
	}
	return &clone
}

// DropByOIDDuringRecovery is the idempotent counterpart to
// CreateDuringRecovery used for replaying a DROP FUNCTION/PROCEDURE record.
// A missing OID (already-dropped, or a not-yet-applicable ordering) is a
// silent no-op — recovery must not abort on it. DU-002 restart-persistence
// follow-up (M0119-0004, loop #71 ledger resume point).
func (rs *Routines) DropByOIDDuringRecovery(oid uint32) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for k, r := range rs.byKey {
		if r.OID != oid {
			continue
		}
		delete(rs.byKey, k)
		nk := nameKey(r.DBOid, r.Schema, r.Name)
		keys := rs.byName[nk]
		for i, kk := range keys {
			if kk == k {
				rs.byName[nk] = append(keys[:i], keys[i+1:]...)
				break
			}
		}
		if len(rs.byName[nk]) == 0 {
			delete(rs.byName, nk)
		}
		return
	}
}

// RenameByOIDDuringRecovery is the discard-result recovery counterpart to
// RenameRoutine used for replaying an ALTER FUNCTION ... RENAME TO record.
// A rename record can only be replayed after its routine's CREATE record
// (WAL is scanned in order), so a not-found OID is not expected in
// practice, but replay must not abort on it. DU-002 restart-persistence
// follow-up (M0119-0004, loop #71 ledger resume point).
func (rs *Routines) RenameByOIDDuringRecovery(oid uint32, newName string) {
	rs.mu.RLock()
	var r *Routine
	for _, cand := range rs.byKey {
		if cand.OID == oid {
			r = cand
			break
		}
	}
	rs.mu.RUnlock()
	if r != nil {
		_ = rs.RenameRoutine(r, newName)
	}
}

// SetFlagsByOIDDuringRecovery is the recovery counterpart used for
// replaying an ALTER FUNCTION/PROCEDURE/ROUTINE attribute-change record —
// the four mutable fields are overwritten unconditionally with the
// post-mutation snapshot the WAL record carries. A missing OID is a silent
// no-op, mirroring RenameByOIDDuringRecovery. DU-002 restart-persistence
// follow-up (M0119-0004, loop #71 ledger resume point).
func (rs *Routines) SetFlagsByOIDDuringRecovery(oid uint32, volatile string, securityDefiner, leakproof, strict bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.byKey {
		if r.OID == oid {
			r.Volatile = volatile
			r.SecurityDefiner = securityDefiner
			r.Leakproof = leakproof
			r.Strict = strict
			return
		}
	}
}

// Lookup returns the routine matching schema+name+argtypes, or
// false. Stage A uses exact type-name match — the upstream coercion
// rules arrive when the type system grows up.
// When schema is empty, searches all schemas.
// dbOid is a trailing variadic parameter (mirrors catalog.InMemory's
// resolveDBOid convention) so pre-existing callers that don't pass one keep
// resolving against DefaultDBOid unchanged. M0119-0004 DU-002 follow-up.
func (rs *Routines) Lookup(name parser.ObjectName, argTypes []Type, dbOid ...uint32) (*Routine, bool) {
	return rs.LookupWithArgModes(name, argTypes, nil, dbOid...)
}

// LookupWithArgModes is Lookup's twin for callers holding the parsed
// parameter modes (ALTER/DROP FUNCTION, COMMENT ON FUNCTION). Those
// statements resolve by the same identity-argument list PG's
// pg_get_function_identity_arguments emits, which still carries OUT
// parameters (ruleutils.c print_function_arguments only omits TABLE-mode
// args from identity output, not OUT) — so the lookup stub needs argModes
// populated for Routine.Signature() to correctly exclude them from the
// match, exactly like pg_proc.proargtypes does. Passing nil argModes
// reproduces Lookup's plain behavior (every arg treated as IN).
func (rs *Routines) LookupWithArgModes(name parser.ObjectName, argTypes []Type, argModes []string, dbOid ...uint32) (*Routine, bool) {
	stub := &Routine{Name: name.Name, ArgTypes: argTypes, ArgModes: argModes}
	sig := stub.Signature()
	db := resolveDBOid(dbOid)
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	if name.Schema != "" {
		r, ok := rs.byKey[routineKey(db, name.Schema, name.Name, sig)]
		return r, ok
	}
	// No schema: search all schemas. Try public first for compatibility,
	// then others.
	if r, ok := rs.byKey[routineKey(db, "public", name.Name, sig)]; ok {
		return r, ok
	}
	for _, r := range rs.byKey {
		if r.DBOid == db && strings.EqualFold(r.Name, name.Name) && r.Signature() == sig {
			return r, true
		}
	}
	return nil, false
}

// LookupByName returns every overload of the given schema+name.
// When schema is empty, searches all schemas (search-path style).
// dbOid is a trailing variadic parameter, mirroring Lookup.
func (rs *Routines) LookupByName(name parser.ObjectName, dbOid ...uint32) []*Routine {
	db := resolveDBOid(dbOid)
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	if name.Schema != "" {
		keys := rs.byName[nameKey(db, name.Schema, name.Name)]
		out := make([]*Routine, 0, len(keys))
		for _, k := range keys {
			if r, ok := rs.byKey[k]; ok {
				out = append(out, r)
			}
		}
		return out
	}
	// No schema specified: collect matches from all schemas.
	var out []*Routine
	for _, r := range rs.byKey {
		if r.DBOid == db && strings.EqualFold(r.Name, name.Name) {
			out = append(out, r)
		}
	}
	return out
}

// Drop removes the routine with the given schema+name+argtypes.
// Returns ErrRoutineNotFound when the signature doesn't resolve;
// the caller maps that to the SQL-level IF EXISTS contract.
// When schema is empty, searches all schemas.
func (rs *Routines) Drop(name parser.ObjectName, argTypes []Type, dbOid ...uint32) error {
	stub := &Routine{Name: name.Name, ArgTypes: argTypes}
	signature := stub.Signature()
	schema := name.Schema
	db := resolveDBOid(dbOid)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	// Resolve schema when not provided: try public first, then all schemas.
	if schema == "" {
		if _, ok := rs.byKey[routineKey(db, "public", name.Name, signature)]; ok {
			schema = "public"
		} else {
			for _, r := range rs.byKey {
				if r.DBOid == db && strings.EqualFold(r.Name, name.Name) && r.Signature() == signature {
					schema = r.Schema
					break
				}
			}
		}
		if schema == "" {
			return fmt.Errorf("%w: %s%s", ErrRoutineNotFound, name.Name, signature)
		}
	}
	k := routineKey(db, schema, name.Name, signature)
	if _, ok := rs.byKey[k]; !ok {
		return fmt.Errorf("%w: %s.%s%s", ErrRoutineNotFound, schema, name.Name, signature)
	}
	delete(rs.byKey, k)
	nk := nameKey(db, schema, name.Name)
	keys := rs.byName[nk]
	for i, kk := range keys {
		if kk == k {
			rs.byName[nk] = append(keys[:i], keys[i+1:]...)
			break
		}
	}
	if len(rs.byName[nk]) == 0 {
		delete(rs.byName, nk)
	}
	return nil
}

// DropByName removes a routine when only the bare name was
// supplied to DROP FUNCTION (no argument list). Returns
// ErrRoutineAmbiguous if more than one overload exists — the
// caller surfaces the upstream "function name is not unique"
// SQLSTATE 42725. When schema is empty, searches all schemas.
func (rs *Routines) DropByName(name parser.ObjectName, dbOid ...uint32) error {
	schema := name.Schema
	db := resolveDBOid(dbOid)
	prefix := routineDBPrefix(db)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	// Resolve schema when not specified.
	if schema == "" {
		// Collect all matching keys across schemas within this database.
		var allKeys []string
		for nk, keys := range rs.byName {
			// nameKey = "<dbOid>\x00schema.name"; check dbOid prefix + name suffix
			if strings.HasPrefix(nk, prefix) && strings.HasSuffix(nk, "."+strings.ToLower(name.Name)) {
				allKeys = append(allKeys, keys...)
			}
		}
		switch len(allKeys) {
		case 0:
			return fmt.Errorf("%w: %s", ErrRoutineNotFound, name.Name)
		case 1:
			k := allKeys[0]
			r := rs.byKey[k]
			delete(rs.byKey, k)
			if r != nil {
				nk := nameKey(r.DBOid, r.Schema, name.Name)
				delete(rs.byName, nk)
			}
			return nil
		default:
			return fmt.Errorf("%w: %s", ErrRoutineAmbiguous, name.Name)
		}
	}
	keys := rs.byName[nameKey(db, schema, name.Name)]
	switch len(keys) {
	case 0:
		return fmt.Errorf("%w: %s.%s", ErrRoutineNotFound, schema, name.Name)
	case 1:
		k := keys[0]
		delete(rs.byKey, k)
		delete(rs.byName, nameKey(db, schema, name.Name))
		return nil
	default:
		return fmt.Errorf("%w: %s.%s has %d overloads", ErrRoutineAmbiguous, schema, name.Name, len(keys))
	}
}

// DropRoutine removes the specific routine r from the registry.
// It uses the routine's OID for exact matching to avoid any signature
// ambiguity (important for DROP PROCEDURE with OUT params).
func (rs *Routines) DropRoutine(r *Routine) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	schema := r.Schema
	if schema == "" {
		schema = "public"
	}
	sig := r.Signature()
	k := routineKey(r.DBOid, schema, r.Name, sig)
	if _, ok := rs.byKey[k]; !ok {
		return fmt.Errorf("%w: %s.%s%s", ErrRoutineNotFound, schema, r.Name, sig)
	}
	delete(rs.byKey, k)
	nk := nameKey(r.DBOid, schema, r.Name)
	keys := rs.byName[nk]
	for i, kk := range keys {
		if kk == k {
			rs.byName[nk] = append(keys[:i], keys[i+1:]...)
			break
		}
	}
	if len(rs.byName[nk]) == 0 {
		delete(rs.byName, nk)
	}
	return nil
}

// ResolveByName resolves the single routine matching a bare name (no argument
// list) WITHOUT removing it — the read-only twin of DropByName. Used by deferred
// DROP FUNCTION (M0118-0009 `stats`), which must identify the target routine at
// statement time but defer its registry removal to COMMIT. Returns the same
// error contract as DropByName: ErrRoutineNotFound / ErrRoutineAmbiguous. When
// schema is empty, searches all schemas.
func (rs *Routines) ResolveByName(name parser.ObjectName, dbOid ...uint32) (*Routine, error) {
	schema := name.Schema
	db := resolveDBOid(dbOid)
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	if schema == "" {
		prefix := routineDBPrefix(db)
		var allKeys []string
		for nk, keys := range rs.byName {
			if strings.HasPrefix(nk, prefix) && strings.HasSuffix(nk, "."+strings.ToLower(name.Name)) {
				allKeys = append(allKeys, keys...)
			}
		}
		switch len(allKeys) {
		case 0:
			return nil, fmt.Errorf("%w: %s", ErrRoutineNotFound, name.Name)
		case 1:
			return rs.byKey[allKeys[0]], nil
		default:
			return nil, fmt.Errorf("%w: %s", ErrRoutineAmbiguous, name.Name)
		}
	}
	keys := rs.byName[nameKey(db, schema, name.Name)]
	switch len(keys) {
	case 0:
		return nil, fmt.Errorf("%w: %s.%s", ErrRoutineNotFound, schema, name.Name)
	case 1:
		return rs.byKey[keys[0]], nil
	default:
		return nil, fmt.Errorf("%w: %s.%s has %d overloads", ErrRoutineAmbiguous, schema, name.Name, len(keys))
	}
}

// ResolveBySig resolves the routine with the given schema+name+argtypes WITHOUT
// removing it — the read-only twin of Drop, used by deferred DROP FUNCTION
// (M0118-0009 `stats`). Returns ErrRoutineNotFound when the signature doesn't
// resolve. When schema is empty, searches public then all schemas.
func (rs *Routines) ResolveBySig(name parser.ObjectName, argTypes []Type, dbOid ...uint32) (*Routine, error) {
	return rs.ResolveBySigWithArgModes(name, argTypes, nil, dbOid...)
}

// ResolveBySigWithArgModes is ResolveBySig's twin for callers holding the
// parsed parameter modes — see LookupWithArgModes for why DROP FUNCTION's
// deferred/autocommit removal paths need this (DROP FUNCTION on a routine
// with OUT parameters, given the identity-argument list including OUT,
// per PG's LookupFuncWithArgs).
func (rs *Routines) ResolveBySigWithArgModes(name parser.ObjectName, argTypes []Type, argModes []string, dbOid ...uint32) (*Routine, error) {
	stub := &Routine{Name: name.Name, ArgTypes: argTypes, ArgModes: argModes}
	signature := stub.Signature()
	schema := name.Schema
	db := resolveDBOid(dbOid)
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	if schema == "" {
		if _, ok := rs.byKey[routineKey(db, "public", name.Name, signature)]; ok {
			schema = "public"
		} else {
			for _, r := range rs.byKey {
				if r.DBOid == db && strings.EqualFold(r.Name, name.Name) && r.Signature() == signature {
					schema = r.Schema
					break
				}
			}
		}
		if schema == "" {
			return nil, fmt.Errorf("%w: %s%s", ErrRoutineNotFound, name.Name, signature)
		}
	}
	r, ok := rs.byKey[routineKey(db, schema, name.Name, signature)]
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s%s", ErrRoutineNotFound, schema, name.Name, signature)
	}
	return r, nil
}

// DropRoutinesReferencingTypes drops every registered routine whose argument
// or return type name matches one of typeNames (case-insensitive), returning
// the dropped routines. It backs temporary-object cleanup (DISCARD TEMP /
// backend exit): dropping a temporary table also drops its implicit composite
// rowtype, which in PostgreSQL cascades (via pg_depend) to any function that
// takes or returns that rowtype — e.g. the spec's
// uses_a_temp_type(just_give_me_a_type). goopg has no OID-level type-dependency
// graph, so the temp table's (session-unique) name is the dependency signal;
// callers pass the names of the just-dropped temp tables. M0118-0009
// (temp-schema-cleanup).
func (rs *Routines) DropRoutinesReferencingTypes(typeNames []string) []*Routine {
	if len(typeNames) == 0 {
		return nil
	}
	want := make(map[string]bool, len(typeNames))
	for _, n := range typeNames {
		if n != "" {
			want[strings.ToLower(n)] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var dropped []*Routine
	for k, r := range rs.byKey {
		if r == nil {
			continue
		}
		referenced := want[strings.ToLower(r.ReturnType.Name)]
		if !referenced {
			for _, at := range r.ArgTypes {
				if want[strings.ToLower(at.Name)] {
					referenced = true
					break
				}
			}
		}
		if !referenced {
			continue
		}
		delete(rs.byKey, k)
		schema := r.Schema
		if schema == "" {
			schema = "public"
		}
		nk := nameKey(r.DBOid, schema, r.Name)
		keys := rs.byName[nk]
		for i, kk := range keys {
			if kk == k {
				rs.byName[nk] = append(keys[:i], keys[i+1:]...)
				break
			}
		}
		if len(rs.byName[nk]) == 0 {
			delete(rs.byName, nk)
		}
		dropped = append(dropped, r)
	}
	return dropped
}

// List returns every registered routine in deterministic OID order.
// Used by `pg_catalog.pg_proc`'s virtual-row provider.
// LookupByOID returns the routine with the given OID, or nil if not found.
func (rs *Routines) LookupByOID(oid uint32) *Routine {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	for _, r := range rs.byKey {
		if r.OID == oid {
			return r
		}
	}
	return nil
}

// RenameRoutine changes the Name of a routine in the registry.
// Updates both byKey and byName indices. M0097-0022.
func (rs *Routines) RenameRoutine(r *Routine, newName string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	oldKey := routineKey(r.DBOid, r.Schema, r.Name, r.Signature())
	if _, ok := rs.byKey[oldKey]; !ok {
		return fmt.Errorf("routine %s.%s not found in registry", r.Schema, r.Name)
	}
	oldNK := nameKey(r.DBOid, r.Schema, r.Name)
	// Remove old key from byKey and byName.
	delete(rs.byKey, oldKey)
	oldList := rs.byName[oldNK]
	for i, k := range oldList {
		if k == oldKey {
			rs.byName[oldNK] = append(oldList[:i], oldList[i+1:]...)
			break
		}
	}
	if len(rs.byName[oldNK]) == 0 {
		delete(rs.byName, oldNK)
	}
	// Update the routine name.
	r.Name = newName
	newKey := routineKey(r.DBOid, r.Schema, r.Name, r.Signature())
	newNK := nameKey(r.DBOid, r.Schema, newName)
	// Insert under new key and name.
	rs.byKey[newKey] = r
	rs.byName[newNK] = append(rs.byName[newNK], newKey)
	return nil
}

// SetSchema changes the Schema of a routine (ALTER FUNCTION/PROCEDURE/
// ROUTINE ... SET SCHEMA). Schema is part of both indices' keys, so this
// re-keys byKey/byName exactly like RenameRoutine, just on Schema instead of
// Name. M0097-0150.
func (rs *Routines) SetSchema(r *Routine, newSchema string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	oldKey := routineKey(r.DBOid, r.Schema, r.Name, r.Signature())
	if _, ok := rs.byKey[oldKey]; !ok {
		return fmt.Errorf("routine %s.%s not found in registry", r.Schema, r.Name)
	}
	oldNK := nameKey(r.DBOid, r.Schema, r.Name)
	delete(rs.byKey, oldKey)
	oldList := rs.byName[oldNK]
	for i, k := range oldList {
		if k == oldKey {
			rs.byName[oldNK] = append(oldList[:i], oldList[i+1:]...)
			break
		}
	}
	if len(rs.byName[oldNK]) == 0 {
		delete(rs.byName, oldNK)
	}
	r.Schema = newSchema
	newKey := routineKey(r.DBOid, r.Schema, r.Name, r.Signature())
	newNK := nameKey(r.DBOid, r.Schema, r.Name)
	rs.byKey[newKey] = r
	rs.byName[newNK] = append(rs.byName[newNK], newKey)
	return nil
}

// SetSchemaByOIDDuringRecovery is the recovery counterpart to SetSchema used
// for replaying an ALTER FUNCTION ... SET SCHEMA record. Mirrors
// RenameByOIDDuringRecovery: a missing OID is a silent no-op. M0097-0150.
func (rs *Routines) SetSchemaByOIDDuringRecovery(oid uint32, newSchema string) {
	rs.mu.RLock()
	var r *Routine
	for _, cand := range rs.byKey {
		if cand.OID == oid {
			r = cand
			break
		}
	}
	rs.mu.RUnlock()
	if r != nil {
		_ = rs.SetSchema(r, newSchema)
	}
}

// SetOwnerByOIDDuringRecovery is the recovery counterpart used for replaying
// an ALTER FUNCTION/PROCEDURE/ROUTINE ... OWNER TO record. Owner is not part
// of either index key, so this is a direct field write. Mirrors
// SetFlagsByOIDDuringRecovery. M0097-0150.
func (rs *Routines) SetOwnerByOIDDuringRecovery(oid, ownerOID uint32) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.byKey {
		if r.OID == oid {
			r.Owner = ownerOID
			return
		}
	}
}

// routineConfigName extracts the GUC name from a "name=value" pg_proc.
// proconfig-shaped entry, mirroring catalog.go's dbRoleSettingConfigName for
// the analogous pg_db_role_setting.setconfig entries.
func routineConfigName(entry string) string {
	if eq := strings.IndexByte(entry, '='); eq >= 0 {
		return entry[:eq]
	}
	return ""
}

// ApplyFunctionConfigOps applies a sequence of CREATE/ALTER FUNCTION SET/
// RESET clauses (in statement order, as parsed by parser.
// parseFunctionConfigSetClause/parseFunctionConfigResetClause) to cfg,
// returning the updated array. A SET upserts (replaces an existing
// same-name entry case-insensitively, else appends); RESET removes the
// named entry if present; RESET ALL clears everything. Used both to build a
// fresh routine's initial Config (execCreateFunction — cfg starts nil) and
// to mutate an already-registered routine's Config in place
// (execAlterFunction passes r.Config directly; Routine field mutation here
// is unsynchronized, matching every other ALTER FUNCTION attribute write in
// that function, e.g. `r.Volatile = *s.Volatile`). DU-002 proconfig
// follow-up to M0097-0150.
func ApplyFunctionConfigOps(cfg []string, ops []parser.FunctionConfigOp) []string {
	for _, op := range ops {
		switch {
		case op.ResetAll:
			cfg = nil
		case op.Reset:
			for i, e := range cfg {
				if strings.EqualFold(routineConfigName(e), op.Name) {
					cfg = append(cfg[:i], cfg[i+1:]...)
					break
				}
			}
		default:
			entry := op.Name + "=" + op.Value
			found := false
			for i, e := range cfg {
				if strings.EqualFold(routineConfigName(e), op.Name) {
					cfg[i] = entry
					found = true
					break
				}
			}
			if !found {
				cfg = append(cfg, entry)
			}
		}
	}
	return cfg
}

// ReplaceConfigByOIDDuringRecovery overwrites oid's entire Config array with
// cfg (a full post-mutation snapshot, not a diff — mirrors
// SetFlagsByOIDDuringRecovery's whole-state-replay shape) during WAL replay
// of a RecordKindAlterFunctionConfig record. A missing OID is a silent no-op,
// matching every other *ByOIDDuringRecovery method in this file.
func (rs *Routines) ReplaceConfigByOIDDuringRecovery(oid uint32, cfg []string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.byKey {
		if r.OID == oid {
			r.Config = append([]string(nil), cfg...)
			return
		}
	}
}

func (rs *Routines) List() []*Routine {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	out := make([]*Routine, 0, len(rs.byKey))
	for _, r := range rs.byKey {
		out = append(out, r)
	}
	// Sort by OID — assignment is monotonic so this matches
	// creation order, which the virtual view's tests rely on.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].OID > out[j].OID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// normalizeDropType normalizes type aliases to canonical names for DROP matching.
func normalizeDropType(t string) string {
	switch strings.ToLower(t) {
	case "int", "integer":
		return "int4"
	case "bigint":
		return "int8"
	case "smallint":
		return "int2"
	case "bool":
		return "boolean"
	case "float", "float8", "double precision":
		return "float8"
	case "float4", "real":
		return "float4"
	}
	return strings.ToLower(t)
}

// LookupDropCandidates returns routines matching a DROP arg list using
// full-arg-list matching (including OUT params). If args[i].ModeExplicit is
// false, any param mode at position i matches. If ModeExplicit is true,
// only matching modes are accepted.
func (rs *Routines) LookupDropCandidates(name parser.ObjectName, args []parser.FunctionArg, dbOid ...uint32) []*Routine {
	db := resolveDBOid(dbOid)
	dropTypes := make([]string, len(args))
	for i, a := range args {
		dropTypes[i] = normalizeDropType(a.Type.Name)
	}

	modeChar := func(a parser.FunctionArg) string {
		switch a.Mode {
		case parser.FuncArgOut:
			return "o"
		case parser.FuncArgInout:
			return "b"
		case parser.FuncArgVariadic:
			return "v"
		default:
			return "i"
		}
	}

	rs.mu.RLock()
	defer rs.mu.RUnlock()

	var cands []*Routine
	for _, r := range rs.byKey {
		if r.DBOid != db {
			continue
		}
		// Name must match
		if !strings.EqualFold(r.Name, name.Name) {
			continue
		}
		if name.Schema != "" && !strings.EqualFold(r.Schema, name.Schema) {
			continue
		}
		// Full arg count must match (including OUT params)
		if len(r.ArgTypes) != len(args) {
			continue
		}
		matched := true
		for i, dropArg := range args {
			// Type must match
			rType := normalizeDropType(r.ArgTypes[i].Name)
			if rType != dropTypes[i] {
				matched = false
				break
			}
			// Mode match: only filter if explicitly specified
			if dropArg.ModeExplicit {
				rMode := "i"
				if i < len(r.ArgModes) && r.ArgModes[i] != "" {
					rMode = r.ArgModes[i]
				}
				if rMode != modeChar(dropArg) {
					matched = false
					break
				}
			}
		}
		if matched {
			cands = append(cands, r)
		}
	}
	return cands
}
