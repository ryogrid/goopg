(idle — nothing in flight)

Loop #46 COMPLETE and committed (pending push): M0119-0004 DU-002 slice 404
follow-up — closed the "expose builtin pg_proc rows queryably" resume point
from loop #45, using shape (b) (catalog-level lookup, not a full 3397-row
port). New `catalog.BuiltinProc`/`LookupBuiltinProc`/`BuiltinProcs()`
(internal/catalog/catalog.go, leaf package both initdb and executor already
import cycle-free) hand-curates int4recv (OID 2406) + prsd_lextype (OID
3721) from real pg_proc.dat values. Wired into (1) `resolveTransformFunc`
(internal/executor/operators_ddl.go) as a fallback after user-routine
lookup, validated via new `validateBuiltinTransformFunc`; (2)
`registerPgProcView`'s VirtualRows (internal/initdb/pg_proc_view.go),
appended AFTER user routines so existing `rows[len(builtinProcs):]` test
slicing stays correct — only exact-count assertions needed
`+= len(catalog.BuiltinProcs())`. A SECOND independent gap surfaced and was
fixed: `formatTypeOID` (internal/executor/expr.go) had no case for OID 2281
(`internal` pseudo-type) — pg_dump's server-side `format_type('2281'::oid,
NULL)` catalog query rendered `(???)` instead of `(internal)`; fixed with
`case 2281: return "internal"` (matches the existing `record`/2249
precedent). The `'CREATE TRANSFORM FOR int'` DU-002 connsetup fixture
(exact upstream 002_pg_dump.pl SQL) is now wired into
TestPort_PgDumpConnectionSetup and asserts byte-identical vs real pg_dump
18.3 — verified live on a running server AND via the Go test.

Tests added: TestLookupBuiltinProc/TestBuiltinProcs (catalog, new file
builtin_proc_test.go), TestPgProcViewBuiltinTransformFuncs (initdb), 5 new
TestResolveTransformFunc subtests (curated-builtin resolve/reject/
schema-qualification), TestFormatTypeOIDInternalPseudoType (executor, new
file format_type_pseudo_test.go). Also fixed 3 pre-existing pg_proc_view_test.go
assertions whose row-count/index math assumed only `len(builtinProcs)` rows
existed (TestPgProcViewEmptyByDefault/RendersRoutine/Ordering) and rewrote
TestPgProcViewProparallel's blanket "every row is u" assertion (now
correctly split: legacy stubs/user routines = u, new curated builtins = s
per pg_proc.h's real BKI_DEFAULT(s)).

Gates this loop: full catalog/executor/initdb/parser suites PASS; go vet
clean; gofmt -d confirms none of my new code needs reformatting (pre-existing
go1.25-vs-1.26 noise only, untouched); TPC-H spotcheck Q12=2/Q13=33 PASS;
TestPort_PgDumpConnectionSetup (whole ~10k-line suite) PASS; a broader
`go test ./internal/...` sweep was kicked off at loop-end to catch any
unrelated regression (check /tmp/claude-*/tasks/b2i422p8b.output or rerun
if resuming mid-check) — check this FIRST if resuming. pgbench smoke runs at
commit time via the pre-commit hook (not run separately this loop yet).
make ralph-state-guard: self-repaired a stale status/progress marker (same
recurring pattern as loops #44/#45), OK after repair.

Design doc updated: docs/design/0110-0001-pg-dump-tap-port.md, new
"Slice 404 follow-up" subsection right after the original Slice 404 section.
Ledger: slice-404 row flipped to `resolved`; new slice-404-follow-up row
appended with the remaining CAST/CONVERSION wiring + restart-persistence
deferrals. fix_plan.md: appended a loop #46 note under the M0119-0004
CREATE TRANSFORM item.

Next loop candidates (M0119-0004 pg_dump slices, DU-002):
- Trivial follow-up (now unblocked): wire CAST's WITH FUNCTION arm
  (operators_ddl.go ~line 12977) and resolveConversionFunc (~line 12735) to
  also try catalog.LookupBuiltinProc as a fallback, IF a future fixture ever
  references a builtin function through either path (none does yet — don't
  do this speculatively without a forcing fixture).
- Restart-persistence sweep: WAL-log CreateConversion/CreateCast/
  CreateCollation/CreateTransform + replay on startup like CREATE SCHEMA —
  closes the recurring (c)/(b) deferral shared by slices 389-404.
- Re-scan deferral_ledger.md for other open "| - |" rows for a bounded
  slice if the above two are too large for one loop.
- Whichever is picked: update fix_plan.md AND the design doc in the SAME
  loop (this loop did both — keep it up).
