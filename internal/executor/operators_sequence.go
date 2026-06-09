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
	"strings"
	"sync"
	"sync/atomic"
)

// seqState holds the mutable state of one sequence.
type seqState struct {
	current   atomic.Int64
	start     int64
	increment int64
	min       int64
	max       int64
	cycle     bool
	called    atomic.Bool // true after first nextval or setval
	ownedBy   string      // "table.column" set by ALTER SEQUENCE ... OWNED BY
	schema    string      // schema name (default "public")
	seqName   string      // bare sequence name (no schema prefix)
	dataType  string      // "smallint", "integer", or "bigint" (default)
	mu        sync.Mutex  // serialises nextval
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

// seqRegistry is the process-global sequence store.
var seqRegistry sync.Map // map[string]*seqState

// seqKey normalises a sequence name to a registry key.
func seqKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// RegisterSequence creates or replaces a sequence in the registry.
// Called by CREATE SEQUENCE and by SERIAL column initialisation.
func RegisterSequence(name string, start, increment, min, max int64, cycle bool) {
	schema, bare := splitSeqName(strings.ToLower(strings.TrimSpace(name)))
	s := &seqState{
		start:     start,
		increment: increment,
		min:       min,
		max:       max,
		cycle:     cycle,
		schema:    schema,
		seqName:   bare,
		dataType:  "bigint",
	}
	// current starts at start-increment so that the first nextval returns start.
	s.current.Store(start - increment)
	seqRegistry.Store(seqKey(name), s)
}

// SetSequenceDataType records the declared data type of a sequence (e.g. from
// CREATE SEQUENCE ... AS smallint). M0097-0068.
func SetSequenceDataType(name, dataType string) {
	v, ok := seqRegistry.Load(seqKey(name))
	if !ok {
		return
	}
	s := v.(*seqState)
	s.mu.Lock()
	s.dataType = strings.ToLower(dataType)
	s.mu.Unlock()
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
func LookupSequence(name string) *seqState {
	v, ok := seqRegistry.Load(seqKey(name))
	if !ok {
		return nil
	}
	return v.(*seqState)
}

// DropSequence removes a sequence from the registry. M0097-0038.
func DropSequence(name string) bool {
	_, loaded := seqRegistry.LoadAndDelete(seqKey(name))
	return loaded
}

// SetSequenceOwnedBy records that the sequence with the given name is owned by
// "table.column" (as produced by ALTER SEQUENCE ... OWNED BY). Pass "" to clear.
func SetSequenceOwnedBy(name, owner string) {
	v, ok := seqRegistry.Load(seqKey(name))
	if !ok {
		return
	}
	s := v.(*seqState)
	s.mu.Lock()
	s.ownedBy = strings.ToLower(owner)
	s.mu.Unlock()
}

// DropSequencesOwnedByTable drops all sequences whose ownedBy field starts with
// "tableName." (case-insensitive). Called by DROP TABLE to cascade-drop owned sequences.
func DropSequencesOwnedByTable(tableName string) {
	prefix := strings.ToLower(tableName) + "."
	seqRegistry.Range(func(k, v any) bool {
		s := v.(*seqState)
		s.mu.Lock()
		owned := s.ownedBy
		s.mu.Unlock()
		if strings.HasPrefix(owned, prefix) {
			seqRegistry.Delete(k)
		}
		return true
	})
}

// ResetSequence resets a sequence to its start value (equivalent to TRUNCATE ... RESTART IDENTITY).
// Returns false if the sequence does not exist.
func ResetSequence(name string) bool {
	v, ok := seqRegistry.Load(seqKey(name))
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
func GetSequenceCurrentValue(name string) (int64, bool) {
	v, ok := seqRegistry.Load(seqKey(name))
	if !ok {
		return 0, false
	}
	return v.(*seqState).current.Load(), true
}

// SetSequenceCurrentValue directly sets the internal counter (used for ROLLBACK of RESTART IDENTITY).
func SetSequenceCurrentValue(name string, val int64) bool {
	v, ok := seqRegistry.Load(seqKey(name))
	if !ok {
		return false
	}
	v.(*seqState).current.Store(val)
	return true
}


// evalNextval implements nextval(sequence_name text) → int8.
// Advances the sequence and returns the new value. Also stores the
// value in ctx.LastSeqVal and ctx.CurrSeqVals for currval/lastval.
func evalNextval(args []Datum, ctx *Context) (Datum, error) {
	if len(args) == 0 {
		return NullDatum, nil
	}
	if args[0].IsNull() {
		return NullDatum, nil
	}
	name := args[0].StringValue()
	s := LookupSequence(name)
	if s == nil {
		return Datum{}, &ExecError{Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", name)}
	}
	v, err := s.nextVal()
	if err != nil {
		return Datum{}, &ExecError{Code: "2200H", Message: err.Error()}
	}
	// Store for currval / lastval.
	if ctx != nil {
		if ctx.CurrSeqVals == nil {
			ctx.CurrSeqVals = make(map[string]int64)
		}
		ctx.CurrSeqVals[seqKey(name)] = v
		ctx.LastSeqVal = v
		ctx.LastSeqSet = true
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
	name := args[0].StringValue()
	if ctx == nil || ctx.CurrSeqVals == nil {
		return Datum{}, &ExecError{Code: "55000", Message: fmt.Sprintf("currval of sequence %q is not yet defined in this session", name)}
	}
	v, ok := ctx.CurrSeqVals[seqKey(name)]
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
	name := args[0].StringValue()
	value := args[1].Int
	isCalled := true
	if len(args) >= 3 && !args[2].IsNull() && args[2].Kind == KindBool {
		isCalled = args[2].BoolValue()
	}

	s := LookupSequence(name)
	if s == nil {
		RegisterSequence(name, 1, 1, 1, 9223372036854775807, false)
		s = LookupSequence(name)
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
	// Update session state.
	if ctx != nil {
		if ctx.CurrSeqVals == nil {
			ctx.CurrSeqVals = make(map[string]int64)
		}
		ctx.CurrSeqVals[seqKey(name)] = value
		ctx.LastSeqVal = value
		ctx.LastSeqSet = true
	}
	return Datum{Kind: KindInt, Int: value}, nil
}

// evalLastval implements lastval() → int8.
// Returns the most recent value returned by nextval in the current session.
func evalLastval(ctx *Context) (Datum, error) {
	if ctx == nil || !ctx.LastSeqSet {
		return Datum{}, &ExecError{Code: "55000", Message: "lastval is not yet defined in this session"}
	}
	return Datum{Kind: KindInt, Int: ctx.LastSeqVal}, nil
}

// UpdateSequenceParams applies ALTER SEQUENCE parameter changes to a sequence.
// All pointer fields: nil means "leave unchanged". restart=true resets current
// to start-increment (honoring newStart if also supplied). restartWith, if not
// nil, overrides the restart target. M0097-0068.
func UpdateSequenceParams(name string, increment, minVal, maxVal, startWith, restartWith *int64,
	restart, cycle, noCycle bool) error {
	s := LookupSequence(name)
	if s == nil {
		return fmt.Errorf("sequence %q does not exist", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if increment != nil {
		s.increment = *increment
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
func AllSequenceInfos() []SeqInfo {
	var out []SeqInfo
	seqRegistry.Range(func(_, v any) bool {
		s := v.(*seqState)
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
