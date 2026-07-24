package config

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Type is the value type of a GUC. Mirrors guc.c's PGC_BOOL / PGC_INT /
// PGC_REAL / PGC_STRING / PGC_ENUM.
type Type int

const (
	TypeBool Type = iota
	TypeInt
	TypeReal
	TypeString
	TypeEnum
)

// Context is "where this variable can be set". Mirrors GucContext in
// postgres/src/include/utils/guc.h:71-80. The constants are ordered
// from most-restricted to least-restricted; SET semantics check
// `Context >= Suset` (or Userset) for sql-driven changes.
type Context int

const (
	ContextInternal   Context = iota // never settable from SQL or config
	ContextPostmaster                // requires server restart
	ContextSigHup                    // requires reload
	ContextSuBackend                 // superuser-only at backend start
	ContextBackend                   // settable at backend start (e.g. via PGOPTIONS)
	ContextSuset                     // superuser-only via SET
	ContextUserset                   // anyone via SET
)

// Source records where the *current* value came from. Higher numbers
// override lower ones for the same variable, except where SET LOCAL and
// SET layer on top via SessionRegistry.
type Source int

const (
	SourceDefault Source = iota
	SourceEnvVar
	SourceConfigFile
	SourceCommandLine
	SourceOverride // CLI flag explicitly overrides the config file
	SourceSession  // SET inside a session
	SourceLocal    // SET LOCAL inside a transaction
)

// Scope answers "at which scope can this variable carry a value". The
// SessionRegistry layering handles the dynamic SET / SET LOCAL part;
// the Server / Database / Role layers are recorded but not yet wired
// in (no catalog).
type Scope int

const (
	ScopeServer Scope = 1 << iota
	ScopeDatabase
	ScopeRole
	ScopeSession
	ScopeTransaction
)

// Unit is the value-unit suffix the parser honours. Numeric units ("8MB",
// "1500ms") are interpreted at Set() time according to the variable's
// declared unit.
type Unit int

const (
	UnitNone Unit = iota
	UnitBytes
	UnitKB
	UnitMB
	UnitGB
	UnitTB
	UnitMs
	UnitS
	UnitMin
	UnitH
	UnitD
	// UnitBlocks mirrors upstream's GUC_UNIT_BLOCKS: the value is stored
	// as a count of BLCKSZ-sized blocks. Used by the min_parallel_*_scan_size
	// GUCs, which upstream declares in blocks and displays in kB/MB.
	// There is deliberately no "block" suffix in unitFromSuffix — upstream
	// accepts only byte suffixes on input for these GUCs too.
	UnitBlocks
)

// blockSize is BLCKSZ, the on-disk page size, in bytes. It is the conversion
// factor for UnitBlocks. Kept local to the config package because the GUC
// layer must not import storage.
const blockSize = 8192

// VarFlag is a bitmask of behaviour modifiers. Mirrors
// postgres/src/include/utils/guc.h flags but only the ones we use.
type VarFlag uint32

const (
	// FlagReport: emit ParameterStatus when this variable changes.
	FlagReport VarFlag = 1 << iota
	// FlagDisallowInFile: cannot appear in postgresql.conf.
	FlagDisallowInFile
	// FlagNotInSample: omitted from the generated sample config.
	FlagNotInSample
	// FlagCustom: defined outside the seeded set (placeholder value).
	FlagCustom
	// FlagExplain: mirrors guc_tables.c's GUC_EXPLAIN — this variable
	// affects query planning and is eligible for EXPLAIN (SETTINGS)
	// once its effective value differs from BootVal. See
	// get_explain_guc_options / ExplainPrintSettings (explain.c).
	FlagExplain
)

// Variable is one GUC. Use NewVariable in BuildDefaultRegistry rather
// than instantiating this directly so validation is consistent.
type Variable struct {
	Name        string
	Type        Type
	Unit        Unit
	BootVal     string
	Value       string // current effective value, in canonical string form
	Source      Source
	Context     Context
	Scope       Scope
	Flags       VarFlag
	MinVal      float64 // valid for Int/Real
	MaxVal      float64
	EnumOptions []string // valid for Enum
}

