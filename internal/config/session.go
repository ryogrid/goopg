package config

import (
	"fmt"
	"strings"
)

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
		session: map[string]string{},
		local:   map[string]string{},
		custom:  map[string]*Variable{},
	}
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
		if !isCustomGUCName(name) {
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
	canon, err := v.canonicalize(value)
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
