package misc

// goopg's own configuration parameters. PostgreSQL 18.3 has no parameter by
// these names, and PG rejects an unknown plain name ("unrecognized
// configuration parameter"). A server-private knob belongs where PG puts an
// extension's parameters: a dotted custom-variable name
// (DefineCustomBoolVariable, guc.c), here under the "goopg." prefix. Before
// M0122-0008 they were registered as wal_sender_memory_buffer,
// enable_nestloop_index and enable_opportunistic_prune, so goopg accepted
// SET and postgresql.conf lines that PG refuses and listed them as if they
// were built-in. (goopg.enable_nestloop_index was retired at M0147-0051 with
// the legacy NLI post-pass it switched off.)
const (
	GoopgWalSenderMemoryBuffer    = "goopg.wal_sender_memory_buffer"
	GoopgEnableOpportunisticPrune = "goopg.enable_opportunistic_prune"
)
