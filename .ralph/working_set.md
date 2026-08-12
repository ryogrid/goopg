(idle — nothing in flight)

M0119-0006 58th slice landed: a bare `bpchar` is unbounded, and its blanks are
data. Committed and pushed.

Carry-forward #1 — **a resume point can be RIGHT and still incomplete.** Loops
#69/#70 learned that a deferral resume point is a hypothesis that may be wrong;
this one was correct as far as it went (gate the `n := 1` default on
`tname != "bpchar"`) and still missed half the defect. The same code arm also
TRIMS trailing blanks, which is only safe because the render boundaries re-pad
from `Args[0]` — an unbounded value has no width to re-pad FROM, so trimming
destroys data instead of deferring it. Read what the code around the resume
point does, not only what the row says to change.

Carry-forward #2 — **the row's stated precondition was worth the ten minutes.**
It demanded a check for whether any path canonicalises `character(N)` to
`bpchar` with `Args` intact. It DOES (the heap reload renames OID 1042), and the
gate is safe only because `pgTypeArgsFromTypmod` decodes the typmod back into
`Args`. Had the reload dropped it, the fix would have unbounded every restored
`char(N)` column. Answer preconditions by measuring, never by assuming.

Carry-forward #3 — the sibling audit paid again (3rd loop running).
`PadBpchar`, the `expr.go` cast path and the parser were all already correct;
the one that was NOT was `validateTypedLen`, `pg_input_error_info`'s private
copy of the same declared-length rule, still counting BYTES after the 57th
slice moved the codec path to runes. When a slice changes a RULE, grep for
other implementations of that rule, not only for callers of the function.

**Banner moved DURING this loop** (commit `29ff2e8d`): M0132 is now
next-after-M-NIGHTLY and M0131 is demoted. This slice was selected under the
older banner and is unaffected, but the next loop must re-read the banner and
will likely select **M0132** (S1 first — verification/characterisation, no
behaviour change), NOT another M0119 slice. Verify before selecting.

Remaining M0119-0006 bpchar residue if ever selected again: `octet_length` off
the trimmed image (needs builtin-argument type plumbing), the trimmed heap
image itself (a storage-format slice with its own gates), plus the two rows
this slice filed (`pg_input_error_info` returns 0 rows where PG returns one
all-NULL row; `validateTypedLen` matches its type by text prefix).

Gates: `go build ./...` clean; targeted `internal/executor` PASS;
`TestPort_RegressSuite` PASS (1045 s — needs explicit `-timeout 40m`, the
default 600 s panics with a goroutine dump that reads like a hang);
`RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh` RESULT=PASS
(Q12=2, Q13=35 canonical); TPC-DS SF0.5 sweep PASS=95 MISMATCH=0 CKMISMATCH=0
ERROR=0, plan shapes identical 99/99; pgbench smoke PASS via the commit hook.
Both new guards verified red on the pre-fix source (4 / 3 failing assertions).

In-flight: none.