// canonicalize normalises an incoming string value into the form we
// store, merging against v.Value as the current-value baseline. Returns
// (canonical, error). Correct for every call site except
// SessionRegistry.Set/SetInternal, where a session/transaction-scoped
// override can diverge from the global v.Value — those use
// canonicalizeFrom with the session's actual effective value instead.
func (v *Variable) canonicalize(value string) (string, error) {
	return v.canonicalizeFrom(v.Value, value)
}

// canonicalizeFrom is canonicalize with an explicit current-value
// baseline. DateStyle is the only GUC whose canonical form depends on it
// (a partial spec like "SET datestyle = 'SQL'" must keep the existing
// order component — see mergeDateStyle); every other type ignores current.
func (v *Variable) canonicalizeFrom(current, value string) (string, error) {
	if strings.EqualFold(v.Name, "DateStyle") {
		return mergeDateStyle(current, v.BootVal, value)
	}
	switch v.Type {
	case TypeBool:
		b, ok := parseBoolish(value)
		if !ok {
			return "", fmt.Errorf("invalid bool value %q", value)
		}
		if b {
			return "on", nil
		}
		return "off", nil
	case TypeInt:
		n, err := parseIntWithUnit(value, v.Unit)
		if err != nil {
			return "", err
		}
		if v.MinVal != 0 || v.MaxVal != 0 {
			if float64(n) < v.MinVal || float64(n) > v.MaxVal {
				return "", fmt.Errorf("value %d out of range [%g, %g]", n, v.MinVal, v.MaxVal)
			}
		}
		return strconv.FormatInt(n, 10), nil
	case TypeReal:
		// Strip leading/trailing whitespace and attempt to separate numeric
		// part from unit suffix (e.g. "900us" → 900, "us").
		trimmed := strings.TrimSpace(value)
		numEnd := 0
		for numEnd < len(trimmed) {
			c := trimmed[numEnd]
			if c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9') {
				numEnd++
			} else {
				break
			}
		}
		suffix := strings.TrimSpace(strings.ToLower(trimmed[numEnd:]))
		numStr := strings.TrimSpace(trimmed[:numEnd])
		if numStr == "" {
			// No numeric prefix (e.g. "NaN", "Inf") — treat entire string as value.
			numStr = trimmed
			suffix = ""
		}
		f, err := strconv.ParseFloat(numStr, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("invalid value for parameter %q: %q", v.Name, value)
		}
		// Convert unit suffix for time-unit GUCs (e.g. vacuum_cost_delay in ms).
		if suffix != "" && v.Unit != UnitNone {
			// Handle "us" (microseconds) specially: 1 us = 0.001 ms.
			if suffix == "us" {
				switch v.Unit {
				case UnitMs:
					f = f * 0.001
				default:
					return "", fmt.Errorf("invalid unit %q for parameter %q", suffix, v.Name)
				}
			} else {
				suffixUnit, ok := unitFromSuffix(suffix)
				if !ok {
					return "", fmt.Errorf("invalid unit %q for parameter %q", suffix, v.Name)
				}
				// Map suffix unit to ms multiplier.
				var fromMs float64
				switch suffixUnit {
				case UnitMs:
					fromMs = 1
				case UnitS:
					fromMs = 1000
				case UnitMin:
					fromMs = 60000
				case UnitH:
					fromMs = 3600000
				case UnitD:
					fromMs = 86400000
				default:
					return "", fmt.Errorf("unsupported unit suffix %q for real parameter", suffix)
				}
				// Convert to native unit.
				var toMs float64
				switch v.Unit {
				case UnitMs:
					toMs = 1
				case UnitS:
					toMs = 1000
				case UnitMin:
					toMs = 60000
				default:
					toMs = 1
				}
				f = f * fromMs / toMs
			}
		}
		if v.MinVal != 0 || v.MaxVal != 0 {
			if f < v.MinVal || f > v.MaxVal {
				// Produce range error similar to PostgreSQL format.
				nativeStr := func(x float64) string {
					switch v.Unit {
					case UnitMs:
						if x == float64(int64(x)) {
							return fmt.Sprintf("%g ms", x)
						}
						return fmt.Sprintf("%g ms", x)
					default:
						return fmt.Sprintf("%g", x)
					}
				}
				return "", fmt.Errorf("%s is outside the valid range for parameter %q (%s .. %s)",
					nativeStr(f), v.Name, nativeStr(v.MinVal), nativeStr(v.MaxVal))
			}
		}
		fStr := strconv.FormatFloat(f, 'g', -1, 64)
		// For time-unit GUCs, format the display value with units like PostgreSQL:
		// - Use "us" if the value has sub-ms precision or is < 1ms
		// - Use "ms" if the value is an exact integer number of ms
		if v.Unit == UnitMs && (f != 0) {
			usVal := f * 1000 // convert to microseconds
			if usVal == float64(int64(usVal)) && f < 1.0 {
				// Exact µs value, less than 1ms → show in µs
				fStr = fmt.Sprintf("%gus", usVal)
			} else if f == float64(int64(f)) {
				// Exact integer ms → show in ms
				fStr = fmt.Sprintf("%gms", f)
			} else {
				// Fractional ms → show in µs (e.g. 80.1ms → 80100us)
				usRounded := math.Round(f * 1000)
				fStr = fmt.Sprintf("%gus", usRounded)
			}
		}
		return fStr, nil
	case TypeString:
		return value, nil
	case TypeEnum:
		for _, opt := range v.EnumOptions {
			if strings.EqualFold(opt, value) {
				return opt, nil
			}
		}
		// Upstream registers boolean synonyms for on/off-bearing enums as
		// HIDDEN config_enum_entry rows (hidden = true) — see
		// debug_parallel_query_options in
		// postgres/src/backend/utils/misc/guc_tables.c:395-405, which lists
		// true/false/yes/no/1/0 with hidden=true. `SET
		// debug_parallel_query = true` is therefore accepted, while the
		// synonyms stay out of pg_settings.enumvals and out of the error
		// HINT. Reproduce that by falling back to the boolean parser only
		// when the enum actually offers both "on" and "off" — which keeps
		// enums like IntervalStyle unaffected — and by NOT adding the
		// synonyms to EnumOptions, so enumvals and the error text below
		// stay PG-shaped.
		//
		// One deliberate superset: parseBoolish also accepts "t"/"f", which
		// upstream's hidden list omits for this GUC (it does accept them for
		// real TypeBool GUCs). Accepting a strict superset of PG here is
		// harmless — no valid PG input is rejected — and keeps one boolean
		// parser rather than two.
		if enumHasBoolPair(v.EnumOptions) {
			if b, ok := parseBoolish(value); ok {
				if b {
					return "on", nil
				}
				return "off", nil
			}
		}
		return "", fmt.Errorf("invalid value %q for enum (valid: %s)",
			value, strings.Join(v.EnumOptions, ", "))
	}
	return value, nil
}

