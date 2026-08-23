(idle — nothing in flight)

Task just completed: M0134-0094 (box.sql) — sized live against the PG 18.3
oracle (scripts/pg-regress-runner.sh --verbose box): PARKED (768 → 738
diff lines, 62 → 45 ^ERROR mismatches). box was a raw-varlena text
pass-through with zero validation (badly-formatted inputs silently
accepted, no corner canonicalization, `box '...'` typed-literal syntax
unparseable). Fixed with parseBoxLiteral/boxCanonicalText
(internal/executor/expr.go, reproduces box_in/box_out via
path_decode/pair_decode from postgres/src/backend/utils/adt/geo_ops.c)
wired into all three shared entry points: coerceTextLikeDatum (codec.go,
column coercion), evalTypedStringLit + tryTypedLiteral allowlist (typed
literal `box '(x,y),(x,y)'`), and pg_input_is_valid('...','box'). Tests:
internal/executor/box_literal_test.go. Design doc:
docs/design/m0134-0094-box-literal-validation-and-canonicalization.md.
Deferral ledger row appended (.ralph/deferral_ledger.md, M0134-0094):
area() unregistered, box comparison operators fall through generic
lexicographic string compare instead of PG's area-based semantics
(silent-wrong-answer risk, worth flagging to a future loop), the
&</&>/<<|/~=/<-> operator family mostly unlexed, SP-GiST/GiST index
support for box columns entirely absent — each independently large,
none attempted.

Committed 5f8a7b7d and pushed to origin/regress-renumbering (confirmed).
fix_plan.md M0134-0094 marked [x] (PARKED convention).

Nightly filing: checked ci/logs/action-items.md — same run
(20260824-013441, sha e7495e712dda) as prior loops, already filed
(AI-...-001 recursiveUnionOp flake, AI-...-002 AdvisoryLock repeat). No
new filing needed this loop (confirmed no delta in the file before this
loop's edits).

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0095 (brin.sql,
status not-tried)**. First: `git log --oneline -1 origin/regress-renumbering`
to confirm 5f8a7b7d landed. Then size brin.sql live against ./postgres
oracle via scripts/pg-regress-runner.sh --verbose brin (background,
generous timeout; rm -rf tmp/regress-goopg-data tmp/regress-diffs first
if a prior run left them non-empty — this loop's box.sql runs left both
behind twice, cleaned both times). CAUTION carried forward: watch
`ps -o rss= -C goopg` while any regress file runs; kill -KILL promptly
(never bare pkill -f) if RSS climbs unbounded — box.sql itself stayed
under 600MB and finished in ~2 min both runs. Geometry-family note (from
prior loop, still relevant for a LATER file): if a future geometry file
(circle.sql, lseg.sql, path.sql, polygon.sql) is selected, the
box.sql fix (parseBoxLiteral/boxCanonicalText pattern) may generalize —
worth checking for reuse rather than re-deriving from scratch. If
brin.sql resolves clean (like async.sql/bitmapops.sql) or with a
small/contained diff, land accordingly in one loop (CLOSE if clean/no-op,
PARK+ledger+design-doc if a contained partial fix, per the established
M0134 per-task pattern).

Gates run: RALPH_PRECOMMIT_SCOPE=units ralph-precommit-test.sh PASS (full
suite, ~9 min, internal/initdb 445s the long pole as usual); make
check-testport-inventory PASS; make regen-testport ran clean; make
ralph-state-guard PASS (self-repaired a stale progress.json
completed-marker, same recurring pattern as prior loops — this is now a
standing benign artifact, not a bug to chase); pre-commit hook's pgbench
smoke PASS (336/617/12423 TPS across the 3 pgbench transaction types).
go build ./... clean; go test -run
'TestBoxLiteralParseValidation|TestBoxColumnCoercionCanonicalizes'
./internal/executor/ PASS.

In-flight: none. (tmp/regress-goopg-data and tmp/regress-diffs from both
box.sql oracle runs were rm -rf'd this loop.)
