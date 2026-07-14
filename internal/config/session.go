package config

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// sessionIDCounter mints a process-unique, monotonically increasing identity
// for each SessionRegistry. Used as a stable per-session token (e.g. to record
// which session owns a temporary relation — see UniqueID / TempOwner). The
// zero value is never handed out so callers can treat 0 as "no session".
var sessionIDCounter atomic.Uint64

// SessionRegistry layers SET (session-scoped) and SET LOCAL
// (transaction-scoped) overrides on top of a global Registry.
//
// Lookup precedence: transaction layer → session layer → global.
// Writes go to whichever layer the caller asks for.
//
// Not goroutine-safe — one SessionRegistry per connection-handler
// goroutine.
type SessionRegistry struct {
	global  *Registry
	id      uint64            // stable, process-unique per-session identity (see UniqueID)
	session map[string]string // session-scoped values, canonicalised
	local   map[string]string // transaction-scoped values, canonicalised
	custom  map[string]*Variable
	inTx    bool

	// onReportableChange, if non-nil, is called whenever a Report-flagged
	// variable's effective value changes. The caller (the server) wires
	// this to a ParameterStatus emitter so libpq clients see auto-tracked
	// settings update mid-session.
	onReportableChange func(name, value string)
}

// NewSessionRegistry constructs a fresh SessionRegistry over global.
func NewSessionRegistry(global *Registry) *SessionRegistry {
	return &SessionRegistry{
		global:  global,
		id:      sessionIDCounter.Add(1),
		session: map[string]string{},
		local:   map[string]string{},
		custom:  map[string]*Variable{},
	}
}

// UniqueID returns this session's stable, process-unique identity. It is fixed
// for the SessionRegistry's lifetime (one per connection) and is used as the
// owning-session token for temporary relations so that inheritance expansion
// can exclude another backend's temp children (PostgreSQL's RELATION_IS_OTHER_TEMP
// rule). Always non-zero. Design 0118-0036.
func (s *SessionRegistry) UniqueID() uint64 {
	if s == nil {
		return 0
	}
	return s.id
}

// SetReportableHook installs the ParameterStatus callback. Pass nil to
// disable.
func (s *SessionRegistry) SetReportableHook(fn func(name, value string)) {
	s.onReportableChange = fn
}

// Get returns the (variable, effective-value, ok) triple. The variable
// pointer is the global registry entry — callers should not mutate it.
func (s *SessionRegistry) Get(name string) (*Variable, string, bool) {
	v, ok := s.lookupVariable(name)
	if !ok {
		return nil, "", false
	}
	key := strings.ToLower(name)
	if val, ok := s.local[key]; ok {
		return v, val, true
	}
	if val, ok := s.session[key]; ok {
		return v, val, true
	}
	return v, v.Value, true
}

// GetDisplay is like Get but returns the client-visible display form (e.g.
// "77MB" rather than "78848") for unit-flagged GUCs — the pair backs SHOW,
// current_setting(), and ALTER DATABASE/ROLE ... SET ... FROM CURRENT, all of
// which mirror upstream's GetConfigOptionByName(name, NULL, false) ->
// ShowGUCOption(record, use_units=true). Internal callers that need the raw
// canonical value for their own arithmetic (e.g. deadlock_timeout parsing a
// bare millisecond integer) must keep using Get, not this method.
func (s *SessionRegistry) GetDisplay(name string) (*Variable, string, bool) {
	v, eff, ok := s.Get(name)
	if !ok {
		return v, eff, ok
	}
	return v, v.FormatDisplayValue(eff), true
}

// AllDisplay is like All but formats each value via GetDisplay — the SHOW
// ALL / pg_settings-style counterpart to GetDisplay.
func (s *SessionRegistry) AllDisplay() []ReportableValue {
	all := s.global.All()
	out := make([]ReportableValue, 0, len(all))
	for _, v := range all {
		_, eff, _ := s.GetDisplay(v.Name)
		out = append(out, ReportableValue{Name: v.Name, Value: eff})
	}
	return out
}