// Set assigns a new value and source. Returns an error if the new
// source is not allowed to override the existing one (e.g. SET
// against a Postmaster-context variable), or if the value fails
// validation.
func (v *Variable) Set(value string, source Source) error {
	// Source-based gating: SET / SET LOCAL only allowed when context
	// permits SQL-driven changes.
	if (source == SourceSession || source == SourceLocal) && v.Context < ContextSuset {
		return fmt.Errorf("parameter %q cannot be changed now", v.Name)
	}
	canon, err := v.canonicalize(value)
	if err != nil {
		return err
	}
	v.Value = canon
	v.Source = source
	return nil
}

// Reset returns the variable to its boot value and SourceDefault.
func (v *Variable) Reset() {
	v.Value = v.BootVal
	v.Source = SourceDefault
}

// Display returns the canonical wire-form value for SHOW /
// ParameterStatus. For booleans that's "on" or "off"; for integers
// with units we keep the canonical no-unit form (callers needing the
// human-readable "8MB" can format separately).
func (v *Variable) Display() string { return v.Value }

// unitConversion is one (suffix, base-units-per-suffix-unit) entry, mirroring
// a row of guc.c's memory_unit_conversion_table / time_unit_conversion_table
// (postgres/src/backend/utils/misc/guc.c) restricted to the rows for a single
// base unit, ordered greatest-to-smallest as upstream requires.
type unitConversion struct {
	suffix     string
	multiplier int64
}

