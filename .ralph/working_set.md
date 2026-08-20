Task: M0134-0045 (misc.sql, status `failed`) — diagnosis COMPLETE, no fix
landed (PARKED). Root cause is a real storage-engine design gap requiring its
own design pass before implementation, not a single-file-scoped fix.

Files this loop: `.ralph/deferral_ledger.md` (new row, M0134-0045, full
diagnosis + ruled-out candidates), `.ralph/fix_plan.md` (M0134-0045 entry
rewritten to PARKED with the same diagnosis, points next selection at
M0134-0046), `docs/design/0130-0011-nbtree-pg-on-disk-format.md` ("Known
follow-up: dedup does not build posting tuples" section appended, no README.md
index change needed — this is a follow-up note on an existing doc, not a new
landed slice).

Key symbols: `internal/access/nbtree/btree.go` — `dedupConsolidate` (~3551,
the actual bug: only drops byte-identical dupes, never builds posting
tuples despite its name/comment), `pageItems` (~2171, unconditionally expands
on-page postings into individual items before any split/dedup-recovery
decision), `insertIntoBlock` (~2654-2965, split path + dedup-recovery
no-split rewrite at ~2847), `byteAwareSplitLoc` (~3632, RULED OUT as
sufficient alone — see Hypothesis). `internal/access/nbtree/pgitemcodec.go`
`itemEncodedSize`/`bodySize` (~242-263, the correct per-format on-page-cost
function `byteAwareSplitLoc` doesn't use). `internal/executor/
pgindex_keydesc.go:264-266` (`buildPGIndexKeyDesc` refuses explicit
opclasses — why onek's indexes stay blob-format despite `pgIndexTupleKeys=
true`).

Hypothesis/Findings (final, this loop): `misc.sql` fails on ONE server crash
(`storage: not enough free space in page` in the nbtree split path from
`UPDATE onek SET unique1 = unique1 - 1;`), not a diff-mismatch case — ~340 of
361 diff lines are fallout from the dropped connection. THREE rounds of
investigation were needed to locate the real cause because the first two
worked from a stale premise (a comment at `pgitemcodec.go:33` claiming "every
production descriptor is nil" — actually `pgIndexTupleKeys=true` by default
since the S11.4 flip, though onek's indexes still land on blob format for an
unrelated reason: explicit opclasses, which `buildPGIndexKeyDesc` refuses).
CONFIRMED root cause (live instrumentation, implementer round): a page
holding compact posting-tuple content gets unconditionally unpacked into
individual `(key,TID)` items by `pageItems` before any split decision, and
`dedupConsolidate` — despite its name/comment — never repacks them into
postings, only drops exact duplicates. The expanded form's real combined
size (1098 items × 20B = 21960B, confirmed via instrumentation) legitimately
exceeds two fresh ~8160B pages even though the original packed form fit one.
RULED OUT: (A) blob-format `truncateSeparator` missing a duplicate-key
heap-TID tiebreaker — unreached, panic fires before separator construction;
(B) `byteAwareSplitLoc`'s hardcoded cost formula vs. real `itemEncodedSize` —
genuine metric mismatch but empirically insufficient alone (implementer
swapped the formula, panic reproduced identically — uniform item width means
the split POINT doesn't move, only the reported margin does; the real
blocker is aggregate capacity, not split-point balance).

Next step: this needs a proper design pass (posting-tuple construction
mirroring PG oracle `postgres/src/backend/access/nbtree/nbtdedup.c`'s
`_bt_dedup_pass`, including its posting-list byte-size cap) before an
implementer can be briefed for the actual fix — too large/risky for this
loop's ONE-task budget on top of the diagnosis work already done. Per the
fix_plan entry, the recommended next loop action is to select **M0134-0046
(misc_functions.sql)** instead of re-attempting misc.sql immediately (size it
via `scripts/pg-regress-runner.sh --verbose misc_functions`, delegate to
researcher), since misc.sql cannot progress past 0/1 until the nbtree design
work lands — that design work deserves its own dedicated task/loop, likely
worth filing as its own root-XXXX item given the blast radius (may also block
other bulk-UPDATE-heavy regress files sharing duplicate-key btree churn —
worth a grep sweep of other parked M0134 crash logs for the same panic
string before scoping).

Gates run this loop: `go build ./...` PASS (implementer round);
`go test ./internal/access/nbtree/...` (package scope, no `-count=1`) PASS;
`scripts/pg-regress-runner.sh --verbose misc` reproduced the crash reliably
(diagnostic use, not a pass/fail gate this loop — no fix landed). No
regress/precommit gate run since nothing was committed to `internal/`.
`make ralph-state-guard` — run at end of loop, see status block for result.

Delegation: researcher agent `a714b8169f952329c` — 2 rounds complete (initial
sizing, then a corrected-premise standalone-repro request whose OWN premise
turned out stale too — see Hypothesis above). implementer agent
`a73459b1a8cd3089d` — 1 round complete, NEEDS-DECISION verdict, live
instrumentation + empirical fix-candidate falsification, all temporary
instrumentation confirmed reverted (`git status --short internal/` clean).
Handoff dir `tmp/ralph-handoffs/M0134-0045-nbtree-split-panic-repro/brief.md`
(report.md write was blocked by a tool policy; full report captured in this
file's Hypothesis section and the ledger row instead).

In-flight: none. No server left running. Tree clean except the bookkeeping
files listed above, pending this loop's commit.
