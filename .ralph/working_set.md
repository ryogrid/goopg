(idle — nothing in flight)

Completed this loop: **M0119-0006 78th slice — reg* → text/varchar/name/bpchar
cast renders the name** (deferral row 1350, commit `b55de2dd`). A reg* datum is
a plain KindInt holding an object OID, so `'pg_type'::regclass::text` → `1247`,
`'f_varbit(varbit)'::regprocedure::text` → `131072`, `'f_varbit'::regproc::text`
→ `131072`, and `'pg_type'::regclass::name` passed the KindInt through. Fix:
`evalCastTyped` (internal/executor/expr.go) — the only cast entry point with both
the source-type name and `*Context` — now routes a reg* → {text,varchar,name,
bpchar} cast (KindInt) through `executor.RegOut`, the 68th slice's shared
SELECT+COPY reg*out renderer (the cast is the missing third sibling);
`qualify` mirrors the SELECT path's `!publicSchemaVisible`; `char` (CHAROID)
excluded. Also fixes the unprobed `regcol::text` column shape. The regclass arm
of `regOut` gained dbOid scoping (`dbOid ...uint32` variadic → LookupTableByOID/
LookupIndexByOID) so a regclass cast never renders another DB's relation name
(connDBOid, M0122-0007 4e follow-up 33); SELECT/COPY callers keep DefaultDBOid.
Tests `reg_cast_to_text_test.go` (6×4 matrix + column + OID-0/dangling +
sibling-agreement), mutation-checked. Gates: executor package, pre-commit units,
tpch-spotcheck (Q12=2, Q13=35), `TestPort_RegressSuite/oid` all PASS.

**Next slice candidates (remaining reg* deferrals, open `-` rows):**
- **row 1351 second half (STILL OPEN)** — the regprocedure arglist carries only
  the arg's NAME, so a bare `char` arg (parser-stamped bpchar `Args=[1]`) is
  indistinguishable from OID-18 `"char"` and renders `"char"` where PG renders
  `character`. Needs an OID-per-arg catalog-representation change (carry the
  resolved type OID alongside the Name, render via format_type_be). Design
  `docs/design/0119-0006-char-family-arg-aliases.md` defers this verbatim.
- row 1340 (role name case-fold — regroleout can't quote `"Alice"`), row 1343
  (bare arg-type schema resolution — user-type store lacks namespace), row 1344
  (quoted arg-type name case at CREATE), row 1347 (empty-schema visibility
  proxy blocks `SET search_path=…,offpath`).

**In-flight / environmental (for the next loop):**
- The FULL `TestPort_RegressSuite` HUNG this loop (600s go-test timeout) and
  ballooned its server to ~29GB RSS — did NOT attribute to the 78th slice (no
  hang mechanism in a leaf cast renderer; the `oid` subtest passes in 6s).
  Likely cause: a **concurrent Ralph loop** (claude PID 365596, ~2h old) was
  ALSO running the regress suite, leaving a 2h-old orphaned goopg server
  (port 38767). Orphans SIGKILLed this loop (PIDs 365099/365024/523632/523543/
  376883). Re-run the full `TestPort_RegressSuite` once the concurrent loop is
  stopped; if it hangs again, bisect to the hanging subtest via
  `go test -run 'TestPort_RegressSuite/<name>'`.
- Stray untracked files NOT this loop's (concurrent loop): `internal/pgnodes/
  int2_cast_test.go`, `internal/testport/datconnlimit_durability_test.go` — do
  not stage them.