// memoryDisplayUnits/timeDisplayUnits give, for each possible native storage
// Unit, the ordered (greatest-to-smallest, terminating at the base unit
// itself) list of display units convert_int_from_base_unit walks.
var memoryDisplayUnits = map[Unit][]unitConversion{
	UnitBytes: {{"TB", 1024 * 1024 * 1024 * 1024}, {"GB", 1024 * 1024 * 1024}, {"MB", 1024 * 1024}, {"kB", 1024}, {"B", 1}},
	UnitKB:    {{"TB", 1024 * 1024 * 1024}, {"GB", 1024 * 1024}, {"MB", 1024}, {"kB", 1}},
	UnitMB:    {{"TB", 1024 * 1024}, {"GB", 1024}, {"MB", 1}},
	UnitGB:    {{"TB", 1024}, {"GB", 1}},
	UnitTB:    {{"TB", 1}},
	// UnitBlocks mirrors upstream's GUC_UNIT_BLOCKS rows in
	// memory_unit_conversion_table. Note the kB row's multiplier is
	// NEGATIVE: a block (8 kB) is LARGER than the display unit, so
	// convert_int_from_base_unit multiplies instead of dividing. See the
	// negative-multiplier branch in FormatDisplayValue.
	//
	//	{"TB", (1024*1024*1024)/(BLCKSZ/1024)} == 134217728
	//	{"GB", (1024*1024)/(BLCKSZ/1024)}      ==    131072
	//	{"MB", (1024)/(BLCKSZ/1024)}           ==       128
	//	{"kB", -(BLCKSZ/1024)}                 ==        -8
	UnitBlocks: {
		{"TB", (1024 * 1024 * 1024) / (blockSize / 1024)},
		{"GB", (1024 * 1024) / (blockSize / 1024)},
		{"MB", 1024 / (blockSize / 1024)},
		{"kB", -(blockSize / 1024)},
	},
}

var timeDisplayUnits = map[Unit][]unitConversion{
	UnitMs:  {{"d", 86400000}, {"h", 3600000}, {"min", 60000}, {"s", 1000}, {"ms", 1}},
	UnitS:   {{"d", 86400}, {"h", 3600}, {"min", 60}, {"s", 1}},
	UnitMin: {{"d", 1440}, {"h", 60}, {"min", 1}},
	UnitH:   {{"d", 24}, {"h", 1}},
	UnitD:   {{"d", 1}},
}

