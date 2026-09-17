#!/usr/bin/env bash
# ralph-githooks-test.sh — self-test for the RALPH_LOOP=1 arms of
# .githooks/pre-commit and .githooks/commit-msg, run in a throwaway git repo
# (the pgbench smoke is stubbed there; nothing touches this repository).
# Usage: scripts/ralph-githooks-test.sh   (exit 0 = all cases pass)
set -uo pipefail

SRC="${RALPH_HOOKS_SRC:-$(cd "$(dirname "$0")/.." && pwd)}"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
pass=0 fail=0

cd "$T" || exit 1
git init -q .
git config user.name tester; git config user.email t@example.invalid
git config commit.gpgsign false
mkdir -p .githooks scripts/lib .ralph .claude internal/optimizer internal/executor internal/catalog internal/testport cmd/goopg docs tmp/gate-stamps ci/logs
cp "$SRC/.githooks/pre-commit" "$SRC/.githooks/commit-msg" .githooks/
cp "$SRC/scripts/ralph_protected_regions.py" "$SRC/scripts/ralph-lineage-guard.py" scripts/
printf '#!/usr/bin/env bash\nexit 0\n' > scripts/ralph-precommit-test.sh
chmod +x .githooks/* scripts/*
git config core.hooksPath .githooks

cat > AGENT.md <<'EOF'
# AGENT
<!-- PLAN-PARITY-HARNESS:BEGIN -->
rule: reference clusters are read-only
<!-- PLAN-PARITY-HARNESS:END -->
status: old
EOF
printf 'owner rules\n' > CLAUDE.md
cat > .ralph/fix_plan.md <<'EOF'
# plan
## Current Priority
1. P0 first
## M0142
- [!] **M0142-0008c — frozen** FROZEN pending owner. Parent: none
- [ ] **M0143-0900 — trace the candidate pool** Parent: none
  Kind: recon
- [ ] **M0143-0901 — add the trace blocks** Parent: none
  Kind: impl
EOF
# .ralph/gate-exceptions.md — owner-maintained; seeded with ZERO active rows,
# exactly like the real file. Individual cases rewrite it as a fixture.
exc_table() { # <row...>  (each row already pipe-delimited)
  { printf '# Gate exceptions (OWNER-MAINTAINED)\n\n'
    printf '| task-id | gate | max-commits | expires | reason |\n'
    printf '| --- | --- | --- | --- | --- |\n'
    for r in "$@"; do printf '%s\n' "$r"; done
  } > .ralph/gate-exceptions.md
}
exc_table
printf '#!/usr/bin/env bash\n' > scripts/lib/gate-stamp.sh
printf '{}\n' > .claude/settings.json
printf 'prompt\n' > .ralph/PROMPT.md
cat > .ralph/deferral_ledger.md <<'EOF'
# Deferral Ledger

| status | date | task-id | landed | deferred | resume point | why |
| - | 2026-09-07 | take3-D-10 | a | b | c | d |
| - | 2026-09-17 | csq-R2 | OWNER DECISION (Q3c): chain FROZEN | x | y | z |
EOF
printf 'package testport\n' > internal/testport/h.go
printf 'package optimizer\n' > internal/optimizer/cost.go
printf 'package optimizer\n' > internal/optimizer/join.go
printf 'package executor\n' > internal/executor/scan.go
printf 'package catalog\n' > internal/catalog/x.go
printf 'package main\n' > cmd/goopg/main.go
printf 'd\n' > docs/a.md
GOOPG_SKIP_PRECOMMIT=0 git add -A && git commit -qm "init" >/dev/null 2>&1 || { echo "setup commit failed"; exit 1; }

expect() { # <want ok|reject> <name> <msg> <env...> -- (files already staged)
  local want="$1" name="$2" msg="$3"; shift 3
  local got out
  if out="$(env "$@" git commit -q -m "$msg" 2>&1)"; then got=ok; else got=reject; fi
  if [ "$got" = "$want" ]; then pass=$((pass + 1))
  else fail=$((fail + 1)); printf 'FAIL %s: want=%s got=%s\n%s\n' "$name" "$want" "$got" "$out"; fi
  git reset -q --soft HEAD 2>/dev/null
  if [ "$got" = ok ]; then git reset -q --soft HEAD~1; fi
  git reset -q        # unstage everything
  git checkout -q -- . 2>/dev/null
}
code_tree() { git ls-files -s -- internal cmd go.mod go.sum | sha256sum | cut -d' ' -f1; }
stamp() { # <gate> <result> [code_tree]
  printf '{"gate":"%s","tree":"%s","code_tree":"%s","binary_sha256":"x","result":"%s","time":"2026-09-17T00:00:00Z"}\n' \
    "$1" "$(git write-tree)" "${3:-$(code_tree)}" "$2" > "tmp/gate-stamps/$1.json"
}

# --- pre-commit: protected regions ------------------------------------------
echo more >> CLAUDE.md; git add CLAUDE.md
expect reject "pre: CLAUDE.md edit" "docs: x" RALPH_LOOP=1
echo more >> CLAUDE.md; git add CLAUDE.md
expect ok "pre: CLAUDE.md edit outside loop" "docs: x" RALPH_LOOP=0
sed -i 's/read-only/writable/' AGENT.md; git add AGENT.md
expect reject "pre: AGENT harness edit" "docs: x" RALPH_LOOP=1
sed -i 's/status: old/status: new/' AGENT.md; git add AGENT.md
expect ok "pre: AGENT status edit outside markers" "docs: x" RALPH_LOOP=1
sed -i 's/P0 first/my pet task first/' .ralph/fix_plan.md; git add .ralph/fix_plan.md
expect reject "pre: banner edit" "ralph: x" RALPH_LOOP=1
# --- pre-commit: harness mechanism files (M5) --------------------------------
echo '# x' >> scripts/lib/gate-stamp.sh; git add scripts/lib/gate-stamp.sh
expect reject "pre: gate-stamp.sh edit" "scripts: x" RALPH_LOOP=1
echo '{"a":1}' > .claude/settings.json; git add .claude/settings.json
expect reject "pre: settings.json edit" "scripts: x" RALPH_LOOP=1
echo more >> .ralph/PROMPT.md; git add .ralph/PROMPT.md
expect reject "pre: PROMPT.md edit" "ralph: x" RALPH_LOOP=1
git rm -q --cached scripts/ralph-lineage-guard.py
expect reject "pre: guard script deletion staged" "scripts: x" RALPH_LOOP=1
echo '# x' >> scripts/lib/gate-stamp.sh; git add scripts/lib/gate-stamp.sh
expect ok "pre: gate-stamp.sh edit outside loop" "scripts: x" RALPH_LOOP=0
# --- pre-commit: deferral ledger append-only (M8) ----------------------------
printf '| - | 2026-09-18 | new-row | a | b | c | d |\n' >> .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect ok "pre: ledger append" "ralph: x" RALPH_LOOP=1
sed -i 's/| take3-D-10 | a |/| take3-D-10 | a2 |/' .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect reject "pre: ledger row modified" "ralph: x" RALPH_LOOP=1
sed -i 's/^| - | 2026-09-07 | take3-D-10.*//' .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect reject "pre: ledger row deleted" "ralph: x" RALPH_LOOP=1
sed -i 's/| - | 2026-09-17 | csq-R2 |/| resolved | 2026-09-17 | csq-R2 |/' .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect reject "pre: owner row status flipped" "ralph: x" RALPH_LOOP=1
printf '| - | 2026-09-18 | csq-R3 | OWNER DECISION: reopen | b | c | d |\n' >> .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect reject "pre: appended row claims OWNER DECISION" "ralph: x" RALPH_LOOP=1
printf '| - | 2026-09-18 | csq-R2 | superseded: chain reopened | b | c | d |\n' >> .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect reject "pre: appended row supersedes owner row id" "ralph: x" RALPH_LOOP=1
printf '| - | 2026-09-18 | take3-D-10 | follow-up | b | c | d |\n' >> .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect ok "pre: appended row re-using a non-owner id" "ralph: x" RALPH_LOOP=1
sed -i 's/| take3-D-10 | a |/| take3-D-10 | a2 |/' .ralph/deferral_ledger.md; git add .ralph/deferral_ledger.md
expect ok "pre: ledger edit outside loop" "ralph: x" RALPH_LOOP=0
# --- pre-commit: lineage guard ----------------------------------------------
printf -- '- [ ] **M0143-0100 — new**\n  body\n' >> .ralph/fix_plan.md; git add .ralph/fix_plan.md
expect reject "pre: new M0143 without Parent" "ralph: x" RALPH_LOOP=1
printf -- '- [ ] **M0143-0100 — new**\n  Parent: none\n' >> .ralph/fix_plan.md; git add .ralph/fix_plan.md
expect ok "pre: new M0143 with Parent" "ralph: x" RALPH_LOOP=1
sed -i 's/- \[!\] \*\*M0142-0008c/- [ ] **M0142-0008c/' .ralph/fix_plan.md; git add .ralph/fix_plan.md
expect reject "pre: unfreeze FROZEN" "ralph: x" RALPH_LOOP=1
# --- pre-commit: skip log ----------------------------------------------------
echo x >> docs/a.md; git add docs/a.md
expect ok "pre: GOOPG_SKIP_PRECOMMIT=1 logs" "docs: x" GOOPG_SKIP_PRECOMMIT=1 RALPH_LOOP=1
if grep -q 'RALPH_LOOP=1 user=tester' ci/logs/precommit-skips.log 2>/dev/null; then pass=$((pass + 1))
else fail=$((fail + 1)); echo "FAIL skip log line missing"; fi

