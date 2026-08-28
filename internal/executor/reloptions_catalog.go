package executor

import (
	"fmt"
	"sort"
	"strings"
)

// Reloption name/namespace registry — M0134-0160.
//
// Until this file existed goopg validated storage parameters purely by
// *recognising* them: every `WITH (...)` consumer was a chain of
// `if v, ok := s.With["fillfactor"]; ok { ... }` blocks, so a name nobody
// looked for was silently accepted and dropped. `CREATE TABLE t(i int) WITH
// (not_existing_option=2)` therefore SUCCEEDED, where PG raises
//
//	ERROR:  unrecognized parameter "not_existing_option"
//
// That is a silent-acceptance correctness gap on its own (a typo'd
// `autovacuum_enable=false` looks like it took effect and does nothing), and it
// also cascades: reloptions.sql's negative cases all reuse one relation name,
// so the first silently-accepted option creates the relation and every later
// statement reports a spurious "relation already exists" instead of its own
// error. execCreateIndex's `buffering` check (operators_ddl.go, M0134-0127)
// already documents that exact cascade for one option; this registry closes it
// for all of them.
//
// Upstream model: PG keeps five static tables — boolRelOpts / intRelOpts /
// realRelOpts / enumRelOpts / stringRelOpts in
// postgres/src/backend/access/common/reloptions.c — each entry tagging a name
// with a bitmask of the relation kinds that admit it (relopt_kind,
// postgres/src/include/access/reloptions.h). parseRelOptions walks the caller's
// option array against the subset of those tables matching the relation's kind
// and raises "unrecognized parameter" for a name with no match
// (reloptions.c:1488). Namespaces are checked earlier and separately, in
// transformRelOptions, against the caller's validnsps list — for heap relations
// HEAP_RELOPT_NAMESPACES = {"toast", NULL} — raising "unrecognized parameter
// namespace" (reloptions.c:1275). Both use ERRCODE_INVALID_PARAMETER_VALUE
// (22023).
//
// The kinds below carry only what goopg can be asked about through SQL. The
// ATTRIBUTE (ALTER TABLE ... ALTER COLUMN SET (n_distinct...)) and TABLESPACE
// (ALTER TABLESPACE ... SET (...)) kinds are deliberately absent: those two
// clauses have their own validation paths and are not routed through a
// `WITH (...)` map.

// relOptKind mirrors PG's relopt_kind bitmask
// (postgres/src/include/access/reloptions.h:39-56). One bit per relation kind
// that has its own admissible-option set.
type relOptKind uint32

const (
	relOptHeap relOptKind = 1 << iota
	relOptToast
	relOptBTree
	relOptHash
	relOptGiST
	relOptSPGiST
	relOptGIN
	relOptBRIN
	relOptView
)

// relOptionKinds is the union of PG 18.3's five reloption tables, flattened to
// name -> kind bitmask. Verified against
// postgres/src/backend/access/common/reloptions.c (PG 18.3): 24 names admit
// RELOPT_KIND_HEAP and 18 admit RELOPT_KIND_TOAST, which is exactly the set
// execCreateTable already extracts.
//
// Note the asymmetries — they are upstream's, not typos: parallel_workers,
// toast_tuple_target, user_catalog_table, autovacuum_analyze_threshold and
// autovacuum_analyze_scale_factor are HEAP-only (a TOAST relation is never
// analyzed and never scanned in parallel), so `toast.parallel_workers` is an
// error in PG.
var relOptionKinds = map[string]relOptKind{
	// boolRelOpts
	"autosummarize":      relOptBRIN,
	"autovacuum_enabled": relOptHeap | relOptToast,
	"user_catalog_table": relOptHeap,
	"fastupdate":         relOptGIN,
	"security_barrier":   relOptView,
	"security_invoker":   relOptView,
	"vacuum_truncate":    relOptHeap | relOptToast,
	"deduplicate_items":  relOptBTree,

	// intRelOpts
	"fillfactor":                            relOptHeap | relOptBTree | relOptHash | relOptGiST | relOptSPGiST,
	"autovacuum_vacuum_threshold":           relOptHeap | relOptToast,
	"autovacuum_vacuum_max_threshold":       relOptHeap | relOptToast,
	"autovacuum_vacuum_insert_threshold":    relOptHeap | relOptToast,
	"autovacuum_analyze_threshold":          relOptHeap,
	"autovacuum_vacuum_cost_limit":          relOptHeap | relOptToast,
	"autovacuum_freeze_min_age":             relOptHeap | relOptToast,
	"autovacuum_multixact_freeze_min_age":   relOptHeap | relOptToast,
	"autovacuum_freeze_max_age":             relOptHeap | relOptToast,
	"autovacuum_multixact_freeze_max_age":   relOptHeap | relOptToast,
	"autovacuum_freeze_table_age":           relOptHeap | relOptToast,
	"autovacuum_multixact_freeze_table_age": relOptHeap | relOptToast,
	"log_autovacuum_min_duration":           relOptHeap | relOptToast,
	"toast_tuple_target":                    relOptHeap,
	"pages_per_range":                       relOptBRIN,
	"gin_pending_list_limit":                relOptGIN,
	"parallel_workers":                      relOptHeap,
	"vacuum_index_cleanup":                  relOptHeap | relOptToast,
	"vacuum_max_eager_freeze_failure_rate":  relOptHeap | relOptToast,
	"autovacuum_vacuum_scale_factor":        relOptHeap | relOptToast,
	"autovacuum_vacuum_insert_scale_factor": relOptHeap | relOptToast,
	"autovacuum_analyze_scale_factor":       relOptHeap,
	"autovacuum_vacuum_cost_delay":          relOptHeap | relOptToast,
	"vacuum_cleanup_index_scale_factor":     relOptBTree,

	// enumRelOpts
	"buffering":    relOptGiST,
	"check_option": relOptView,
}

