package misc

import "strings"

// gucSaveLevel is one GUC nest level opened by a routine that carries SET
// clauses (pg_proc.proconfig). fmgr_security_definer opens it with
// NewGUCNestLevel, applies the clauses with ProcessGUCArray(...,
// GUC_ACTION_SAVE) and closes it with AtEOXact_GUC(true, save_nestlevel).
//
// The clause values live in the local layer, which shadows the session
// layer, so leaving the level only has to put the local layer back. The
// GucStack states guc.c tracks per saved variable reduce to one bit here:
//
//   - GUC_SAVE (untouched, or SET LOCAL inside the routine): the prior value
//     comes back when the level closes.
//   - GUC_SET (a plain SET or RESET inside the routine): the new value
//     outlives the routine. AtEOXact_GUC merges a GUC_SET entry into the
//     enclosing level's entry and makes that one GUC_SET too, so every open
//     level that saved the variable stops restoring it (overridden).
//
// Variables the routine sets without a SET clause need nothing here: their
// entries at the routine's level merge into the enclosing level on exit, so
// SET and SET LOCAL behave as if the routine boundary were not there.
type gucSaveLevel struct {
	keys       []string           // saved keys (lower-cased), in apply order
	prior      map[string]*string // local-layer value before the level; nil = none
	overridden map[string]bool    // a plain SET / RESET replaced the saved value
	reported   map[string]bool    // a ParameterStatus went out for the key meanwhile
}

// PushSaveLevel opens a GUC nest level and applies each "name=value" entry
// of a routine's proconfig with GUC_ACTION_SAVE semantics. On success the
// caller must PopSaveLevel(level) when the routine returns, on every path.
// On error the level is already closed (everything applied so far is
// restored) and the error is the first entry's validation failure.
//
// Applying a clause sends no ParameterStatus: PostgreSQL reports GUC_REPORT
// variables only when the value at the end of the query differs from the one
// last reported, and a saved value is always restored by then.
func (s *SessionRegistry) PushSaveLevel(config []string) (level int, err error) {
	lvl := &gucSaveLevel{
		prior:      map[string]*string{},
		overridden: map[string]bool{},
		reported:   map[string]bool{},
	}
	s.saveLevels = append(s.saveLevels, lvl)
	level = len(s.saveLevels)
	for _, item := range config {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			// ProcessGUCArray: "could not parse setting for parameter" —
			// proconfig entries are always name=value, so only a corrupted
			// catalog row reaches this.
			continue
		}
		v, placeholder, canon, perr := s.prepareSet(name, value)
		if perr != nil {
			s.PopSaveLevel(level)
			return 0, perr
		}
		if placeholder {
			s.custom[strings.ToLower(name)] = v
		}
		key := strings.ToLower(name)
		if _, saved := lvl.prior[key]; !saved {
			lvl.keys = append(lvl.keys, key)
			if old, had := s.local[key]; had {
				o := old
				lvl.prior[key] = &o
			} else {
				lvl.prior[key] = nil
			}
		}
		s.local[key] = canon
		_, eff, _ := s.Get(name)
		s.global.invokeOnChange(v.Name, eff)
	}
	return level, nil
}

// PopSaveLevel closes the nest level PushSaveLevel returned, and any level
// opened inside it that is still open (AtEOXact_GUC pops every stack entry
// at or above the level). Every saved variable not overridden by a plain
// SET goes back to its value from before the level.
func (s *SessionRegistry) PopSaveLevel(level int) {
	if level <= 0 {
		return
	}
	for len(s.saveLevels) >= level {
		lvl := s.saveLevels[len(s.saveLevels)-1]
		s.saveLevels = s.saveLevels[:len(s.saveLevels)-1]
		for i := len(lvl.keys) - 1; i >= 0; i-- {
			key := lvl.keys[i]
			if lvl.overridden[key] {
				continue
			}
			v, ok := s.lookupVariable(key)
			if !ok {
				continue
			}
			if p := lvl.prior[key]; p != nil {
				s.local[key] = *p
			} else {
				delete(s.local, key)
			}
			_, eff, _ := s.Get(key)
			// The client saw a value set inside the routine (SET LOCAL on
			// a saved GUC_REPORT variable), so it must see the restore.
			if lvl.reported[key] {
				s.report(v, key)
			}
			s.global.invokeOnChange(v.Name, eff)
		}
	}
}

// markOverridden records a plain SET / RESET of key: every open nest level
// that saved key keeps the new value instead of restoring its own prior
// (GUC_SET merging down through the levels).
func (s *SessionRegistry) markOverridden(key string) {
	for _, lvl := range s.saveLevels {
		if _, saved := lvl.prior[key]; saved {
			lvl.overridden[key] = true
		}
	}
}

// report sends the ParameterStatus for a GUC_REPORT variable's current
// effective value, and notes it in the open nest levels so that restoring a
// saved value sends one too.
func (s *SessionRegistry) report(v *Variable, name string) {
	if v.Flags&FlagReport == 0 || s.onReportableChange == nil {
		return
	}
	key := strings.ToLower(name)
	for _, lvl := range s.saveLevels {
		lvl.reported[key] = true
	}
	_, eff, _ := s.Get(name)
	s.onReportableChange(v.Name, eff)
}
