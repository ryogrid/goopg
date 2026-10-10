package misc

import (
	"fmt"
	"strings"
	"sync"
)

// mapOldGUCNames is guc.c's map_old_guc_names: obsolete spellings that
// find_option still accepts, converted to the current name.
var mapOldGUCNames = map[string]string{
	"sort_mem":       "work_mem",
	"vacuum_mem":     "maintenance_work_mem",
	"ssl_ecdh_curve": "ssl_groups",
}

// pgGUC is one built-in PostgreSQL parameter: its own spelling and
// context (pg_gucs_gen.go).
type pgGUC struct {
	Name    string
	Context Context
}

var (
	builtinRegOnce sync.Once
	builtinReg     *Registry
)

// builtinRegistry is a process-wide registry of the built-in variable
// definitions at their boot values, for validating values that are stored
// rather than applied. Never mutated after construction.
func builtinRegistry() *Registry {
	builtinRegOnce.Do(func() { builtinReg = BuildDefaultRegistry() })
	return builtinReg
}

// ConfigArrayItem is the front half of GUCArrayAdd / GUCArrayDelete (guc.c),
// used where a setting is stored for later rather than applied —
// pg_proc.proconfig (CREATE / ALTER FUNCTION ... SET / RESET) and
// pg_db_role_setting.setconfig (ALTER DATABASE / ROLE ... SET / RESET):
//
//   - validate_option_array_item: an unknown name must be a valid custom
//     name (42704 otherwise); a known variable must be settable by SET at
//     this point — set_config_option's context checks, 55P02 — and, when
//     value is non-nil, value must be valid for it;
//   - the name is then normalised (find_option: map_old_guc_names and the
//     variable's own spelling), so `SET datestyle` is stored as DateStyle.
//
// It returns the name to store. A custom name keeps the given spelling.
// Errors are *AlterSystemError, carrying PostgreSQL's SQLSTATE. The caller
// is responsible for the superuser / parameter-ACL check.
func ConfigArrayItem(name string, value *string) (string, error) {
	r := builtinRegistry()
	lname := strings.ToLower(name)
	if mapped, ok := mapOldGUCNames[lname]; ok {
		lname = mapped
	}
	v, ok := r.Get(lname)
	pg, isPG := pgGUCs[lname]
	if !ok && !isPG {
		if !IsCustomGUCName(name) {
			return "", &AlterSystemError{Code: "42704", Msg: fmt.Sprintf("unrecognized configuration parameter %q", lname)}
		}
		return name, nil
	}
	// PostgreSQL's own definition decides the context and spelling; goopg's
	// registration (which can diverge, or be missing) only supplies the
	// value check below.
	context, canon := pg.Context, pg.Name
	if !isPG {
		context, canon = v.Context, v.Name
	}
	switch context {
	case ContextInternal:
		return "", &AlterSystemError{Code: "55P02", Msg: fmt.Sprintf("parameter %q cannot be changed", lname)}
	case ContextPostmaster:
		return "", &AlterSystemError{Code: "55P02", Msg: fmt.Sprintf("parameter %q cannot be changed without restarting the server", lname)}
	case ContextSigHup:
		return "", &AlterSystemError{Code: "55P02", Msg: fmt.Sprintf("parameter %q cannot be changed now", lname)}
	case ContextSuBackend, ContextBackend:
		return "", &AlterSystemError{Code: "55P02", Msg: fmt.Sprintf("parameter %q cannot be set after connection start", lname)}
	}
	if value != nil && ok {
		// A PostgreSQL parameter goopg does not implement is accepted by
		// name and context alone; there is no definition to check the
		// value against.
		if _, err := v.canonicalize(*value); err != nil {
			if verr, ok := err.(*ValidationError); ok {
				return "", &AlterSystemError{Code: "22023", Msg: verr.Msg, Hint: verr.Hint}
			}
			return "", &AlterSystemError{Code: "22023", Msg: err.Error()}
		}
	}
	return canon, nil
}
