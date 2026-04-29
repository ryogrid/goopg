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

	// wal_direct_io requests Linux O_DIRECT writes for WAL
	// segments so newly-written WAL bytes don't pollute the OS
	// page cache. Default `off` keeps the legacy buffered path.
	// On filesystems / kernels that don't honour O_DIRECT
	// (tmpfs, overlayfs, every non-Linux GOOS) the writer
	// transparently falls back to buffered writes and logs
	// `event=wal_direct_io_fallback` at startup. See
	// docs/design/0010-0001-wal-direct-io-write-path.md.
	r.MustRegister(NewVariable(Variable{
		Name: "wal_direct_io", Type: TypeBool, BootVal: "off",
		Context: ContextPostmaster,
		Scope:   ScopeServer,
	}))

	// wal_sender_memory_buffer sizes (in bytes) the in-memory
	// ring of recent WAL bytes used by walsender's
	// RecordIterator. 0 disables the ring; >0 mirrors every
	// successful WAL write so senders can stream without
	// disk reads (especially valuable when wal_direct_io=on
	// keeps recent WAL out of the OS page cache). Default
	// 16 MiB matches the M0010 milestone default. See
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
		Name: "work_mem", Type: TypeInt, Unit: UnitKB, BootVal: "4MB",
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

	// Planner toggle GUCs. Upstream uses them for testing (`SET
	// enable_seqscan = off` to force an index plan). v0's planner
	// ignores them — the rule-based decisions still apply — but
	// the SET succeeds so test scripts don't trip.
	for _, name := range []string{
		"enable_seqscan", "enable_indexscan", "enable_indexonlyscan",
		"enable_bitmapscan", "enable_hashjoin", "enable_mergejoin",
		"enable_nestloop", "enable_sort", "enable_hashagg",
		"enable_material", "enable_partition_pruning",
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
