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

	// Dependency tracking populated at CREATE FUNCTION time for information_schema views.
	SequenceDeps    []RoutineSeqDep   // sequences referenced via nextval/currval
	RoutineCallOIDs []uint32          // OIDs of routines called in body/defaults
	TableDeps       []RoutineTableRef // tables referenced in FROM clauses
	ColumnDeps      []RoutineColRef   // columns referenced in SELECT/WHERE clauses
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

func routineKey(schema, name, signature string) string {
	return schema + "." + name + signature
}

func nameKey(schema, name string) string {
	return schema + "." + name
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
	signature := clone.Signature()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	k := routineKey(schema, clone.Name, signature)
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
	nk := nameKey(schema, clone.Name)
	rs.byName[nk] = append(rs.byName[nk], k)
	return &clone, nil
}

// Lookup returns the routine matching schema+name+argtypes, or
// false. Stage A uses exact type-name match — the upstream coercion
// rules arrive when the type system grows up.
// When schema is empty, searches all schemas.
func (rs *Routines) Lookup(name parser.ObjectName, argTypes []Type) (*Routine, bool) {
	stub := &Routine{Name: name.Name, ArgTypes: argTypes}
	sig := stub.Signature()
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	if name.Schema != "" {
		r, ok := rs.byKey[routineKey(name.Schema, name.Name, sig)]
		return r, ok
	}
	// No schema: search all schemas. Try public first for compatibility,
	// then others.
	if r, ok := rs.byKey[routineKey("public", name.Name, sig)]; ok {
		return r, ok
	}
	for _, r := range rs.byKey {
		if strings.EqualFold(r.Name, name.Name) && r.Signature() == sig {
			return r, true
		}
	}
	return nil, false
}

// LookupByName returns every overload of the given schema+name.
// When schema is empty, searches all schemas (search-path style).
func (rs *Routines) LookupByName(name parser.ObjectName) []*Routine {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	if name.Schema != "" {
		keys := rs.byName[nameKey(name.Schema, name.Name)]
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
		if strings.EqualFold(r.Name, name.Name) {
			out = append(out, r)
		}
	}
	return out
}

// Drop removes the routine with the given schema+name+argtypes.
// Returns ErrRoutineNotFound when the signature doesn't resolve;
// the caller maps that to the SQL-level IF EXISTS contract.
// When schema is empty, searches all schemas.
func (rs *Routines) Drop(name parser.ObjectName, argTypes []Type) error {
	stub := &Routine{Name: name.Name, ArgTypes: argTypes}
	signature := stub.Signature()
	schema := name.Schema
	rs.mu.Lock()
	defer rs.mu.Unlock()
	// Resolve schema when not provided: try public first, then all schemas.
	if schema == "" {
		if _, ok := rs.byKey[routineKey("public", name.Name, signature)]; ok {
			schema = "public"
		} else {
			for _, r := range rs.byKey {
				if strings.EqualFold(r.Name, name.Name) && r.Signature() == signature {
					schema = r.Schema
					break
				}
			}
		}
		if schema == "" {
			return fmt.Errorf("%w: %s%s", ErrRoutineNotFound, name.Name, signature)
		}
	}
	k := routineKey(schema, name.Name, signature)
	if _, ok := rs.byKey[k]; !ok {
		return fmt.Errorf("%w: %s.%s%s", ErrRoutineNotFound, schema, name.Name, signature)
	}
	delete(rs.byKey, k)
	nk := nameKey(schema, name.Name)
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
func (rs *Routines) DropByName(name parser.ObjectName) error {
	schema := name.Schema
	rs.mu.Lock()
	defer rs.mu.Unlock()
	// Resolve schema when not specified.
	if schema == "" {
		// Collect all matching keys across schemas.
		var allKeys []string
		for nk, keys := range rs.byName {
			// nameKey = "schema.name"; check suffix
			if strings.HasSuffix(nk, "."+strings.ToLower(name.Name)) {
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
				nk := nameKey(r.Schema, name.Name)
				delete(rs.byName, nk)
			}
			return nil
		default:
			return fmt.Errorf("%w: %s", ErrRoutineAmbiguous, name.Name)
		}
	}
	keys := rs.byName[nameKey(schema, name.Name)]
	switch len(keys) {
	case 0:
		return fmt.Errorf("%w: %s.%s", ErrRoutineNotFound, schema, name.Name)
	case 1:
		k := keys[0]
		delete(rs.byKey, k)
		delete(rs.byName, nameKey(schema, name.Name))
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
	k := routineKey(schema, r.Name, sig)
	if _, ok := rs.byKey[k]; !ok {
		return fmt.Errorf("%w: %s.%s%s", ErrRoutineNotFound, schema, r.Name, sig)
	}
	delete(rs.byKey, k)
	nk := nameKey(schema, r.Name)
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
	oldKey := routineKey(r.Schema, r.Name, r.Signature())
	if _, ok := rs.byKey[oldKey]; !ok {
		return fmt.Errorf("routine %s.%s not found in registry", r.Schema, r.Name)
	}
	oldNK := nameKey(r.Schema, r.Name)
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
	newKey := routineKey(r.Schema, r.Name, r.Signature())
	newNK := nameKey(r.Schema, newName)
	// Insert under new key and name.
	rs.byKey[newKey] = r
	rs.byName[newNK] = append(rs.byName[newNK], newKey)
	return nil
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
func (rs *Routines) LookupDropCandidates(name parser.ObjectName, args []parser.FunctionArg) []*Routine {
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
