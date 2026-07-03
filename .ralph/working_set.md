Task: M0119-0004-ACLHEAP — continue item (3) (`compatNoopCommandTag`
extended-protocol parity). Loop #87 audited/corrected loop #86's follow-up
plan and landed it (commit 1c1aee2e, pushed). Item (3) is now effectively
closed for GRANT/REVOKE/COMMENT ON/SECURITY LABEL — no further per-sub-form
tests are needed (3 of 4 branches are provably unreachable via a single
statement; COMMENT ON's one reachable case is now tested).

Files: internal/server/dispatch_extended_ddl_test.go (2 new tests: Unreachable
+ CommentOnMalformed); docs/design/0119-0004-database-config-set-pgdump.md
("Correction (loop #87)" section); docs/design/README.md (`0119-0004cy` row);
.ralph/fix_plan.md + .ralph/deferral_ledger.md (loop #87 entries).

Key symbols: internal/server/dispatch.go `compatNoopCommandTag` (~1274);
internal/parser/parser.go `case "grant","revoke"` (~1046), `case "security"`
(~1176), `parseCommentOnTail` (~2858).

Findings (do not re-derive): GRANT/REVOKE/SECURITY LABEL always parse
successfully for any single statement (no error path in their dispatch
arms) — compatNoopCommandTag/tryCompatNoopExtended are dead code for them.
Only a malformed COMMENT ON <supported-ObjKind> (e.g. missing name) reaches
the fallback.

Next step (NEW, higher-value than the closed item (3)): fix the
multi-statement-batch masking bug discovered this loop (deferral ledger
row, loop #87, `.ralph/deferral_ledger.md` last entry) — a simple-query
batch like "GRANT SELECT ON t TO x; <garbage>;" makes parser.Parse fail for
the WHOLE batch, and compatNoopCommandTag then matches the raw multi-stmt
text's leading "grant " prefix, silently absorbing the entire batch as a
bare CommandComplete success (swallowing the real syntax error, executing
neither statement) instead of real PG's 42601. Not specific to GRANT —
applies to all ~12 compatNoopCommandTag prefixes. Resume point: before the
prefix match in dispatch.go (~180) and dispatch_extended.go's
tryCompatNoopExtended (~394-397), detect multi-statement input (mirror
parser.Parse's own `;`-statement splitter, or re-parse just the first
statement in isolation) and raise the real syntax error instead of
absorbing. Reuse/extend the `splitLeadingRoleDDL` (M0118-0008) precedent
rather than reinventing. Needs a regression test.

Alternative next step if that's judged too large for one loop: continue
down the M0119-0004-ACLHEAP fix_plan/ledger backlog per the "Current
Priority" banner (M0110/M0119 spinoffs), or M0120-0002 (WordPress
verification) if a human has run the DROP TABLE ... CASCADE + reseed
commands from that ledger row (still blocked otherwise — needs human
authorization for a destructive op on shared test infra).

Gates run this loop: go build ./... clean; go vet ./... clean;
go test ./internal/server/... PASS (full package); scripts/tpch-spotcheck.sh
PASS (Q12=2/Q13=33); make ralph-state-guard OK (self-repaired a stale
completed-marker); pgbench smoke (pre-commit hook) PASS, 0 failed
transactions across TPC-B/simple-update/select-only.
