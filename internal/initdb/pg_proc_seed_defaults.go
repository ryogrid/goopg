package initdb

import (
	"sync"

	"github.com/goopg/goopg/internal/nodes"
)

// pg_proc argument DEFAULTs for bootstrap (internal-language) functions.
//
// Why this file exists.
//
// `postgres/src/include/catalog/pg_proc.dat` cannot express argument defaults:
// grep it for `pronargdefaults` and you get zero hits. Upstream instead runs
// `postgres/src/backend/catalog/system_functions.sql` at the tail of initdb,
// which `CREATE OR REPLACE FUNCTION`s ~50 built-ins purely to attach DEFAULT
// clauses (and a few PARALLEL/STRICT tweaks). That statement is what fills in
// `pronargdefaults` and the `proargdefaults` pg_node_tree.
//
// goopg seeds pg_proc straight from the generated pg_proc.dat mirror
// (`pg_proc_seed_data.go`) and never replays system_functions.sql, so every
// seeded row carried `pronargdefaults = 0` and an empty `proargdefaults`. That
// is invisible to goopg's own executor — its builtin dispatch resolves call
// shapes itself — but it is fatal to a real PG 18.3 attached to goopg's cluster
// directory: `parse_func.c:func_get_detail` builds the candidate list from
// `FuncnameGetCandidates`, which only allows a call with fewer arguments than
// `pronargs` when `pronargdefaults` covers the shortfall. With 0 defaults,
// `SELECT pg_create_physical_replication_slot('s')` on the standby fails with
//
//	ERROR: function pg_create_physical_replication_slot(unknown) does not exist
//
// which is exactly what blocked Phase D (reverse attach) of
// TestE2E_PGStandbyFullCycle: the promoted PG could not create its own slot.
//
// Form of the value.
//
// `proargdefaults` holds a bare List of the expressions for the *trailing*
// `pronargdefaults` arguments, in argument order. For a boolean DEFAULT false
// the parser stores a plain bool Const, so the whole value is two Consts — the
// bytes below are byte-identical to what a stock `initdb` produces (verified by
// querying a scratch PG 18.3 cluster; the golden string is asserted in
// pg_proc_seed_defaults_test.go).
//
// Scope: this table covers only the entries needed so far. The remaining
// system_functions.sql DEFAULT clauses are ledgered — adding one is a matter of
// appending an entry here.
type pgProcSeedDefault struct {
	// nargDefaults is pg_proc.pronargdefaults: how many of the trailing
	// arguments have defaults.
	nargDefaults int
	// defaults are the default expressions for those trailing arguments, in
	// argument order; serialized into proargdefaults as a bare List.
	defaults []nodes.Node
}

// pgProcSeedDefaults maps a pg_proc OID to its system_functions.sql defaults.
var pgProcSeedDefaults = map[uint32]pgProcSeedDefault{
	// system_functions.sql:469 — pg_create_physical_replication_slot(
	//   IN slot_name name,
	//   IN immediately_reserve boolean DEFAULT false,
	//   IN temporary boolean DEFAULT false, OUT ...)
	3779: {
		nargDefaults: 2,
		defaults: []nodes.Node{
			nodes.NewBoolConst(false), // immediately_reserve
			nodes.NewBoolConst(false), // temporary
		},
	},
}

// pgProcSeedDefaultsTrees caches the serialized pg_node_tree per OID; the seed
// runs once per initdb but the render is pure, so memoize rather than re-walk
// the IR for each of the 3397 rows.
var pgProcSeedDefaultsTrees = sync.OnceValue(func() map[uint32]string {
	m := make(map[uint32]string, len(pgProcSeedDefaults))
	for oid, d := range pgProcSeedDefaults {
		m[oid] = nodes.OutList(d.defaults)
	}
	return m
})

// pgProcSeedArgDefaults returns the (pronargdefaults, proargdefaults) pair for
// a seeded pg_proc OID. Functions with no defaults return (0, "") — the
// pre-existing seed behaviour.
func pgProcSeedArgDefaults(oid uint32) (int, string) {
	d, ok := pgProcSeedDefaults[oid]
	if !ok {
		return 0, ""
	}
	return d.nargDefaults, pgProcSeedDefaultsTrees()[oid]
}
