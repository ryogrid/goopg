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

	return r
}