// Set applies SET name = value (or SET LOCAL ...). Returns an error if
// the variable doesn't exist, the value fails validation, or the
// variable's Context forbids SQL-driven changes.
func (s *SessionRegistry) Set(name, value string, isLocal bool) error {
	// SET x TO DEFAULT / SET x = DEFAULT resets to the boot value.
	// Treat "DEFAULT" (case-insensitive) the same as RESET.
	if strings.EqualFold(value, "DEFAULT") {
		return s.Reset(name)
	}
	v, ok := s.lookupVariable(name)
	if !ok {
		if !IsCustomGUCName(name) {
			return fmt.Errorf("unrecognized configuration parameter %q", name)
		}
		v = NewVariable(Variable{
			Name:    name,
			Type:    TypeString,
			BootVal: "",
			Context: ContextUserset,
			Scope:   ScopeSession | ScopeTransaction,
			Flags:   FlagCustom | FlagDisallowInFile | FlagNotInSample,
		})
		s.custom[strings.ToLower(name)] = v
	}
	if v.Context < ContextSuset {
		// Postmaster / SigHup / Internal contexts cannot be SET.
		return fmt.Errorf("parameter %q cannot be changed now", v.Name)
	}
	_, current, _ := s.Get(name)
	canon, err := v.canonicalizeFrom(current, value)
	if err != nil {
		return err
	}
	key := strings.ToLower(name)
	if isLocal {
		if !s.inTx {
			// Postgres allows SET LOCAL outside a transaction; the
			// value is set for the duration of the implicit
			// transaction (which ends at the next ReadyForQuery). We
			// model that by storing it in the local layer and then
			// dropping it on the next ResetTransaction call.
		}
		s.local[key] = canon
	} else {
		s.session[key] = canon
		// SET (not SET LOCAL) clears any local override too — matches
		// upstream which treats SET as "session value plus implicit
		// reset of any LOCAL".
		delete(s.local, key)
	}
	if v.Flags&FlagReport != 0 && s.onReportableChange != nil {
		_, eff, _ := s.Get(name)
		s.onReportableChange(v.Name, eff)
	}
	// M0054-0006e-followup: process-global change callback. Fires
	// AFTER the session/local layer is updated so callbacks see the
	// post-Set effective value via .Get().
	_, eff, _ := s.Get(name)
	s.global.invokeOnChange(v.Name, eff)
	return nil
}

// SetInternal writes a ContextInternal variable's session-layer value
// (e.g. "is_superuser") — the class of GUC_REPORT variable upstream
// only ever assigns from backend C code, never from a client SET
// command (real PG's guc_tables.c marks these PGC_INTERNAL, and
// set_config_option() rejects any SET reaching them the same way
// Set() rejects Context < ContextSuset here). Callers must be trusted
// backend code (connection startup, SET ROLE / SET SESSION
// AUTHORIZATION role-tracking) — never plumb client input to this
// method directly.
func (s *SessionRegistry) SetInternal(name, value string) error {
	v, ok := s.lookupVariable(name)
	if !ok {
		return fmt.Errorf("unrecognized configuration parameter %q", name)
	}
	_, current, _ := s.Get(name)
	canon, err := v.canonicalizeFrom(current, value)
	if err != nil {
		return err
	}
	key := strings.ToLower(name)
	s.session[key] = canon
	delete(s.local, key)
	if v.Flags&FlagReport != 0 && s.onReportableChange != nil {
		_, eff, _ := s.Get(name)
		s.onReportableChange(v.Name, eff)
	}
	_, eff, _ := s.Get(name)
	s.global.invokeOnChange(v.Name, eff)
	return nil
}

// Reset drops session and transaction overrides for the named variable
// so it falls through to the global value.
func (s *SessionRegistry) Reset(name string) error {
	v, ok := s.lookupVariable(name)
	if !ok {
		return fmt.Errorf("unrecognized configuration parameter %q", name)
	}
	key := strings.ToLower(name)
	delete(s.session, key)
	delete(s.local, key)
	if v.Flags&FlagReport != 0 && s.onReportableChange != nil {
		s.onReportableChange(v.Name, v.Value)
	}
	// M0054-0006e-followup: invoke the process-global change
	// callback for the post-Reset value (which is the global default).
	s.global.invokeOnChange(v.Name, v.Value)
	return nil
}

func (s *SessionRegistry) lookupVariable(name string) (*Variable, bool) {
	if s == nil {
		return nil, false
	}
	if v, ok := s.global.Get(name); ok {
		return v, true
	}
	v, ok := s.custom[strings.ToLower(name)]
	return v, ok
}

