package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// Tablespace storage parameters — M0134-0176.
//
// pg_tablespace.spcoptions was hardcoded NULL and the WITH clause of CREATE
// TABLESPACE was parsed into a raw token dump nobody read, so every tablespace
// storage parameter was silently accepted and discarded: `CREATE TABLESPACE t
// LOCATION '' WITH (some_nonexistent_parameter = true)` SUCCEEDED where PG
// raises 22023, and the tablespace it left behind then turned the following
// (valid) CREATE into a spurious "already exists" — the same silent-acceptance
// cascade M0134-0160 fixed for relations.
//
// Upstream keeps the whole tablespace option lifecycle in two functions:
// transformRelOptions merges the caller's DefElem list into the existing
// spcoptions array (postgres/src/backend/access/common/reloptions.c:1160), and
// tablespace_reloptions then validates the MERGED array against
// RELOPT_KIND_TABLESPACE (:2091). Both CreateTableSpace (tablespace.c:359) and
// AlterTableSpaceOptions (tablespace.c:1015) go through that pair, which is why
// this file has one helper serving both.

// tablespaceOptionArray merges opts into the existing spcoptions array `old` and
// validates the result, returning the new array (nil = SQL NULL). It reproduces
// transformRelOptions' order exactly: surviving old elements first, in their
// original order and minus every name mentioned in opts, then — for SET/CREATE
// only — the new elements in source order.
//
// isReset selects RESET semantics: the named options are removed and nothing is
// appended. An element that carries a VALUE is rejected there, because upstream
// only checks it at this point ("the grammar doesn't enforce it",
// reloptions.c:1228-1243). Note the asymmetry this produces, and it is
// upstream's: RESET validates the SURVIVING array, so `RESET (bogus)` removes
// nothing, validates the untouched remainder and succeeds — a name is only ever
// rejected on the way IN.
func tablespaceOptionArray(old []string, opts []parser.TablespaceOption, isReset bool, pos int) ([]string, *ExecError) {
	// RESET (name = value) — ERRCODE_SYNTAX_ERROR, raised before anything else
	// touches the array (reloptions.c:1238-1243).
	if isReset {
		for _, o := range opts {
			if o.HasValue {
				return nil, &ExecError{Code: "42601", Pos: pos,
					Message: "RESET must not include values for parameters"}
			}
		}
	}
	mentioned := make(map[string]bool, len(opts))
	for _, o := range opts {
		mentioned[o.Name] = true
	}
	merged := make([]string, 0, len(old)+len(opts))
	for _, e := range old {
		if name, _, ok := strings.Cut(e, "="); ok && mentioned[name] {
			continue
		}
		merged = append(merged, e)
	}
	if !isReset {
		for _, o := range opts {
			// A bare `name` means `name=true` (reloptions.c:1291-1296).
			value := o.Value
			if !o.HasValue {
				value = "true"
			}
			merged = append(merged, fmt.Sprintf("%s=%s", o.Name, value))
		}
	}
	// Validate the MERGED array, not the caller's list — that is what
	// tablespace_reloptions receives. The names reach here in source order, so
	// a clause with several bad ones reports the first, as upstream does.
	names := make([]string, 0, len(merged))
	for _, e := range merged {
		name, _, _ := strings.Cut(e, "=")
		names = append(names, name)
	}
	// validnsps is NULL for both tablespace callers, so any `ns.name` is an
	// "unrecognized parameter namespace" — hence allowNamespaces=false. oids is
	// not filtered here either: only DefineRelation/CTAS pass acceptOidsOff.
	if verr := validateRelOptionNamesInOrder(names, relOptTablespace, false, false, pos); verr != nil {
		return nil, verr
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}

// execAlterTablespace handles the four ALTER TABLESPACE forms. Until M0134-0176
// none of them parsed: they fell through every arm of the hand-written
// parseAlter to its closing `expectKeyword(KwTable)` and came back as
// `syntax error at or near "expected keyword table (got tablespace)"`.
//
// Upstream splits the work across AlterTableSpaceOptions (tablespace.c:1015),
// RenameTableSpace (:930) and AlterTableSpaceOwner; all four forms start from
// the same "tablespace %s does not exist" lookup (ERRCODE_UNDEFINED_OBJECT), so
// that check is hoisted here. The owner permission checks each of them performs
// (object_ownercheck) are NOT reproduced — goopg has no tablespace-owner ACL to
// check against; see the deferral ledger row for M0134-0176.
func (o *ddlOp) execAlterTablespace(s *parser.AlterTablespaceStmt) error {
	if _, found := o.ctx.Catalog.LookupTablespaceOID(s.Name); !found {
		return &ExecError{Code: "42704", Pos: s.Pos(),
			Message: fmt.Sprintf("tablespace %q does not exist", s.Name)}
	}
	switch s.Action {
	case "set", "reset":
		old, _ := o.ctx.Catalog.TablespaceOptions(s.Name)
		merged, verr := tablespaceOptionArray(old, s.Options, s.Action == "reset", s.Pos())
		if verr != nil {
			return verr
		}
		o.ctx.Catalog.SetTablespaceOptions(s.Name, merged)
		return nil
	case "rename":
		// Same reserved-name rule CREATE TABLESPACE enforces, from the same
		// IsReservedName test (tablespace.c:966-971).
		if len(s.NewName) >= 3 && strings.EqualFold(s.NewName[:3], "pg_") {
			return &ExecError{
				Code:    "42939",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("unacceptable tablespace name %q", s.NewName),
				Detail:  `The prefix "pg_" is reserved for system tablespaces.`,
			}
		}
		if _, taken := o.ctx.Catalog.LookupTablespaceOID(s.NewName); taken && !strings.EqualFold(s.NewName, s.Name) {
			return &ExecError{Code: "42710", Pos: s.Pos(),
				Message: fmt.Sprintf("tablespace %q already exists", s.NewName)}
		}
		o.ctx.Catalog.RenameTablespace(s.Name, s.NewName)
		return nil
	case "owner":
		// PG resolves the RoleSpec and errors on an unknown role before
		// touching the tuple; goopg's role registry is the same one CREATE
		// TABLESPACE ... OWNER consults.
		if !o.ctx.Catalog.RoleExists(s.NewOwner) && !strings.EqualFold(s.NewOwner, "postgres") {
			return &ExecError{Code: "42704", Pos: s.Pos(),
				Message: fmt.Sprintf("role %q does not exist", s.NewOwner)}
		}
		o.ctx.Catalog.SetTablespaceOwner(s.Name, s.NewOwner)
		return nil
	}
	return &ExecError{Code: "0A000", Pos: s.Pos(),
		Message: fmt.Sprintf("unsupported ALTER TABLESPACE action %q", s.Action)}
}
