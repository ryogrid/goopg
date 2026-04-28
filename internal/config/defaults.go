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

	return r
}