// relOptionNamespaces maps a `WITH (ns.name=...)` namespace to the kind whose
// option set it admits, for the relation kinds that declare one. Only heap
// relations declare a namespace upstream: HEAP_RELOPT_NAMESPACES = {"toast",
// NULL} (postgres/src/include/access/reloptions.h), passed as validnsps by
// DefineRelation and ATExecSetRelOptions
// (postgres/src/backend/commands/tablecmds.c). Options landing in it are
// validated against RELOPT_KIND_TOAST, because DefineRelation hands them to
// heap_reloptions(RELKIND_TOASTVALUE, ...).
var relOptionNamespaces = map[string]relOptKind{
	"toast": relOptToast,
}

// indexRelOptKind maps an index access-method name to its reloption kind. An
// unknown method yields 0, which admits no option at all — the same outcome as
// PG's amoptions == NULL AM, whose index_reloptions() rejects every parameter.
func indexRelOptKind(method string) relOptKind {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "", "btree":
		return relOptBTree
	case "hash":
		return relOptHash
	case "gist":
		return relOptGiST
	case "spgist":
		return relOptSPGiST
	case "gin":
		return relOptGIN
	case "brin":
		return relOptBRIN
	}
	return 0
}

// validateRelOptionNames reproduces PG's two name-level checks over a
// `WITH (...)` clause: every namespace must be one the relation kind declares,
// and every option name must be admitted by the kind it lands in.
//
// allowNamespaces gates the namespace pass. Index and view relations pass
// false: DefineIndex and view DDL hand transformRelOptions a NULL validnsps, so
// ANY qualified name is "unrecognized parameter namespace".
//
// acceptOidsOff mirrors transformRelOptions' parameter of the same name
// (reloptions.c:1307-1322): an UNQUALIFIED `oids` option is not a reloption at
// all, and DefineRelation / CTAS filter it out before validation so the legacy
// no-op `WITH (oids = false)` keeps working. `oids = true` is rejected earlier,
// by the caller's own WITH OIDS guard. ALTER ... SET and DefineIndex pass false.
//
// Ordering: PG runs transformRelOptions (namespaces, over the whole DefElem
// list) to completion before parseRelOptions (names), so a clause with both a
// bad namespace and a bad name reports the namespace. This function keeps that
// two-pass order. Within a pass goopg reports the lexicographically first
// offender rather than the source-order first one, because the WITH clause
// reaches here as a map — see the deferral ledger row for M0134-0160.
func validateRelOptionNames(names []string, kind relOptKind, allowNamespaces, acceptOidsOff bool, pos int) *ExecError {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	// Pass 1 — namespaces (transformRelOptions, reloptions.c:1254-1276).
	for _, full := range sorted {
		ns, _, qualified := splitRelOptionName(full)
		if !qualified {
			continue
		}
		if _, ok := relOptionNamespaces[ns]; !ok || !allowNamespaces {
			return &ExecError{Code: "22023", Pos: pos,
				Message: fmt.Sprintf("unrecognized parameter namespace %q", ns)}
		}
	}
	// Pass 2 — names (parseRelOptions, reloptions.c:1459-1489).
	for _, full := range sorted {
		ns, name, qualified := splitRelOptionName(full)
		if acceptOidsOff && !qualified && name == "oids" {
			continue
		}
		want := kind
		if qualified {
			want = relOptionNamespaces[ns]
		}
		if relOptionKinds[name]&want == 0 {
			return &ExecError{Code: "22023", Pos: pos,
				Message: fmt.Sprintf("unrecognized parameter %q", name)}
		}
	}
	return nil
}

// validateRelOptionMap is validateRelOptionNames over a WITH map, which is how
// CREATE TABLE / ALTER TABLE carry the clause.
func validateRelOptionMap(with map[string]string, kind relOptKind, allowNamespaces, acceptOidsOff bool, pos int) *ExecError {
	if len(with) == 0 {
		return nil
	}
	names := make([]string, 0, len(with))
	for k := range with {
		names = append(names, k)
	}
	return validateRelOptionNames(names, kind, allowNamespaces, acceptOidsOff, pos)
}

// splitRelOptionName splits `ns.name` into its parts. PG's grammar builds the
// namespace from a separate DefElem field (def->defnamespace), so only the
// FIRST dot separates: a name may not itself contain one, and an unqualified
// name is returned as-is.
func splitRelOptionName(full string) (ns, name string, qualified bool) {
	if i := strings.IndexByte(full, '.'); i >= 0 {
		return full[:i], full[i+1:], true
	}
	return "", full, false
}
