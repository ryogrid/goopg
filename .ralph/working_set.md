Task: M0119-0004 — pg_dump DU-002 round-trip probe: ALTER TEXT SEARCH DICTIONARY/CONFIGURATION OWNER TO + AddTSConfigMapping per-DB scoping

Files:
- internal/executor/operators_ddl.go: ALTER guard for "text search dictionary" and "text search configuration" cases in execCompatNoop; AddTSConfigMapping/DropTSConfigMapping/ReplaceTSConfigMappingDict callers pass NamespaceDBOid
- internal/catalog/catalog.go: AddTSConfigMapping/DropTSConfigMapping/ReplaceTSConfigMappingDict now take dbOid ...uint32 variadic param + filter by DBOid in the lookup loop

Key symbols:
- ddlOp.execCompatNoop: case "text search dictionary" and "text search configuration" now check strings.HasPrefix(s.Tag, "ALTER ") before requiring TSDictTemplate/TSConfigParser
- catalog.AddTSConfigMapping: new dbOid ...uint32 param, DBOid match added
- catalog.DropTSConfigMapping: new dbOid ...uint32 param, DBOid match added
- catalog.ReplaceTSConfigMappingDict: new dbOid ...uint32 param, DBOid match added

Hypothesis/Findings:
- Bug 1: execCompatNoop's "text search dictionary" case handled both CREATE and ALTER but always required TSDictTemplate.Name (set only for CREATE). ALTER TEXT SEARCH DICTIONARY OWNER TO produced "text search template is required".
- Bug 2: Same pattern in "text search configuration" — ALTER OWNER TO would hit "text search parser is required".
- Bug 3: AddTSConfigMapping/DropTSConfigMapping/ReplaceTSConfigMappingDict matched configs by NamespaceOID+Name only (no DBOid), so a restore into a different DB would find the source DB's config, report duplicate, and fail with "duplicate key value violates unique constraint pg_ts_config_map_index".
- All three FIXED. DU-002 round-trip probe advances past text search objects.
- Next blocker: ALTER SERVER OWNER TO — parser has no ALTER SERVER dispatch (falls through to ALTER TABLE path → syntax error). After that, likely more ALTER ... OWNER TO gaps for other compat-registry objects.

Next step:
Continue DU-002 blockers: add ALTER SERVER/FDW/EVENT TRIGGER OWNER TO compat no-op handlers in parser + executor, or add a generic ALTER <compat_object> OWNER TO catch-all to avoid per-type repetition.

Gates run:
- go build ./...: PASS
- go test ./internal/catalog/...: PASS (0.064s)
- go test ./internal/executor/...: PASS (5.848s)
- go test ./internal/parser/...: PASS (0.035s)
- make ralph-state-guard: OK
- TestPort_PgDumpConnectionSetup: PASS (DU-002 round-trip FAILS at next blocker: ALTER SERVER OWNER TO syntax error)

In-flight: none
