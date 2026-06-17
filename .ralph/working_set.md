(idle — nothing in flight)

Last landed: DU-002 slice 163 (loop #130) — a `LANGUAGE plpgsql` function
(`public.plpg_inc(integer) RETURNS integer … AS $$ BEGIN RETURN $1 + 1; END; $$`)
now round-trips through pg_dump. REAL DIVERGENCE FIXED (sibling-path bug).

Root cause: the runtime pg_proc view resolves prolang by NAME via langNameToOIDStr,
which returned "0" for plpgsql (only internal/c/sql were mapped). pg_dump's dumpFunc
joins pg_proc→pg_language on l.oid=p.prolang (no lanispl filter) just to fetch lanname;
prolang=0 matched no pg_language row → join returned "0 rows instead of one" → the
ENTIRE dump aborted. Fix (2 sibling edits): (a) catalog.go appends a plpgsql row
{13627, plpgsql, owner 10, lanispl=f, lanpltrusted=t, handlers 0} — lanispl=f (like
internal/c/sql) keeps getProcLangs from emitting a spurious CREATE LANGUAGE while the
unfiltered dumpFunc join still resolves lanname; (b) pg_proc_view.go
langNameToOIDStr("plpgsql")="13627". Oracle (PG 18.3): plpgsql is pg_language OID 13627,
lanispl=t, but real pg_dump emits NO CREATE LANGUAGE (pinned via pg_depend). Body
rendered verbatim as prosrc (plpgsql NOT deparsed); $1 forces the $_$ tag.

Files: internal/catalog/catalog.go, internal/initdb/pg_proc_view.go,
internal/catalog/catalog_test.go (TestPgLanguageBuiltinRows → 4 rows),
internal/initdb/pg_proc_view_test.go (TestPgProcViewRendersRoutine prolang → 13627),
internal/testport/pgdump_connsetup_test.go (fixture ~1724, assertions ~2406),
docs/design/0110-0001-pg-dump-tap-port.md (slice 163), .ralph/fix_plan.md (loop #130).
Verified: gofmt OK; go build ./internal/... OK; go vet clean;
TestPort_PgDumpConnectionSetup PASS (2.69s, not skipped); internal/catalog +
internal/initdb suites PASS; pgbench pre-commit smoke on commit.

Next direction (slice 164): remaining function-attribute cells are GENUINE feature
gaps. In likely order of value:
- composite/RECORD return type (RETURNS record / a named composite type) — prorettype handling
- TRANSFORM FOR TYPE (protrftypes always NULL — feature gap)
- RETURNS TABLE (goopg parser maps to OUT params, argmode 'o' not 't' — known divergence;
  pg_dump would render OUT params instead of RETURNS TABLE).
Note: STRICT / SECURITY DEFINER / LEAKPROOF / COST / IMMUTABLE / STABLE / VOLATILE / PARALLEL
SAFE/RESTRICTED / VARIADIC / DEFAULT / multi-statement / SETOF / ROWS / array return /
plpgsql language / procedures + OUT/INOUT are ALL covered (slices 149-163).