# --- commit-msg: area label (H11) -------------------------------------------
echo x >> docs/a.md; git add docs/a.md
expect reject "msg: optimizer label on docs-only" "optimizer(M0141-S2b-3): recon — decomposed" RALPH_LOOP=1
echo x >> docs/a.md; git add docs/a.md
expect ok "msg: docs label on docs-only" "docs(M0141-S2b-3): recon — decomposed" RALPH_LOOP=1
echo x >> docs/a.md; git add docs/a.md
expect ok "msg: label rule off outside loop" "optimizer(x): y" RALPH_LOOP=0
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
expect reject "msg: recon with production file" "catalog(M0136-1): recon — trace" RALPH_LOOP=1
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
expect reject "msg: recon in scope with production file" "catalog(M0136-1-recon): trace" RALPH_LOOP=1
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS
expect ok "msg: 'recon' mid-summary is not a recon label" "catalog(M0136-1): fix reconciliation after recon"$'\n\nPARITY: N/A — x' RALPH_LOOP=1
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
expect reject "msg: docs label with production file" "docs(M0136-1): x" RALPH_LOOP=1
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
expect reject "msg: ralph label with production file" "ralph: x" RALPH_LOOP=1
echo '// c' >> internal/catalog/x.go; echo x >> docs/a.md; git add internal/catalog/x.go docs/a.md
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS
expect ok "msg: scripts label with production file is not policed" "scripts(M0136-1): x"$'\n\nPARITY: N/A — x' RALPH_LOOP=1
# --- commit-msg: gate stamps -------------------------------------------------
BODY=$'\n\nCATEGORIES-EXCL-MATCH: 539 -> 530'
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
rm -f tmp/gate-stamps/*.json
expect reject "msg: no stamps" "optimizer(M0141-S9): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS
expect reject "msg: optimizer file without the acceptance arm" "optimizer(M0141-S9): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect ok "msg: stamps PASS for tree" "optimizer(M0141-S9): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect reject "msg: missing CATEGORIES line" "optimizer(M0141-S9): x" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect ok "msg: PARITY N/A line" "optimizer(M0141-S9): x"$'\n\nPARITY: N/A — trace only' RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS 0000000000000000000000000000000000000000000000000000000000000000; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect reject "msg: stale tree stamp" "optimizer(M0141-S9): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED; stamp tpch-acceptance-arm PASS
expect reject "msg: SKIP-BLOCKED without ledger" "optimizer(M0141-S9): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/cost.go; git add internal/optimizer/cost.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS
expect reject "msg: cost file needs acceptance-arm" "optimizer(M0141-S9): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/executor/scan.go; git add internal/executor/scan.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect ok "msg: executor with all three stamps" "executor(M0139-0009): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/executor/scan.go; git add internal/executor/scan.go
stamp tpch-spotcheck FAIL; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect reject "msg: spotcheck FAIL" "executor(M0139-0009): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
rm -f tmp/gate-stamps/*.json
expect reject "msg: stamps keyed on files, not on the task id (M0136 id)" "optimizer(M0136-0001): x$BODY" RALPH_LOOP=1
# hole A: three loop commits named only "M-NIGHTLY" and matched no task-id
# regex, so nothing checked them. The staged files decide now.
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
rm -f tmp/gate-stamps/*.json
expect reject "msg: M-NIGHTLY-only subject staging internal/*.go needs stamps" "optimizer(M-NIGHTLY): fix a literal$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
rm -f tmp/gate-stamps/*.json
expect reject "msg: no task id at all staging internal/*.go needs stamps" "optimizer: fix a literal$BODY" RALPH_LOOP=1
echo '// c' >> cmd/goopg/main.go; git add cmd/goopg/main.go
rm -f tmp/gate-stamps/*.json
expect reject "msg: cmd/ .go is gated too" "cmd(M-NIGHTLY): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
expect ok "msg: stamps not required outside loop" "optimizer(M0141-S9): x" RALPH_LOOP=0
# ids: P0- and ids in the body; gated paths: all non-test internal/ .go
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
rm -f tmp/gate-stamps/*.json
expect reject "msg: P0- id needs stamps" "optimizer(P0-E4): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
expect reject "msg: task id only in body needs stamps" "optimizer: tidy"$'\n\nPart of M0142-0016.\nCATEGORIES-EXCL-MATCH: a -> b' RALPH_LOOP=1
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
expect reject "msg: internal/catalog .go is gated too" "catalog(M0143-0002): x$BODY" RALPH_LOOP=1
echo '// c' >> internal/testport/h.go; git add internal/testport/h.go
expect ok "msg: internal/testport is not gated" "testport(M0143-0002): x" RALPH_LOOP=1
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm SKIP-BLOCKED
echo '// c' >> internal/executor/scan.go; git add internal/executor/scan.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm SKIP-BLOCKED
expect reject "msg: acceptance-arm SKIP-BLOCKED rejected even with ledger" "executor(M0139-0009): x$BODY"$'\nledger: x' RALPH_LOOP=1
# code_tree ignores non-code index content (docs staged after the gate ran)
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
echo z >> docs/a.md; git add docs/a.md
expect ok "msg: docs staged after stamping keeps code_tree" "optimizer(M0141-S9): x$BODY" RALPH_LOOP=1
# partial-pathspec commit: ls-files inside the hook sees the temp index, so a
# stamp taken with another internal/ change staged does not match.
echo '// c' >> internal/optimizer/join.go; echo '// c' >> internal/catalog/x.go
git add internal/optimizer/join.go internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
if env RALPH_LOOP=1 git commit -q -m "optimizer(M0141-S9): x$BODY" -- internal/optimizer/join.go >/dev/null 2>&1; then
  fail=$((fail + 1)); echo "FAIL msg: pathspec commit with a full-index stamp should mismatch"
else pass=$((pass + 1)); fi
git reset -q; git checkout -q -- .

# --- commit-msg: recon tasks are read from fix_plan Kind:, not the subject ----
# hole B: two recon tasks landed 6 non-test internal/optimizer files that were
# nothing but `if pathTraceEnabled { ... }` blocks — exactly what rule C1
# forbids — because recon detection was a subject-string match.
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect reject "msg: Kind: recon task staging internal/ is rejected" "optimizer(M0143-0900): add pathTrace blocks$BODY" RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect reject "msg: Kind: recon task named only in the body" "optimizer: add pathTrace blocks$BODY"$'\nPart of M0143-0900.' RALPH_LOOP=1
echo '// c' >> internal/optimizer/join.go; git add internal/optimizer/join.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 PASS; stamp tpch-acceptance-arm PASS
expect ok "msg: Kind: impl task staging internal/ is fine" "optimizer(M0143-0901): add pathTrace blocks$BODY" RALPH_LOOP=1
echo x >> docs/a.md; git add docs/a.md
expect ok "msg: Kind: recon task staging docs only is fine" "docs(M0143-0900): findings" RALPH_LOOP=1

# --- commit-msg: SKIP-BLOCKED needs an owner row in gate-exceptions.md -------
# hole C: the P0-E5-only grant was re-used in 18 commits because commit-msg
# auto-widened it for as long as bench/tpch/runtime_goopg/data.HOLD existed.
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED
expect reject "msg: SKIP-BLOCKED with ledger but no exception row" "catalog(M0143-0002): x$BODY"$'\nledger: sf025-cluster-down' RALPH_LOOP=1
: > ci/logs/gate-exception-usage.log
exc_table '| M0143-0002 | tpcds-sf025 | 1 | 2999-12-31 | test fixture: sf025 cluster down |'
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED
expect ok "msg: SKIP-BLOCKED with an exception row + ledger" "catalog(M0143-0002): x$BODY"$'\nledger: sf025-cluster-down' RALPH_LOOP=1
if grep -q 'task=M0143-0002 gate=tpcds-sf025 ' ci/logs/gate-exception-usage.log 2>/dev/null; then pass=$((pass + 1))
else fail=$((fail + 1)); echo "FAIL msg: exception usage not logged"; fi
if grep -q 'tree=[0-9a-f]\{64\} subject=catalog(M0143-0002)' ci/logs/gate-exception-usage.log 2>/dev/null; then pass=$((pass + 1))
else fail=$((fail + 1)); echo "FAIL msg: usage line missing staged tree/subject"; fi
# the same row is now exhausted (max-commits 1, one usage recorded)
exc_table '| M0143-0002 | tpcds-sf025 | 1 | 2999-12-31 | test fixture: sf025 cluster down |'
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED
expect reject "msg: exception budget exhausted" "catalog(M0143-0002): x$BODY"$'\nledger: sf025-cluster-down' RALPH_LOOP=1
# a row for another task does not cover this commit
exc_table '| P0-E5 | tpcds-sf025 | 9 | 2999-12-31 | other task |'
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED
expect reject "msg: exception row belongs to another task" "catalog(M0143-0002): x$BODY"$'\nledger: sf025-cluster-down' RALPH_LOOP=1
# expired row
exc_table '| M0143-0002 | tpcds-sf025 | 9 | 2000-01-01 | expired |'
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED
expect reject "msg: exception row expired" "catalog(M0143-0002): x$BODY"$'\nledger: sf025-cluster-down' RALPH_LOOP=1
# right task+expiry, wrong gate
exc_table '| M0143-0002 | tpch-spotcheck | 9 | 2999-12-31 | wrong gate |'
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED
expect reject "msg: exception row names a different gate" "catalog(M0143-0002): x$BODY"$'\nledger: sf025-cluster-down' RALPH_LOOP=1
# a granted row still needs the ledger: line
exc_table '| M0143-0002 | tpcds-sf025 | 9 | 2999-12-31 | granted |'
echo '// c' >> internal/catalog/x.go; git add internal/catalog/x.go
stamp tpch-spotcheck PASS; stamp tpcds-sf025 SKIP-BLOCKED
expect reject "msg: exception row without a ledger: line" "catalog(M0143-0002): x$BODY" RALPH_LOOP=1
# --- pre-commit: gate-exceptions.md is owner-only -----------------------------
printf '| M0143-0002 | tpcds-sf025 | 9 | 2999-12-31 | self-granted |\n' >> .ralph/gate-exceptions.md
git add .ralph/gate-exceptions.md
expect reject "pre: gate-exceptions.md edit" "ralph: x" RALPH_LOOP=1
printf '| M0143-0002 | tpcds-sf025 | 9 | 2999-12-31 | self-granted |\n' >> .ralph/gate-exceptions.md
git add .ralph/gate-exceptions.md
expect ok "pre: gate-exceptions.md edit outside loop" "ralph: x" RALPH_LOOP=0
git reset -q --hard HEAD >/dev/null 2>&1

echo "ralph-githooks-test: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
