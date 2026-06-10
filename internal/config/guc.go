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
)

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
// store. Returns (canonical, error).
func (v *Variable) canonicalize(value string) (string, error) {
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