// FormatDisplayValue renders raw — the variable's canonical bare-number
// string — the way a client-visible display renders it: with a
// human-friendly unit suffix chosen so it divides evenly, mirroring
// upstream's ShowGUCOption(record, use_units=true) ->
// convert_int_from_base_unit (postgres/src/backend/utils/misc/guc.c). Used
// by SHOW, current_setting(), and ALTER DATABASE/ROLE ... SET ... FROM
// CURRENT (all three route through GetConfigOptionByName(name, NULL, false)
// upstream, which always passes use_units=true).
//
// Only TypeInt variables with a declared Unit are reformatted; upstream also
// restricts unit conversion to result > 0, so 0 and negative values (e.g. the
// "disabled" sentinel used by statement_timeout/lock_timeout) are returned
// unchanged, matching real PG's bare "0" for SHOW statement_timeout.
func (v *Variable) FormatDisplayValue(raw string) string {
	if v.Type != TypeInt || v.Unit == UnitNone {
		return raw
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return raw
	}
	table, ok := memoryDisplayUnits[v.Unit]
	if !ok {
		table, ok = timeDisplayUnits[v.Unit]
	}
	if !ok {
		return raw
	}
	for _, u := range table {
		// Negative multiplier: the storage unit is LARGER than the display
		// unit, so upstream's convert_int_from_base_unit multiplies rather
		// than divides (and the division-evenness test does not apply — the
		// result is always exact). Only UnitBlocks' kB row has this today.
		// Without this branch a blocks-valued GUC whose value is smaller
		// than one MB (e.g. min_parallel_index_scan_size = 64 blocks) would
		// match no row and print as a bare number instead of "512kB".
		if u.multiplier < 0 {
			return strconv.FormatInt(n*(-u.multiplier), 10) + u.suffix
		}
		if u.multiplier <= 1 || n%u.multiplier == 0 {
			return strconv.FormatInt(n/u.multiplier, 10) + u.suffix
		}
	}
	return raw
}

// NewVariable constructs a Variable with its boot value installed.
// Panics if the boot value fails validation — boot values are author
// errors, not user errors.
func NewVariable(spec Variable) *Variable {
	v := &spec
	if v.Value == "" {
		v.Value = v.BootVal
	}
	canon, err := v.canonicalize(v.Value)
	if err != nil {
		panic(fmt.Sprintf("guc %q: invalid boot value %q: %v", v.Name, v.Value, err))
	}
	v.Value = canon
	if v.Source == 0 {
		v.Source = SourceDefault
	}
	return v
}

// Registry is the per-server GUC table. Not goroutine-safe — callers
// either set it up before launching the listener (the common case) or
// guard external access through a SessionRegistry.
type Registry struct {
	vars     map[string]*Variable
	onChange map[string]func(value string) // M0054-0006e-followup
}

// NewRegistry returns an empty registry. Most callers want
// BuildDefaultRegistry instead.
func NewRegistry() *Registry {
	return &Registry{
		vars:     map[string]*Variable{},
		onChange: map[string]func(value string){},
	}
}

// OnChange registers a process-global callback fired whenever a
// session successfully `SET`s the named variable (case-insensitive).
// The callback receives the canonicalised value AFTER the session
// layer is updated. Use cases: bridging SQL-level toggles to
// package-level atomic flags such as `planner.SetNLIEnabled`. Only
// one callback per name is supported; a second registration replaces
// the first. (M0054-0006e-followup.)
func (r *Registry) OnChange(name string, fn func(value string)) {
	if r.onChange == nil {
		r.onChange = map[string]func(value string){}
	}
	r.onChange[strings.ToLower(name)] = fn
}

// invokeOnChange calls the registered callback (if any) for `name`.
// Used by SessionRegistry.Set / Reset to bridge to package globals.
func (r *Registry) invokeOnChange(name, value string) {
	if r == nil || r.onChange == nil {
		return
	}
	if fn, ok := r.onChange[strings.ToLower(name)]; ok {
		fn(value)
	}
}

// Register adds or replaces a variable. Returns an error if a variable
// with the same name already exists with a different type — that is
// almost certainly a programming bug.
//
// The registry is keyed case-insensitively; v.Name is preserved as
// supplied so display paths (SHOW, ParameterStatus) emit the original
// spelling — `DateStyle` and `TimeZone` rather than the lowercased
// forms. Upstream uses the same convention.
func (r *Registry) Register(v *Variable) error {
	key := strings.ToLower(v.Name)
	if existing, ok := r.vars[key]; ok && existing.Type != v.Type {
		return fmt.Errorf("re-registering %q with different type", v.Name)
	}
	r.vars[key] = v
	return nil
}

// MustRegister panics on Register error; for use during init.
func (r *Registry) MustRegister(v *Variable) {
	if err := r.Register(v); err != nil {
		panic(err)
	}
}