// IsCustomGUCName reports whether name has the syntactic shape PostgreSQL
// accepts for a custom/extension GUC (two or more dot-separated simple
// identifiers, e.g. "plpgsql.check_asserts") — mirrors guc.c's
// valid_custom_variable_name (minus the loaded-extension reserved-prefix
// check, which goopg does not track). Used both by SET on an unregistered
// name and by GRANT/REVOKE ... ON PARAMETER's name validation
// (check_GUC_name_for_parameter_acl).
func IsCustomGUCName(name string) bool {
	return isCustomGUCName(name)
}

func isCustomGUCName(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			ch := part[i]
			switch {
			case ch >= 'a' && ch <= 'z':
			case ch >= 'A' && ch <= 'Z':
			case ch == '_':
			case i > 0 && ch >= '0' && ch <= '9':
			default:
				return false
			}
		}
	}
	return true
}

// ResetAll resets every session-scoped and transaction-scoped override.
// SET TO DEFAULT and RESET ALL both route here.
func (s *SessionRegistry) ResetAll() {
	for name := range s.session {
		_ = s.Reset(name)
	}
	for name := range s.local {
		_ = s.Reset(name)
	}
}

// BeginTransaction marks the start of a transaction. SET LOCAL inside
// a transaction is dropped on Commit/Rollback.
func (s *SessionRegistry) BeginTransaction() { s.inTx = true }

// EndTransaction discards the local layer and flips us out of the
// in-transaction flag. Call from both COMMIT and ROLLBACK.
func (s *SessionRegistry) EndTransaction() {
	for name := range s.local {
		v, _ := s.global.Get(name)
		delete(s.local, name)
		if v != nil && v.Flags&FlagReport != 0 && s.onReportableChange != nil {
			_, eff, _ := s.Get(name)
			s.onReportableChange(v.Name, eff)
		}
	}
	s.inTx = false
}

// ReportableVariables returns every variable in the global registry
// that has FlagReport set, in sorted order, with each variable's
// *effective* (session-layered) value. Used to seed the startup
// ParameterStatus block.
func (s *SessionRegistry) ReportableVariables() []ReportableValue {
	all := s.global.All()
	var out []ReportableValue
	for _, v := range all {
		if v.Flags&FlagReport == 0 {
			continue
		}
		_, eff, _ := s.Get(v.Name)
		out = append(out, ReportableValue{Name: v.Name, Value: eff})
	}
	return out
}

// ReportableValue is one (name, value) tuple destined for a
// ParameterStatus message.
type ReportableValue struct {
	Name  string
	Value string
}

// ExplainVariables returns every FlagExplain variable whose *effective*
// (session-layered) value differs from its built-in BootVal — the
// EXPLAIN (SETTINGS) list. Mirrors upstream's get_explain_guc_options
// (guc.c): only GUCs flagged GUC_EXPLAIN, and only when modified from
// the compiled-in default (not merely from PGC_S_DEFAULT source).
// Sorted by name (global.All()'s order) for deterministic output;
// upstream's guc_nondef_list order reflects modification history
// instead, which this codebase does not track.
//
// BootVal is stored as the raw author-facing literal (e.g. "512MB"),
// while Value/eff are always canonicalized (e.g. "524288") — comparing
// eff directly against BootVal would flag nearly every unit-bearing GUC
// as "modified" even at boot, so BootVal is canonicalized the same way
// NewVariable canonicalizes the initial Value before the comparison.
func (s *SessionRegistry) ExplainVariables() []ReportableValue {
	all := s.global.All()
	var out []ReportableValue
	for _, v := range all {
		if v.Flags&FlagExplain == 0 {
			continue
		}
		bootCanon, err := v.canonicalize(v.BootVal)
		if err != nil {
			continue
		}
		_, eff, _ := s.Get(v.Name)
		if eff == bootCanon {
			continue
		}
		out = append(out, ReportableValue{Name: v.Name, Value: eff})
	}
	return out
}

// All returns every variable plus its effective value, for SHOW ALL.
func (s *SessionRegistry) All() []ReportableValue {
	all := s.global.All()
	out := make([]ReportableValue, 0, len(all))
	for _, v := range all {
		_, eff, _ := s.Get(v.Name)
		out = append(out, ReportableValue{Name: v.Name, Value: eff})
	}
	return out
}
