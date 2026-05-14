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

	// WAL & checkpointer GUCs (milestone 0002). Names, units, ranges,
	// and defaults mirror upstream's
	// postgres/src/backend/utils/misc/guc_tables.c entries.
	// PGC_SIGHUP -> ContextSigHup so a goopg reload (control-socket
	// RELOAD) will be able to pick them up once the reload path
	// observes the registry; today the reload is a no-op but the
	// gating is in place.
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

	// track_io_timing gates per-I/O activity wait-event hooks
	// (BufferPin / DataFileRead / Write / Extend / Sync / AIO).
	// Default `off` mirrors upstream PG. M0092-0005: when off,
	// the hooks are not installed at all, saving the per-Pin /
	// per-Read `runtime.Stack` LookupGoroutine call on the hot
	// read path. See
	// docs/design/0092-0005-lookup-goroutine-io-hooks-guc.md.
	r.MustRegister(NewVariable(Variable{
		Name: "track_io_timing", Type: TypeBool, BootVal: "off",
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

	// bgwriter_delay controls how often (in milliseconds) the background
	// writer goroutine wakes up to flush dirty buffer-pool pages.
	// Default 200ms mirrors upstream's bgwriter_delay GUC (M0048-0003).
	r.MustRegister(NewVariable(Variable{
		Name: "bgwriter_delay", Type: TypeInt, BootVal: "200",
		MinVal: 10, MaxVal: 10000,
		Context: ContextPostmaster,
		Scope:   ScopeServer,
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
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "min_parallel_table_scan_size", Type: TypeInt, Unit: UnitKB, BootVal: "8388608",
		MinVal: 0, MaxVal: 715827882,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "min_parallel_index_scan_size", Type: TypeInt, Unit: UnitKB, BootVal: "524288",
		MinVal: 0, MaxVal: 715827882,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
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
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "random_page_cost", Type: TypeReal, BootVal: "4.0",
		MinVal: 0, MaxVal: 1e9,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "effective_cache_size", Type: TypeInt, Unit: UnitKB, BootVal: "4GB",
		MinVal: 1, MaxVal: 1 << 40,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
	}))
	r.MustRegister(NewVariable(Variable{
		Name: "search_path", Type: TypeString, BootVal: `"$user", public`,
		Context: ContextUserset,
		Scope:   ScopeSession | ScopeTransaction,
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

	// Planner toggle GUCs. Upstream uses them for testing (`SET
	// enable_seqscan = off` to force an index plan). v0's planner
	// ignores them — the rule-based decisions still apply — but
	// the SET succeeds so test scripts don't trip.
	for _, name := range []string{
		"enable_seqscan", "enable_indexscan", "enable_indexonlyscan",
		"enable_bitmapscan", "enable_hashjoin", "enable_mergejoin",
		"enable_nestloop", "enable_sort", "enable_hashagg",
		"enable_material", "enable_partition_pruning",
		// M0054-0006: nested-loop index join rollback switch.
		// `off` keeps the legacy Hash plan for joins that the
		// rule would otherwise rewrite.
		"enable_nestloop_index",
	} {
		r.MustRegister(NewVariable(Variable{
			Name: name, Type: TypeBool, BootVal: "on",
			Context: ContextUserset,
			Scope:   ScopeSession | ScopeTransaction,
		}))
	}

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
		MinVal: 0, MaxVal: 1<<30,
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
