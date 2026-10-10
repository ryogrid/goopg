package misc

import (
	"fmt"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/parser/sqlkeywords"
)

// SetArgKind is the node kind of one SET value element — the A_Const value
// tag gram.y's var_list builds (T_Integer, T_Float, T_String).
type SetArgKind uint8

const (
	// SetArgInteger is an integer literal that fits int32 (ICONST). Val is
	// its decimal rendering, which is what flattening prints (%d).
	SetArgInteger SetArgKind = iota
	// SetArgFloat is any other numeric literal (FCONST, including integers
	// too large for int32). Val is the text as written, sign included.
	SetArgFloat
	// SetArgString is a string literal or an identifier (downcased unless it
	// was double-quoted) — gram.y represents both as a String const, and
	// flattening treats them alike.
	SetArgString
)

// SetArg is one element of a SET / ALTER SYSTEM / ALTER ROLE|DATABASE SET /
// function SET clause value list, as parsed (before flattening).
type SetArg struct {
	Kind SetArgKind
	Val  string
}

var (
	builtinFlagsOnce sync.Once
	builtinFlags     map[string]VarFlag
)

// builtinVarFlags returns the flags of a built-in variable. Variable
// definitions are process-wide and fixed, so the table is built once from
// the default registry; ok is false for an unknown or custom name, which
// flatten_set_variable_args treats as flags 0.
func builtinVarFlags(name string) (VarFlag, bool) {
	builtinFlagsOnce.Do(func() {
		r := BuildDefaultRegistry()
		builtinFlags = make(map[string]VarFlag)
		for _, v := range r.vars {
			builtinFlags[strings.ToLower(v.Name)] = v.Flags
		}
	})
	f, ok := builtinFlags[strings.ToLower(name)]
	return f, ok
}

// FlattenSetArgs is guc_funcs.c's flatten_set_variable_args: it turns the
// parsed value list of a SET-style clause into the single string handed to
// the variable. Elements are joined with ", "; integers print as integers,
// other numbers as written, and strings/identifiers verbatim — or, for a
// GUC_LIST_QUOTE variable, through quote_identifier, so that list elements
// survive the later split (search_path = 'a b', public -> "a b", public).
// A variable without GUC_LIST_INPUT accepts exactly one element.
func FlattenSetArgs(name string, args []SetArg) (string, error) {
	flags, _ := builtinVarFlags(name)
	if flags&FlagListInput == 0 && len(args) != 1 {
		return "", &ValidationError{Msg: fmt.Sprintf("SET %s takes only one argument", name)}
	}
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		if a.Kind == SetArgString && flags&FlagListQuote != 0 {
			b.WriteString(sqlkeywords.QuoteIdentifier(a.Val))
		} else {
			b.WriteString(a.Val)
		}
	}
	return b.String(), nil
}
