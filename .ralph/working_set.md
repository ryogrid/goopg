Task: M0129-S10 DONE — ExecError.Pos → FieldPosition wired to wire protocol

Files:
- internal/server/copy.go: execErrDetailFields now extracts FieldPosition when
  ee.Pos > 0 (0-based→1-based). New newExtendedQueryError helper builds
  extendedQueryError with Position from ExecError.
- internal/server/dispatch.go: 2 evalExecuteParams ad-hoc *ExecError handlers now
  use execErrCode/execErrMsg/execErrDetailFields. 2 materializeCursor callers
  now include execErrDetailFields. Deferred FK commit error block now emits
  FieldPosition.
- internal/server/dispatch_extended.go: 4 call sites use newExtendedQueryError
  instead of bare execErrCode/execErrMsg.
- internal/testport/framework/regress.go: LINE/^ and standalone-caret stripping
  removed from NormalizeRegressOutput (was step 5).

Key symbols:
- execErrDetailFields: now includes FieldPosition (protocol.FieldPosition)
- newExtendedQueryError: builds extendedQueryError with Detail+Position from ExecError
- NormalizeRegressOutput: no longer strips LINE/^ position lines

Hypothesis/Findings:
- Every ExecError site that carries Pos>0 now surfaces through FieldPosition.
  Sites with Pos==0 (unset) remain as before.
- psql shows LINE/^ only when the server sends FieldPosition AND the query text
  is in -f mode (file input). In -c mode, even PG with FieldPosition doesn't
  always show LINE/^.
- Many executor error sites still use Pos:0 — a per-site audit is needed to
  stamp real positions, but the wire infrastructure is complete.
- RegressSuite passes (all deferred as before); normalization change doesn't
  regress any ported tests.

Next step: M0129-S4.1 — measurement (per the implementation order S1→S2→S3→S6→S10→S4.1→S4.2→S5.1–S5.8).

Gates run:
- go build ./...: PASS
- server unit tests: PASS
- pre-commit (units): PASS
- tpch-spotcheck: PASS (Q12=2 Q13=35, 27.7s)
- pgbench smoke: PASS (412K txns, 0 failed)
- framework tests (incl NormalizeRegressOutput): PASS
- RegressSuite: PASS (all deferred, no regressions)

In-flight: none
