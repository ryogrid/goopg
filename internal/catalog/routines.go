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
	OID        uint32
	Schema     string
	Name       string
	ArgNames   []string // parallel to ArgTypes; empty string for positional-only args
	ArgTypes   []Type
	ReturnType Type
	Language   string // lower-cased
	Body       string // raw routine source between the dollar-quote delimiters
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
	parts := make([]string, len(r.ArgTypes))
	for i, t := range r.ArgTypes {
		parts[i] = strings.ToLower(t.Name)
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
func (rs *Routines) Lookup(name parser.ObjectName, argTypes []Type) (*Routine, bool) {
	schema := name.Schema
	if schema == "" {
		schema = "public"
	}
	stub := &Routine{Schema: schema, Name: name.Name, ArgTypes: argTypes}
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	r, ok := rs.byKey[routineKey(schema, name.Name, stub.Signature())]
	return r, ok
}

// LookupByName returns every overload of the given schema+name.
// Used by DROP FUNCTION when no argument list was supplied (the
// caller decides how to handle ambiguity) and by future
// `pg_get_functiondef` introspection.
func (rs *Routines) LookupByName(name parser.ObjectName) []*Routine {
	schema := name.Schema
	if schema == "" {
		schema = "public"
	}
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	keys := rs.byName[nameKey(schema, name.Name)]
	out := make([]*Routine, 0, len(keys))
	for _, k := range keys {
		if r, ok := rs.byKey[k]; ok {
			out = append(out, r)
		}
	}
	return out
}

// Drop removes the routine with the given schema+name+argtypes.
// Returns ErrRoutineNotFound when the signature doesn't resolve;
// the caller maps that to the SQL-level IF EXISTS contract.
func (rs *Routines) Drop(name parser.ObjectName, argTypes []Type) error {
	schema := name.Schema
	if schema == "" {
		schema = "public"
	}
	stub := &Routine{Schema: schema, Name: name.Name, ArgTypes: argTypes}
	signature := stub.Signature()
	rs.mu.Lock()
	defer rs.mu.Unlock()
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
// SQLSTATE 42725.
func (rs *Routines) DropByName(name parser.ObjectName) error {
	schema := name.Schema
	if schema == "" {
		schema = "public"
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
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

// List returns every registered routine in deterministic OID order.
// Used by `pg_catalog.pg_proc`'s virtual-row provider.
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
