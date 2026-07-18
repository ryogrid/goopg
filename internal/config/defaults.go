package config

// BuildDefaultRegistry returns a Registry seeded with every GUC the
// goopg server currently advertises. Variables added here should cite
// the upstream entry in postgres/src/backend/utils/misc/guc_tables.c so
// the choice of defaults is auditable.
//
// The list is intentionally small — just what the existing
// ParameterStatus block, the simple-query SHOW path, and the auth /
// listener wiring need. Each later milestone should append the GUCs it
// actually consumes rather than batch-importing the whole upstream
// table.
func BuildDefaultRegistry() *Registry {
	r := NewRegistry()

	// preset / report GUCs (all PGC_INTERNAL or PGC_USERSET in upstream)
	r.MustRegister(NewVariable(Variable{
		Name: "server_version", Type: TypeString, BootVal: "18.3",
		Context: ContextInternal, Flags: FlagReport | FlagDisallowInFile,
		Scope: ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "server_version_num", Type: TypeInt, BootVal: "180003",
		Context: ContextInternal, Flags: FlagDisallowInFile,
		Scope: ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "server_encoding", Type: TypeString, BootVal: "UTF8",
		Context: ContextInternal, Flags: FlagReport | FlagDisallowInFile,
		Scope: ScopeServer,
	}))
	// block_size reports the data-page size (BLCKSZ) the cluster was built
	// with. A read-only preset (PGC_INTERNAL) — not settable, not in
	// postgresql.conf.sample. Exercised by current_setting('block_size') in the
	// stats isolation spec (sizing a large pg_notify payload). M0118-0009.
	r.MustRegister(NewVariable(Variable{
		Name: "block_size", Type: TypeInt, BootVal: "8192",
		Context: ContextInternal, Flags: FlagDisallowInFile,
		Scope: ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "client_encoding", Type: TypeString, BootVal: "UTF8",
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "DateStyle", Type: TypeString, BootVal: "ISO, MDY",
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "IntervalStyle", Type: TypeEnum, BootVal: "postgres",
		EnumOptions: []string{"postgres", "postgres_verbose", "sql_standard", "iso_8601"},
		Context:     ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "TimeZone", Type: TypeString, BootVal: "UTC",
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))
	// timezone_abbreviations selects the abbreviation set used when parsing
	// timestamps (e.g. 'Default', 'Australia', 'India'). PostgreSQL validates
	// the value against an abbreviation file; goopg accepts any string since
	// pg_timezone_abbrevs is a static stub. Required by sysviews.sql, which
	// issues `SET timezone_abbreviations = 'Australia'` / `'India'`. Not a
	// GUC_REPORT parameter in upstream, so no FlagReport here. M0097-0032.
	r.MustRegister(NewVariable(Variable{
		Name: "timezone_abbreviations", Type: TypeString, BootVal: "Default",
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "integer_datetimes", Type: TypeBool, BootVal: "on",
		Context: ContextInternal, Flags: FlagReport | FlagDisallowInFile,
		Scope: ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "standard_conforming_strings", Type: TypeBool, BootVal: "on",
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "is_superuser", Type: TypeBool, BootVal: "off",
		Context: ContextInternal, Flags: FlagReport | FlagDisallowInFile,
		Scope: ScopeSession,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "session_authorization", Type: TypeString, BootVal: "",
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "in_hot_standby", Type: TypeBool, BootVal: "off",
		Context: ContextInternal, Flags: FlagReport | FlagDisallowInFile,
		Scope: ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "default_transaction_read_only", Type: TypeBool, BootVal: "off",
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "application_name", Type: TypeString, BootVal: "",
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "default_transaction_isolation", Type: TypeEnum, BootVal: "read committed",
		EnumOptions: []string{"read uncommitted", "read committed", "repeatable read", "serializable"},
		Context:     ContextUserset,
		Scope:       ScopeSession | ScopeTransaction,
	}))
	// Object-creation default GUCs (CLIENT_CONN_STATEMENT). pg_dump/pg_restore
	// emit `SET default_tablespace = '';` and `SET default_table_access_method
	// = heap;` before every CREATE TABLE section (and `SET
	// default_toast_compression = 'pglz';` when a column carries non-default
	// compression), so an unregistered name aborted a real-PG dump replay on
	// goopg with "unrecognized configuration parameter". Register them as
	// accepted stubs: goopg only implements the heap access method and uses its
	// own built-in TOAST default, and has no real tablespaces, so a SET to the
	// default value is a true no-op and a non-default value is accepted and
	// ignored (behavioral no-op ledgered, same as the enable_*/geqo stubs).
	// Names, contexts, and boot values mirror
	// postgres/src/backend/utils/misc/guc_tables.c (CLIENT_CONN_STATEMENT, all
	// PGC_USERSET); DEFAULT_TABLE_ACCESS_METHOD ("heap", tableam.h) and
	// TOAST_PGLZ_COMPRESSION ("pglz", toast_compression.h) supply the defaults;
	// the toast enum options match the reference PG 18.3 build's --with-lz4.
	// M0122-0007.
	r.MustRegister(NewVariable(Variable{
		Name: "default_table_access_method", Type: TypeString, BootVal: "heap",
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "default_tablespace", Type: TypeString, BootVal: "",
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "default_toast_compression", Type: TypeEnum, BootVal: "pglz",
		EnumOptions: []string{"pglz", "lz4"},
		Context:     ContextUserset, Scope: ScopeSession | ScopeTransaction,
	}))
	// SSI predicate-lock sizing (M0104-0003). Names, defaults, and
	// ranges mirror postgres/src/backend/utils/misc/guc_tables.c so
	// existing tooling (postgresql.conf templates, parameter probes,
	// pgbench setups) keeps working unchanged.
	//
	// Upstream encodes the per-relation default as -2 (and per-xact-
	// coarsened-from-per-relation as a negative fraction). goopg
	// surfaces the GUC values verbatim here so the server-side
	// bridge into `mvcc.Manager.SetPredicateLockLimits` is the
	// only place that resolves the negative shorthand into positive
	// coarsening thresholds.
	r.MustRegister(NewVariable(Variable{
		Name: "max_predicate_locks_per_xact", Type: TypeInt, BootVal: "64",
		MinVal: 10, MaxVal: 1 << 30,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_predicate_locks_per_relation", Type: TypeInt, BootVal: "-2",
		MinVal: -1 << 30, MaxVal: 1 << 30,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_predicate_locks_per_page", Type: TypeInt, BootVal: "2",
		MinVal: 0, MaxVal: 1 << 16,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))

	// connection-level GUCs that goopg actually honours today.
	r.MustRegister(NewVariable(Variable{
		Name: "listen_addresses", Type: TypeString, BootVal: "localhost",
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "port", Type: TypeInt, BootVal: "5432",
		MinVal: 1, MaxVal: 65535,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_connections", Type: TypeInt, BootVal: "100",
		MinVal: 1, MaxVal: 262143,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	// Resource-sizing GUCs echoed into global/pg_control by InitControlFile
	// (xlog.c:4223-4227) so a standby's CheckRequiredParameterValues can
	// verify it has at least as many resources as the primary.
	r.MustRegister(NewVariable(Variable{
		Name: "max_worker_processes", Type: TypeInt, BootVal: "8",
		MinVal: 0, MaxVal: 262143,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_prepared_transactions", Type: TypeInt, BootVal: "0",
		MinVal: 0, MaxVal: 262143,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_locks_per_transaction", Type: TypeInt, BootVal: "64",
		MinVal: 10, MaxVal: 2147483647,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "scram_iterations", Type: TypeInt, BootVal: "4096",
		MinVal: 1, MaxVal: 1 << 30,
		Context: ContextUserset, Flags: FlagReport,
		Scope: ScopeSession,
	}))

	// shared_buffers sizes the buffer pool. Upstream's default is 128
	// MB; goopg matches that so cmd/goopg start with no postgresql.conf
	// behaves like upstream initdb does. The native unit is KB (matches
	// postgres/src/backend/utils/misc/guc_tables.c GUC_UNIT_KB), so the
	// canonical value is the KB count and Display() returns it as a
	// bare integer. cmd/goopg derives PoolSlots from this at server
	// start: slots = sharedBuffersKB / (BlockSize/1024) = KB / 8.
	r.MustRegister(NewVariable(Variable{
		Name: "shared_buffers", Type: TypeInt, Unit: UnitKB, BootVal: "128MB",
		MinVal: 128, MaxVal: 1 << 40,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// transaction_buffers sizes the dedicated CLOG (commit-log) SLRU buffer
	// pool — the number of BLCKSZ pages of the 2-bit transaction-status cache
	// held resident at once (gap G6 / M0117-0006). Mirrors upstream's
	// guc_tables.c entry: PGC_POSTMASTER, RESOURCES_MEM, GUC_UNIT_BLOCKS, with
	// 0 meaning "auto-tune from a fraction of shared_buffers" (see
	// clog.c:CLOGShmemBuffers → mvcc.EffectiveCLOGBuffers). The raw integer is a
	// count of buffers (one block == one CLOG buffer upstream, so the stored
	// value matches PG's GUC_UNIT_BLOCKS value); goopg registers it unit-less
	// because the config layer has no 8 kB block unit. Max is
	// SLRU_MAX_ALLOWED_BUFFERS = 1 GiB / BLCKSZ.
	r.MustRegister(NewVariable(Variable{
		Name: "transaction_buffers", Type: TypeInt, BootVal: "0",
		MinVal: 0, MaxVal: (1 << 30) / 8192,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// WAL & checkpointer GUCs (milestone 0002). Names, units, ranges,
	// and defaults mirror upstream's
	// postgres/src/backend/utils/misc/guc_tables.c entries.
	// PGC_SIGHUP -> ContextSigHup so a goopg reload (control-socket
	// RELOAD, or SIGHUP) picks up a changed value on the running
	// server via Registry.ApplyReloadEntries.
	r.MustRegister(NewVariable(Variable{
		Name: "checkpoint_timeout", Type: TypeInt, Unit: UnitS, BootVal: "300",
		MinVal: 30, MaxVal: 86400,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "checkpoint_completion_target", Type: TypeReal, BootVal: "0.9",
		MinVal: 0.0, MaxVal: 1.0,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_wal_size", Type: TypeInt, Unit: UnitMB, BootVal: "1024",
		MinVal: 2, MaxVal: 2147483647,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "min_wal_size", Type: TypeInt, Unit: UnitMB, BootVal: "80",
		MinVal: 2, MaxVal: 2147483647,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "full_page_writes", Type: TypeBool, BootVal: "on",
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))

	// fsync mirrors upstream's GUC of the same name (guc_tables.c: "Forces
	// synchronization of updates to disk."): when off, every fsync/fdatasync
	// issued for durability — the WAL commit flush, checkpoint data-file
	// sync, CLOG/SLRU sync — is skipped. Write ordering and content are
	// unchanged, so a process crash still recovers; only host-crash
	// durability is forfeit. Default `on` (PG parity, GUC-defaults rule);
	// test harnesses opt into `off` per postgresql.conf, exactly like
	// upstream PostgreSQL::Test::Cluster. See
	// ci/design/test-gate-speedups/02-durability-off-for-test-servers.md.
	r.MustRegister(NewVariable(Variable{
		Name: "fsync", Type: TypeBool, BootVal: "on",
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))

	// wal_init_zero controls whether new WAL segments are zero-
	// filled at creation time so subsequent commit-path syncs
	// don't pay for inode metadata updates and don't trigger
	// filesystem allocations on the hot path. Default `on`
	// matches upstream. See
	// docs/design/0007-0001-wal-segment-preallocation.md.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_init_zero", Type: TypeBool, BootVal: "on",
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// wal_sync_method selects the commit-path durability barrier
	// flushUpTo's dataSync stage uses. Enum options match what's
	// available on Linux upstream (postgres/src/backend/access/
	// transam/xlog.c's wal_sync_method_options — fsync_writethrough
	// is Windows/macOS-only, excluded here). Default "fdatasync"
	// matches upstream's PLATFORM_DEFAULT_WAL_SYNC_METHOD on Linux
	// (postgres/src/include/port/linux.h). Only "fsync"/"fdatasync"
	// are implemented today; "open_sync"/"open_datasync" are accepted
	// here (for SHOW/pg_settings parity with upstream) but rejected
	// by wal.NewWriter with ErrUnsupportedSyncMethod until O_SYNC/
	// O_DSYNC open-time flags are wired across every WAL segment-open
	// site — mirrors io_method=io_uring's ErrUnsupportedMethod
	// precedent below. ContextSigHup matches upstream's PGC_SIGHUP
	// (guc_tables.c); goopg reads it once at Writer construction, same
	// as every other ContextPostmaster/ContextSigHup WAL GUC — live
	// SIGHUP-triggered reconfiguration is out of scope until the
	// `reload` control-socket command stops being a no-op stub. See
	// docs/design/0007-0002-fdatasync-commit-path.md.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_sync_method", Type: TypeEnum, BootVal: "fdatasync",
		EnumOptions: []string{"fsync", "fdatasync", "open_sync", "open_datasync"},
		Context:     ContextSigHup,
		Scope:       ScopeServer,
	}))

	// track_io_timing gates per-I/O activity wait-event hooks
	// (BufferPin / DataFileRead / Write / Extend / Sync / AIO).
	// Default `off` mirrors upstream PG. ContextUserset: `SET
	// track_io_timing` takes effect immediately per session — the
	// hooks (internal/initdb/open.go) are always installed and check
	// the calling backend's live flag via
	// activity.ActivityRegistry.LookupTrackedGoroutine, which itself
	// short-circuits on a process-wide fast-path flag so the
	// default-off case still costs only one atomic load (M0092-0005's
	// original rationale; M0122-0003 runtime-SET follow-up). See
	// docs/design/0092-0005-lookup-goroutine-io-hooks-guc.md.
	r.MustRegister(NewVariable(Variable{
		Name: "track_io_timing", Type: TypeBool, BootVal: "off",
		Context: ContextUserset,
		Scope:   ScopeServer,
	}))

	// Compatibility stubs for GUCs checked by pg_regress tests. M0097-0073.
	r.MustRegister(NewVariable(Variable{
		Name: "jit", Type: TypeBool, BootVal: "off",
		Context: ContextUserset,
		Scope:   ScopeServer,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name:        "compute_query_id",
		Type:        TypeEnum,
		BootVal:     "off",
		EnumOptions: []string{"off", "on", "auto", "regress"},
		Context:     ContextUserset,
		Scope:       ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name:        "plan_cache_mode",
		Type:        TypeEnum,
		BootVal:     "auto",
		EnumOptions: []string{"auto", "force_generic_plan", "force_custom_plan"},
		Flags:       FlagExplain,
		Context:     ContextUserset,
		Scope:       ScopeServer,
	}))
	// The rest of upstream's jit_* GUC family (guc_tables.c) — goopg has
	// no JIT compiler at all (not even a stub code path consulted at
	// runtime, unlike enable_nestloop-style planner toggles), so these
	// exist purely so SET/SHOW and pg_settings enumeration don't fail
	// with "unrecognized configuration parameter" on scripts written
	// against a real PostgreSQL. Contexts/defaults/bounds mirror
	// guc_tables.c exactly (jit_debugging_support/jit_profiling_support
	// are PGC_SU_BACKEND; jit_dump_bitcode is PGC_SUSET; jit_provider is
	// PGC_POSTMASTER; the rest are PGC_USERSET).
	r.MustRegister(NewVariable(Variable{
		Name: "jit_debugging_support", Type: TypeBool, BootVal: "off",
		Context: ContextSuBackend,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "jit_dump_bitcode", Type: TypeBool, BootVal: "off",
		Context: ContextSuset,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "jit_expressions", Type: TypeBool, BootVal: "on",
		Context: ContextUserset,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "jit_profiling_support", Type: TypeBool, BootVal: "off",
		Context: ContextSuBackend,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "jit_tuple_deforming", Type: TypeBool, BootVal: "on",
		Context: ContextUserset,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "jit_provider", Type: TypeString, BootVal: "llvmjit",
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// wal_sender_memory_buffer sizes (in bytes) the in-memory
	// ring of recent WAL bytes used by walsender's
	// RecordIterator. 0 disables the ring; >0 mirrors every
	// successful WAL write so senders can stream without
	// disk reads. Default 16 MiB. See
	// docs/design/0010-0002-walsender-in-memory-wal-handoff.md.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_sender_memory_buffer", Type: TypeInt, BootVal: "16777216",
		MinVal: 0, MaxVal: 1 << 30,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// wal_buffers sizes (in bytes) the bounded in-memory WAL
	// buffer that holds generated WAL records before they hit
	// segment files. Default 16 MiB matches PostgreSQL's
	// default. Records ≤ wal_buffers stay in RAM until either
	// (a) Append would overflow the buffer (drain just enough
	// to make room) or (b) FlushUpTo demands a byte ≤ the
	// requested LSN be on disk and durable. 0 disables the
	// buffer entirely (every Append calls writeAt directly —
	// pre-M0013 behaviour). Records larger than wal_buffers
	// bypass the buffer in one shot rather than fragment.
	// See docs/design/0013-0001-wal-buffers-architecture.md.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_buffers", Type: TypeInt, BootVal: "16777216",
		MinVal: 0, MaxVal: 1 << 30,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// synchronous_commit controls whether a transaction commit waits
	// for the WAL record to be flushed to disk before returning to the
	// client. Default on matches upstream's safe default; off allows
	// faster commits at the cost of losing recent committed transactions
	// on a server crash (up to wal_writer_delay latency). See
	// docs/design/0042-0003-wal-buffer-and-writer-alignment.md.
	//
	// M0102-0005: upstream extends this GUC beyond a plain on/off boolean
	// to a 5-level enum (off / local / remote_write / on=remote_flush /
	// remote_apply). goopg accepts the enum spellings via TypeString so a
	// session can set `SET synchronous_commit = remote_apply` without a
	// parse error; the actual wait semantics live in
	// `internal/wal/syncrep.go`. `on` is the boot default, matching upstream.
	r.MustRegister(NewVariable(Variable{
		Name: "synchronous_commit", Type: TypeString, BootVal: "on",
		Context: ContextUserset,
		Scope:   ScopeSession,
	}))

	// synchronous_standby_names selects which standbys must acknowledge a
	// COMMIT before the primary releases its waiter. Grammar mirrors
	// upstream: empty = async (no wait); 'name' or 'a, b' = wait for any
	// listed (PG-pre-9.6 form, now == FIRST 1); 'FIRST n (a, b, c)' = wait
	// for the first n in list order; 'ANY n (a, b, c)' = wait for any n
	// of the listed names. A standby is identified by its
	// application_name. See docs/design/0102-0005-synchronous-replication.md.
	r.MustRegister(NewVariable(Variable{
		Name: "synchronous_standby_names", Type: TypeString, BootVal: "",
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))

	// wal_writer_delay sets the period (in milliseconds) of the
	// background WAL writer loop. The loop calls FlushUpTo to drain
	// buffered WAL bytes so they are not held in RAM indefinitely.
	// Default 200ms mirrors upstream's wal_writer_delay GUC. See
	// docs/design/0042-0003-wal-buffer-and-writer-alignment.md.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_writer_delay", Type: TypeInt, BootVal: "200",
		MinVal: 1, MaxVal: 10000,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// wal_writer_flush_after sets the threshold (in bytes) above which
	// the WAL writer loop issues an fdatasync in addition to writing.
	// Default 1 MiB mirrors upstream's wal_writer_flush_after GUC. 0
	// means always fsync on every loop iteration. See
	// docs/design/0042-0003-wal-buffer-and-writer-alignment.md.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_writer_flush_after", Type: TypeInt, BootVal: "1048576",
		MinVal: 0, MaxVal: 1 << 30,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// commit_delay / commit_siblings drive the backend-driven WAL flush group
	// commit (docs/design/wal-backend-flush/). The would-be flush holder sleeps
	// commit_delay microseconds — holding the WAL write lock — to widen the
	// batch, but only when at least commit_siblings other flushers are in
	// flight. PG defaults: commit_delay 0 (no delay), commit_siblings 5.
	r.MustRegister(NewVariable(Variable{
		Name: "commit_delay", Type: TypeInt, BootVal: "0",
		MinVal: 0, MaxVal: 100000,
		Context: ContextUserset,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "commit_siblings", Type: TypeInt, BootVal: "5",
		MinVal: 0, MaxVal: 1000,
		Context: ContextUserset,
		Scope:   ScopeServer,
	}))

	// bgwriter_delay controls how often (in milliseconds) the background
	// writer goroutine wakes up to flush dirty buffer-pool pages.
	// Default 200ms mirrors upstream's bgwriter_delay GUC (M0048-0003).
	r.MustRegister(NewVariable(Variable{
		Name: "bgwriter_delay", Type: TypeInt, BootVal: "200",
		MinVal: 10, MaxVal: 10000,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// checkpoint_flush_after / bgwriter_flush_after / backend_flush_after
	// set the writeback threshold (in BLCKSZ pages) for the checkpointer /
	// bgwriter / an individual backend's own dirty-victim-eviction writes;
	// once a context's running total crosses its threshold, goopg issues
	// a real sync_file_range(2) write-behind hint (0 disables it — see
	// storage/writeback.go, M0122-0003 pg_stat_io writeback/writeback_time
	// follow-up). Defaults (32/64/0) and max (256) mirror upstream's
	// DEFAULT_CHECKPOINT_FLUSH_AFTER/DEFAULT_BGWRITER_FLUSH_AFTER/
	// DEFAULT_BACKEND_FLUSH_AFTER and WRITEBACK_MAX_PENDING_FLUSHES
	// (pg_config_manual.h). backend_flush_after is PGC_USERSET upstream
	// (per-session); goopg applies it as a single process-wide threshold
	// instead (see deferral ledger).
	r.MustRegister(NewVariable(Variable{
		Name: "checkpoint_flush_after", Type: TypeInt, BootVal: "32",
		MinVal: 0, MaxVal: 256,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "bgwriter_flush_after", Type: TypeInt, BootVal: "64",
		MinVal: 0, MaxVal: 256,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "backend_flush_after", Type: TypeInt, BootVal: "0",
		MinVal: 0, MaxVal: 256,
		Context: ContextUserset,
	}))

	// bgwriter_lru_maxpages caps how many dirty pages the background
	// writer flushes per tick. 0 disables the bgwriter. Default 100
	// mirrors upstream's bgwriter_lru_maxpages GUC (M0048-0003).
	r.MustRegister(NewVariable(Variable{
		Name: "bgwriter_lru_maxpages", Type: TypeInt, BootVal: "100",
		MinVal: 0, MaxVal: 1000,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// io_method picks the AIO I/O method. `sync` runs every
	// I/O on the calling goroutine (the safe default that
	// matches v0's pre-AIO behaviour); `worker` uses a
	// goroutine pool. `io_uring` is reserved for a future loop
	// — selecting it here returns ErrUnsupportedMethod when
	// the engine is constructed. See
	// docs/design/0009-0001-aio-core.md.
	r.MustRegister(NewVariable(Variable{
		Name: "io_method", Type: TypeEnum, BootVal: "worker",
		EnumOptions: []string{"sync", "worker", "io_uring"},
		Context:     ContextPostmaster,
		Scope:       ScopeServer,
	}))
	// io_workers is the goroutine count for `io_method=worker`.
	// Upstream's default is 3; we mirror it.
	r.MustRegister(NewVariable(Variable{
		Name: "io_workers", Type: TypeInt, BootVal: "3",
		MinVal: 1, MaxVal: 1024,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	// io_max_concurrency caps in-flight AIO operations
	// globally. 0 disables the cap (no backpressure). Upstream
	// names this `io_max_concurrency` and lets the platform
	// derive a default; we mirror upstream's "let the method
	// decide" by passing 0 through to the engine, which sets
	// 4×workers for the worker method.
	r.MustRegister(NewVariable(Variable{
		Name: "io_max_concurrency", Type: TypeInt, BootVal: "0",
		MinVal: 0, MaxVal: 1024,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// Compatibility GUCs HammerDB / psql / pgbench issue with
	// SET before running their workloads. v0 doesn't honour any
	// of these semantically — the planner / executor ignores
	// the values — but registering them as ContextUserset lets
	// `SET <name> = <value>` succeed instead of failing with
	// `unrecognized configuration parameter`. Names, units,
	// ranges, and defaults mirror upstream's
	// postgres/src/backend/utils/misc/guc_tables.c entries
	// where applicable.
	r.MustRegister(NewVariable(Variable{
		Name: "max_parallel_workers_per_gather", Type: TypeInt, BootVal: "2",
		MinVal: 0, MaxVal: 1024,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_parallel_maintenance_workers", Type: TypeInt, BootVal: "2",
		MinVal: 0, MaxVal: 1024,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "min_parallel_table_scan_size", Type: TypeInt, Unit: UnitKB, BootVal: "8388608",
		MinVal: 0, MaxVal: 715827882,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "min_parallel_index_scan_size", Type: TypeInt, Unit: UnitKB, BootVal: "524288",
		MinVal: 0, MaxVal: 715827882,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "client_min_messages", Type: TypeEnum, BootVal: "notice",
		EnumOptions: []string{"debug5", "debug4", "debug3", "debug2", "debug1", "log", "notice", "warning", "error"},
		Context:     ContextUserset,
		Scope:       ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "statement_timeout", Type: TypeInt, Unit: UnitMs, BootVal: "0",
		MinVal: 0, MaxVal: 2147483647,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "work_mem", Type: TypeInt, Unit: UnitKB, BootVal: "512MB",
		MinVal: 64, MaxVal: 1 << 40,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "extra_float_digits", Type: TypeInt, BootVal: "1",
		MinVal: -15, MaxVal: 3,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "bytea_output", Type: TypeEnum, BootVal: "hex",
		EnumOptions: []string{"escape", "hex"},
		Context:     ContextUserset,
		Scope:       ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "check_function_bodies", Type: TypeBool, BootVal: "on",
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "random_page_cost", Type: TypeReal, BootVal: "4.0",
		MinVal: 0, MaxVal: 1e9,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "effective_cache_size", Type: TypeInt, Unit: UnitKB, BootVal: "4GB",
		MinVal: 1, MaxVal: 1 << 40,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	// Planner cost GUCs — goopg ignores them but SET succeeds so test
	// scripts that adjust them don't fail with "unrecognized parameter".
	// Values mirror postgres/src/backend/utils/misc/guc_tables.c.
	r.MustRegister(NewVariable(Variable{
		Name: "jit_above_cost", Type: TypeReal, BootVal: "100000",
		MinVal: -1, MaxVal: 1e15,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "jit_optimize_above_cost", Type: TypeReal, BootVal: "500000",
		MinVal: -1, MaxVal: 1e15,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "jit_inline_above_cost", Type: TypeReal, BootVal: "500000",
		MinVal: -1, MaxVal: 1e15,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "parallel_setup_cost", Type: TypeReal, BootVal: "1000",
		MinVal: 0, MaxVal: 1e15,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "parallel_tuple_cost", Type: TypeReal, BootVal: "0.1",
		MinVal: 0, MaxVal: 1e15,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	// debug_parallel_query (renamed from force_parallel_mode) — a developer
	// GUC that forces a parallel plan for testing. goopg has no parallel
	// executor so it is a no-op, but SET must succeed so upstream isolation
	// specs (serializable-parallel*) that flip it during session setup don't
	// fail with "unrecognized configuration parameter". Enum off/on/regress
	// mirrors postgres/src/backend/utils/misc/guc_tables.c.
	r.MustRegister(NewVariable(Variable{
		Name: "debug_parallel_query", Type: TypeEnum, BootVal: "off",
		EnumOptions: []string{"off", "on", "regress"},
		Context:     ContextUserset,
		Scope:       ScopeSession | ScopeTransaction,
		Flags:       FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "search_path", Type: TypeString, BootVal: `"$user", public`,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))

	// transaction_isolation is what JDBC's getTransactionIsolation()
	// reports back; stays read-only with FlagReport so SHOW works
	// without a separate `SET` path.
	r.MustRegister(NewVariable(Variable{
		Name: "transaction_isolation", Type: TypeEnum, BootVal: "read committed",
		EnumOptions: []string{"read uncommitted", "read committed", "repeatable read", "serializable"},
		Context:     ContextUserset, Flags: FlagReport,
		Scope: ScopeSession | ScopeTransaction,
	}))

	// Opportunistic page pruning (M0046-0002). When on, the HOT-update
	// path tries to reclaim universally-dead tuples inline when a page
	// is full, avoiding an unnecessary relation extension.
	r.MustRegister(NewVariable(Variable{
		Name: "enable_opportunistic_prune", Type: TypeBool, BootVal: "on",
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))

	// vacuum_cost_delay — sleep duration between vacuum cost cycles. Default 0
	// (disabled). Unit is ms; values < 1ms display in µs (e.g. "900us").
	// M0097-0031: stub so guc.sql SET/SHOW round-trips work.
	r.MustRegister(NewVariable(Variable{
		Name: "vacuum_cost_delay", Type: TypeReal, Unit: UnitMs, BootVal: "0",
		MinVal: 0, MaxVal: 100,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// track_activities — controls pg_stat_activity tracking.
	r.MustRegister(NewVariable(Variable{
		Name: "track_activities", Type: TypeBool, BootVal: "on",
		Context: ContextSuset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// track_counts — controls collection of per-relation row-count statistics
	// (n_tup_ins/upd/del, seq_scan, etc.). PGC_SUSET, boot on (guc_tables.c).
	// Registered for the isolation `stats` spec, which toggles it; goopg's
	// cumulative-statistics subsystem is being built incrementally
	// (M0118-0009 stats enabler) and consults this flag as it grows.
	r.MustRegister(NewVariable(Variable{
		Name: "track_counts", Type: TypeBool, BootVal: "on",
		Context: ContextSuset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// track_functions — controls collection of per-function call statistics.
	// PGC_SUSET, enum {none, pl, all}, boot "none" (guc_tables.c). "pl" tracks
	// only procedural-language functions, "all" also C/SQL functions.
	// Registered for the isolation `stats` spec (M0118-0009 stats enabler).
	r.MustRegister(NewVariable(Variable{
		Name: "track_functions", Type: TypeEnum, BootVal: "none",
		EnumOptions: []string{"none", "pl", "all"},
		Context:     ContextSuset,
		Scope:       ScopeSession | ScopeTransaction,
	}))
	// stats_fetch_consistency — controls how repeated accesses to cumulative
	// statistics within a transaction behave: "none" re-fetches each access,
	// "cache" caches the first access until end-of-xact / pg_stat_clear_snapshot,
	// "snapshot" caches all of a database's stats on first access. PGC_USERSET,
	// enum, boot "cache" (guc_tables.c). goopg fetches cumulative stats directly
	// (no async collector / snapshot cache), so the value is recognised but only
	// the externally observable behaviour the `stats` spec checks is honoured as
	// the subsystem is built out (M0118-0009 stats enabler).
	r.MustRegister(NewVariable(Variable{
		Name: "stats_fetch_consistency", Type: TypeEnum, BootVal: "cache",
		EnumOptions: []string{"none", "cache", "snapshot"},
		Context:     ContextUserset,
		Scope:       ScopeSession | ScopeTransaction,
	}))
	// default_with_oids — removed in PG12, retained as a recognised no-op.
	r.MustRegister(NewVariable(Variable{
		Name: "default_with_oids", Type: TypeBool, BootVal: "off",
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// allow_in_place_tablespaces — developer/regression option permitting
	// CREATE TABLESPACE ... LOCATION '' to create an in-place tablespace directly
	// inside pg_tblspc. PGC_SUSET, GUC_NOT_IN_SAMPLE, boot off (guc_tables.c).
	// M0095-0003.
	r.MustRegister(NewVariable(Variable{
		Name: "allow_in_place_tablespaces", Type: TypeBool, BootVal: "off",
		Context: ContextSuset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// allow_system_table_mods — developer/regression option permitting
	// modifications of the structure of system tables (e.g. REINDEX of a
	// TOAST relation, ALTER on a catalog). PGC_SUSET, DEVELOPER_OPTIONS,
	// GUC_NOT_IN_SAMPLE, boot off (guc_tables.c). Registered so test scripts
	// that `SET allow_system_table_mods = on` during setup succeed rather than
	// failing with `unrecognized configuration parameter`; goopg does not yet
	// gate any catalog-structure modification on it (M0118-0008,
	// reindex-concurrently-toast enabler).
	r.MustRegister(NewVariable(Variable{
		Name: "allow_system_table_mods", Type: TypeBool, BootVal: "off",
		Context: ContextSuset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// seq_page_cost — planner cost estimate for sequential page fetch.
	r.MustRegister(NewVariable(Variable{
		Name: "seq_page_cost", Type: TypeReal, BootVal: "1.0",
		MinVal: 0, MaxVal: 1 << 30,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	// Per-tuple / per-operator planner cost estimates (guc_tables.c). goopg's
	// planner does not consume these yet, but they must be registered so
	// upstream specs that tune them (e.g. the index-only-scan isolation spec)
	// can SET them without an "unrecognized configuration parameter" error.
	r.MustRegister(NewVariable(Variable{
		Name: "cpu_tuple_cost", Type: TypeReal, BootVal: "0.01",
		MinVal: 0, MaxVal: 1e9,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "cpu_index_tuple_cost", Type: TypeReal, BootVal: "0.005",
		MinVal: 0, MaxVal: 1e9,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "cpu_operator_cost", Type: TypeReal, BootVal: "0.0025",
		MinVal: 0, MaxVal: 1e9,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
		Flags:   FlagExplain,
	}))

	// Tuple-freeze age thresholds (M0046-0005). vacuum_freeze_min_age is
	// the minimum XID age before VACUUM rewrites xmin → FrozenTransactionId.
	// autovacuum_freeze_max_age is the maximum XID age before autovacuum
	// is forced for anti-wraparound protection.
	r.MustRegister(NewVariable(Variable{
		Name: "vacuum_freeze_min_age", Type: TypeInt, BootVal: "50000000",
		MinVal: 0, MaxVal: 1000000000,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "autovacuum_freeze_max_age", Type: TypeInt, BootVal: "200000000",
		MinVal: 100000, MaxVal: 2000000000,
		Context: ContextPostmaster,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// vacuum_multixact_freeze_min_age is the MultiXact-age analog of
	// vacuum_freeze_min_age: the minimum MultiXactId age before VACUUM
	// replaces a tuple's xmax MultiXact with a plain XID / FrozenTransactionId.
	r.MustRegister(NewVariable(Variable{
		Name: "vacuum_multixact_freeze_min_age", Type: TypeInt, BootVal: "5000000",
		MinVal: 0, MaxVal: 1000000000,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))

	// Planner toggle GUCs. Upstream uses them for testing (`SET
	// enable_seqscan = off` to force an index plan). v0's planner
	// ignores them — the rule-based decisions still apply — but
	// the SET succeeds so test scripts don't trip. M0097-0069.
	for _, name := range []string{
		"enable_seqscan", "enable_indexscan", "enable_indexonlyscan",
		"enable_bitmapscan", "enable_hashjoin", "enable_mergejoin",
		"enable_nestloop", "enable_sort", "enable_hashagg",
		"enable_material", "enable_partition_pruning",
		// M0054-0006: nested-loop index join rollback switch.
		// `off` keeps the legacy Hash plan for joins that the
		// rule would otherwise rewrite.
		"enable_nestloop_index",
		// Additional planner method toggles present in catalog pg_settings
		// but missing from the registry — needed so SET succeeds. M0097-0069.
		"enable_partitionwise_join", "enable_partitionwise_aggregate",
		"enable_parallel_hash", "enable_parallel_append",
		"enable_gathermerge", "enable_incremental_sort",
		"enable_async_append", "enable_memoize",
		"enable_presorted_aggregate", "enable_distinct_reordering",
		"enable_group_by_reordering", "enable_self_join_elimination",
		"enable_tidscan",
	} {
		r.MustRegister(NewVariable(Variable{
			Name: name, Type: TypeBool, BootVal: "on",
			Context: ContextUserset,
			Scope:   ScopeSession | ScopeTransaction,
			// All upstream enable_* planner-method GUCs are GUC_EXPLAIN
			// (guc_tables.c); goopg's invented enable_nestloop_index
			// follows the same convention since it too gates a plan shape.
			Flags: FlagExplain,
		}))
	}

	// GEQO (genetic query optimization) GUCs. goopg's planner is
	// rule/cost-based and never runs GEQO, so these are pure no-op
	// stubs — but psql tab-completion, ORMs, and tuning scripts issue
	// `SET geqo_threshold = ...` etc., and pg_settings tooling expects
	// the whole family present. Names, defaults, and ranges mirror
	// postgres/src/backend/utils/misc/guc_tables.c (QUERY_TUNING_GEQO,
	// all PGC_USERSET / GUC_EXPLAIN); numeric bounds come from
	// src/include/optimizer/geqo.h. M0122-0007.
	r.MustRegister(NewVariable(Variable{
		Name: "geqo", Type: TypeBool, BootVal: "on",
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "geqo_threshold", Type: TypeInt, BootVal: "12",
		MinVal: 2, MaxVal: 2147483647,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "geqo_effort", Type: TypeInt, BootVal: "5",
		MinVal: 1, MaxVal: 10,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "geqo_pool_size", Type: TypeInt, BootVal: "0",
		MinVal: 0, MaxVal: 2147483647,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "geqo_generations", Type: TypeInt, BootVal: "0",
		MinVal: 0, MaxVal: 2147483647,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "geqo_selection_bias", Type: TypeReal, BootVal: "2",
		MinVal: 1.5, MaxVal: 2,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "geqo_seed", Type: TypeReal, BootVal: "0",
		MinVal: 0, MaxVal: 1,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	// Other planner-tuning GUCs (QUERY_TUNING_OTHER). goopg's planner
	// ignores all three, but SET must succeed and the values surface in
	// pg_settings. constraint_exclusion is an enum {partition,on,off}
	// (default partition); cursor_tuple_fraction / recursive_worktable_factor
	// are reals. Mirrors guc_tables.c; recursive_worktable_factor's default
	// (10.0) is DEFAULT_RECURSIVE_WORKTABLE_FACTOR (cost.h),
	// cursor_tuple_fraction's (0.1) is DEFAULT_CURSOR_TUPLE_FRACTION
	// (planmain.h). M0122-0007.
	r.MustRegister(NewVariable(Variable{
		Name: "constraint_exclusion", Type: TypeEnum, BootVal: "partition",
		EnumOptions: []string{"partition", "on", "off"},
		Context:     ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "cursor_tuple_fraction", Type: TypeReal, BootVal: "0.1",
		MinVal: 0, MaxVal: 1,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "recursive_worktable_factor", Type: TypeReal, BootVal: "10",
		MinVal: 0.001, MaxVal: 1000000,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))

	// Additional planner cost/limit GUCs. v0 ignores them but registers
	// them so SET succeeds. M0097-0069.
	r.MustRegister(NewVariable(Variable{
		Name: "parallel_leader_participation", Type: TypeBool, BootVal: "on",
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "from_collapse_limit", Type: TypeInt, BootVal: "8",
		MinVal: 1, MaxVal: 2147483647,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "join_collapse_limit", Type: TypeInt, BootVal: "8",
		MinVal: 1, MaxVal: 2147483647,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "hash_mem_multiplier", Type: TypeReal, BootVal: "2.0",
		MinVal: 1, MaxVal: 1000,
		Context: ContextUserset, Scope: ScopeSession | ScopeTransaction,
		Flags: FlagExplain,
	}))

	// Time-bounded session GUCs commonly issued by JDBC / pgbouncer.
	r.MustRegister(NewVariable(Variable{
		Name: "lock_timeout", Type: TypeInt, Unit: UnitMs, BootVal: "0",
		MinVal: 0, MaxVal: 2147483647,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "idle_in_transaction_session_timeout", Type: TypeInt, Unit: UnitMs, BootVal: "0",
		MinVal: 0, MaxVal: 2147483647,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// deadlock_timeout: how long a backend waits on a lock before running the
	// deadlock detector. Superuser-set (PGC_SUSET), default 1s. goopg honours it
	// for LOCK TABLE waits on the transaction-scoped heavyweight lock manager
	// (executor.acquireRelLockTxn → lockmgr); the isolation deadlock specs set
	// it per session so the session with the shortest timeout deterministically
	// discovers the cycle. Mirrors guc_tables.c (M0118-0004).
	r.MustRegister(NewVariable(Variable{
		Name: "deadlock_timeout", Type: TypeInt, Unit: UnitMs, BootVal: "1000",
		MinVal: 1, MaxVal: 2147483647,
		Context: ContextSuset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// transaction_timeout (PG 17+). pg_dump's setup_connection disables it
	// (SET transaction_timeout = 0) alongside the other timeouts; goopg does
	// not enforce it, but SET must succeed. Mirrors guc_tables.c.
	r.MustRegister(NewVariable(Variable{
		Name: "transaction_timeout", Type: TypeInt, Unit: UnitMs, BootVal: "0",
		MinVal: 0, MaxVal: 2147483647,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// synchronize_seqscans. pg_dump's setup_connection turns this off (SET
	// synchronize_seqscans TO off) so a dump's row ordering is stable. goopg
	// has no synchronized-scan optimization, so the value is a no-op, but the
	// SET must succeed. Boot default mirrors guc_tables.c (on).
	r.MustRegister(NewVariable(Variable{
		Name: "synchronize_seqscans", Type: TypeBool, BootVal: "on",
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// row_security. pg_dump's setup_connection sets this (off unless
	// --enable-row-security). goopg has no row-level security, so it is a
	// no-op, but the SET must succeed. Boot default mirrors guc_tables.c (on).
	r.MustRegister(NewVariable(Variable{
		Name: "row_security", Type: TypeBool, BootVal: "on",
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	// xmloption. Every pg_dump archive opens with `SET xmloption = content;`
	// (dumpDatabase's standard preamble, unconditional — not gated on the
	// dump containing any xml columns), so a round-trip restore hits this
	// before touching any user object. goopg's xml codec always parses/
	// serializes as content fragments regardless of the setting (no
	// document-vs-content XML parsing distinction is implemented), so this
	// is a no-op registration, but SET/SHOW must succeed. Mirrors
	// guc_tables.c's "xmloption" entry (PGC_USERSET, enum content/document,
	// default content).
	r.MustRegister(NewVariable(Variable{
		Name: "xmloption", Type: TypeEnum, BootVal: "content",
		EnumOptions: []string{"content", "document"},
		Context:     ContextUserset,
		Scope:       ScopeSession | ScopeTransaction,
	}))

	// Logging GUCs. Stubs so `SET log_statement = 'all'` and
	// related psql / connection-pool wrappers don't trip.
	r.MustRegister(NewVariable(Variable{
		Name: "log_statement", Type: TypeEnum, BootVal: "none",
		EnumOptions: []string{"none", "ddl", "mod", "all"},
		Context:     ContextSuset,
		Scope:       ScopeSession,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "log_min_duration_statement", Type: TypeInt, Unit: UnitMs, BootVal: "-1",
		MinVal: -1, MaxVal: 2147483647,
		Context: ContextSuset,
		Scope:   ScopeSession,
	}))
	// log_line_prefix: PGC_SIGHUP in upstream (guc_tables.c) — config-file
	// only, never client-SET-able, unlike log_statement/log_min_duration_statement
	// above. goopg's statement/duration log lines
	// (internal/server/statement_log.go) expand the %m/%p/%u/%d/%a/%x subset
	// of PG's escape set against it; BootVal mirrors upstream's own default.
	r.MustRegister(NewVariable(Variable{
		Name: "log_line_prefix", Type: TypeString, BootVal: "%m [%p] ",
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))

	// default_statistics_target is read by ANALYZE clients to
	// gauge sample sizes; pgAdmin / DBeaver inspect it on
	// connection. v0's ANALYZE doesn't honour it (full-table
	// scan), but registering avoids the "unrecognized parameter"
	// failure on those tools.
	r.MustRegister(NewVariable(Variable{
		Name: "default_statistics_target", Type: TypeInt, BootVal: "100",
		MinVal: 1, MaxVal: 10000,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))

	// Streaming-replication GUCs (milestone 0005). Names, units,
	// ranges, and defaults mirror upstream's
	// postgres/src/backend/utils/misc/guc_tables.c entries.

	// data_directory_mode is reported by upstream as the permission
	// mask of the data directory; pg_basebackup issues `SHOW
	// data_directory_mode` early in its replication handshake to
	// preserve permissions on the cloned cluster. v0 always uses
	// 0700 (group read/write disallowed); see upstream guc_tables.c
	// "data_directory_mode" entry.
	// wal_segment_size reports the WAL segment size the primary
	// writes. pg_basebackup issues `SHOW wal_segment_size` to size
	// its WAL streaming buffers and align the backup stream with
	// segment boundaries; upstream's parser (sscanf "%d%s") expects
	// the value to carry a unit suffix ("16MB"), so we report it as
	// a pre-formatted string. goopg v0 always uses 16 MiB segments
	// (wal.DefaultSegmentSize); see upstream guc_tables.c
	// "wal_segment_size".
	r.MustRegister(NewVariable(Variable{
		Name: "wal_segment_size", Type: TypeString, BootVal: "16MB",
		Context: ContextInternal,
		Flags:   FlagDisallowInFile,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "data_directory_mode", Type: TypeInt, BootVal: "448",
		MinVal:  0,
		MaxVal:  511,
		Context: ContextInternal,
		Flags:   FlagDisallowInFile,
		Scope:   ScopeServer,
	}))

	// summarize_wal toggles the WAL-summarizer background worker (PG17+).
	// pg_basebackup issues `SHOW summarize_wal` during option negotiation
	// to decide whether incremental backups are available; goopg v0 has
	// no summarizer so the value is always off. Full implementation
	// tracked in M0095-0002.
	r.MustRegister(NewVariable(Variable{
		Name: "summarize_wal", Type: TypeBool, BootVal: "off",
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))

	// Primary-side: how many concurrent replication connections /
	// slots / how long the walsender waits for client status
	// updates before giving up. v0 honours these by configuration
	// but doesn't yet enforce hard limits — the slot store is
	// unbounded, walsender count is unbounded.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_level", Type: TypeEnum, BootVal: "replica",
		EnumOptions: []string{"minimal", "replica", "logical"},
		Context:     ContextPostmaster,
		Scope:       ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_wal_senders", Type: TypeInt, BootVal: "10",
		MinVal: 0, MaxVal: 262143,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_replication_slots", Type: TypeInt, BootVal: "10",
		MinVal: 0, MaxVal: 262143,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "wal_sender_timeout", Type: TypeInt, Unit: UnitMs, BootVal: "60s",
		MinVal: 0, MaxVal: 1 << 30,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "max_slot_wal_keep_size", Type: TypeInt, Unit: UnitMB, BootVal: "-1",
		MinVal: -1, MaxVal: 2147483647,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))

	// Standby-side: the consumer-side configuration. cmd/goopg
	// start reads these when `<DataDir>/standby.signal` is
	// present, dialing the named primary and acquiring the named
	// slot before the walreceiver Run loop begins.
	r.MustRegister(NewVariable(Variable{
		Name: "primary_conninfo", Type: TypeString, BootVal: "",
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "primary_slot_name", Type: TypeString, BootVal: "",
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "wal_receiver_status_interval", Type: TypeInt, Unit: UnitS, BootVal: "10",
		MinVal: 0, MaxVal: 2147483,
		Context: ContextSigHup,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "recovery_target_timeline", Type: TypeString, BootVal: "latest",
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "hot_standby", Type: TypeBool, BootVal: "on",
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	return r
}