// Get returns the variable with the given name (case-insensitive).
func (r *Registry) Get(name string) (*Variable, bool) {
	v, ok := r.vars[strings.ToLower(name)]
	return v, ok
}

// Set canonicalises and assigns a value to the named variable.
func (r *Registry) Set(name, value string, source Source) error {
	v, ok := r.Get(name)
	if !ok {
		return fmt.Errorf("unrecognized configuration parameter %q", name)
	}
	return v.Set(value, source)
}

// All returns every variable, sorted by name.
func (r *Registry) All() []*Variable {
	out := make([]*Variable, 0, len(r.vars))
	for _, v := range r.vars {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ApplyConfigEntries walks parsed entries in order and Sets each one
// with SourceConfigFile. Unknown entries return errors aggregated as
// strings (so a malformed file is a hard failure, not silent
// ignorance).
func (r *Registry) ApplyConfigEntries(entries []ConfigEntry) error {
	var errs []string
	for _, e := range entries {
		v, ok := r.Get(e.Name)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s:%d: unknown parameter %q", e.SourceFile, e.SourceLine, e.Name))
			continue
		}
		if err := setFromFile(v, e.Value); err != nil {
			errs = append(errs, fmt.Sprintf("%s:%d: %v", e.SourceFile, e.SourceLine, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// ReloadResult summarises one ApplyReloadEntries call for the
// caller's log line (`goopg reload` / SIGHUP).
type ReloadResult struct {
	// Changed lists the canonical names of variables whose effective
	// value actually changed.
	Changed []string
	// Warnings lists non-fatal problems: unknown parameters, values
	// that failed to canonicalise, and PGC_POSTMASTER/PGC_INTERNAL
	// entries that were present in the file but cannot take effect
	// without a restart. A reload never fails outright — matching
	// upstream ProcessConfigFile, which logs and keeps running.
	Warnings []string
}

// ApplyReloadEntries re-applies parsed config-file entries to a
// *running* server (the `goopg reload` / SIGHUP path), unlike
// ApplyConfigEntries which is for the initial boot-time load.
//
// Unlike boot, a reload must not clobber PGC_POSTMASTER variables
// (they require a restart per postgres/src/backend/utils/misc/guc.c's
// ProcessConfigFile) nor PGC_INTERNAL ones (never settable at all);
// both are reported as warnings and left untouched. Every other
// context is applied with SourceConfigFile, and — because the server
// is already live, unlike at boot — each successful change also fires
// the registry's OnChange bridge so process-global toggles (e.g.
// enable_nestloop_index) observe the new value immediately.
func (r *Registry) ApplyReloadEntries(entries []ConfigEntry) ReloadResult {
	var res ReloadResult
	for _, e := range entries {
		v, ok := r.Get(e.Name)
		if !ok {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s:%d: unrecognized configuration parameter %q", e.SourceFile, e.SourceLine, e.Name))
			continue
		}
		if v.Context == ContextInternal {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s:%d: parameter %q cannot be changed", e.SourceFile, e.SourceLine, v.Name))
			continue
		}
		if v.Context == ContextPostmaster {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s:%d: parameter %q cannot be changed without restarting the server", e.SourceFile, e.SourceLine, v.Name))
			continue
		}
		canon, err := v.canonicalize(e.Value)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s:%d: %v", e.SourceFile, e.SourceLine, err))
			continue
		}
		if canon != v.Value {
			v.Value = canon
			v.Source = SourceConfigFile
			r.invokeOnChange(v.Name, canon)
			res.Changed = append(res.Changed, v.Name)
		}
	}
	return res
}

// setFromFile bypasses Variable.Set's context gating — the config
// file is allowed to set Postmaster-context variables at startup, even
// though SET would not be.
func setFromFile(v *Variable, value string) error {
	canon, err := v.canonicalize(value)
	if err != nil {
		return err
	}
	v.Value = canon
	v.Source = SourceConfigFile
	return nil
}

// parseBoolish recognises every spelling upstream guc.c:parse_bool
// accepts.
// enumHasBoolPair reports whether opts contains both "on" and "off", which is
// how this package recognises an enum for which upstream also accepts the
// hidden boolean synonyms (true/false/yes/no/1/0). See the TypeEnum arm of
// canonicalizeFrom.
func enumHasBoolPair(opts []string) bool {
	var on, off bool
	for _, o := range opts {
		switch strings.ToLower(o) {
		case "on":
			on = true
		case "off":
			off = true
		}
	}
	return on && off
}

func parseBoolish(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1", "t":
		return true, true
	case "off", "false", "no", "0", "f":
		return false, true
	}
	return false, false
}

// parseIntWithUnit honours unit suffixes ("8MB", "1500ms") and
// converts the result to the variable's native unit.
func parseIntWithUnit(s string, native Unit) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty integer value")
	}
	// Find where the digits end.
	end := 0
	for end < len(s) && (s[end] == '+' || s[end] == '-' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("invalid integer value %q", s)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s[:end]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer value %q: %w", s, err)
	}
	suffix := strings.TrimSpace(strings.ToLower(s[end:]))
	if suffix == "" {
		return n, nil
	}
	suffixUnit, ok := unitFromSuffix(suffix)
	if !ok {
		return 0, fmt.Errorf("unrecognised unit suffix %q", suffix)
	}
	if native == UnitNone {
		return 0, fmt.Errorf("variable does not accept unit %q", suffix)
	}
	return convertUnit(n, suffixUnit, native)
}

func unitFromSuffix(s string) (Unit, bool) {
	switch s {
	case "b":
		return UnitBytes, true
	case "kb":
		return UnitKB, true
	case "mb":
		return UnitMB, true
	case "gb":
		return UnitGB, true
	case "tb":
		return UnitTB, true
	case "us", "ms":
		return UnitMs, true
	case "s":
		return UnitS, true
	case "min":
		return UnitMin, true
	case "h":
		return UnitH, true
	case "d":
		return UnitD, true
	}
	return UnitNone, false
}

// convertUnit converts a value from `from` to `to`. Only conversions
// within a unit family (bytes ↔ KB ↔ MB ↔ GB ↔ TB or ms ↔ s ↔ min ↔ h ↔ d)
// are valid; cross-family conversions (e.g. KB → s) are rejected.
func convertUnit(n int64, from, to Unit) (int64, error) {
	bytesFamily := func(u Unit) (int64, bool) {
		switch u {
		case UnitBytes:
			return 1, true
		case UnitKB:
			return 1024, true
		case UnitMB:
			return 1024 * 1024, true
		case UnitGB:
			return 1024 * 1024 * 1024, true
		case UnitTB:
			return 1024 * 1024 * 1024 * 1024, true
		case UnitBlocks:
			// A block is BLCKSZ bytes, so blocks sit in the byte family
			// with a multiplier of 8192. `SET x = '8MB'` on a blocks-valued
			// GUC therefore stores 1024.
			return blockSize, true
		}
		return 0, false
	}
	timeFamily := func(u Unit) (int64, bool) {
		switch u {
		case UnitMs:
			return 1, true
		case UnitS:
			return 1000, true
		case UnitMin:
			return 60 * 1000, true
		case UnitH:
			return 60 * 60 * 1000, true
		case UnitD:
			return 24 * 60 * 60 * 1000, true
		}
		return 0, false
	}
	if fb, ok := bytesFamily(from); ok {
		tb, ok := bytesFamily(to)
		if !ok {
			return 0, fmt.Errorf("cannot convert byte unit to time unit")
		}
		return n * fb / tb, nil
	}
	if ft, ok := timeFamily(from); ok {
		tt, ok := timeFamily(to)
		if !ok {
			return 0, fmt.Errorf("cannot convert time unit to byte unit")
		}
		return n * ft / tt, nil
	}
	return 0, fmt.Errorf("unconvertible unit pair (%v -> %v)", from, to)
}
