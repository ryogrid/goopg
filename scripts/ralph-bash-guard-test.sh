#!/usr/bin/env bash
# ralph-bash-guard-test.sh — self-test for scripts/ralph-bash-guard.sh and
# scripts/ralph-file-guard.sh (PreToolUse hooks active only under RALPH_LOOP=1).
# Usage: scripts/ralph-bash-guard-test.sh   (exit 0 = all cases pass)
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
GUARD="$HERE/ralph-bash-guard.sh"
FGUARD="$HERE/ralph-file-guard.sh"
pass=0 fail=0

bash_json() { python3 -c 'import json,sys; print(json.dumps({"tool_name":"Bash","tool_input":{"command":sys.argv[1]}}))' "$1"; }

run_bash() { # <loop-env> <command> -> prints "deny" or "allow"
  local out
  out="$(bash_json "$2" 2>/dev/null | RALPH_LOOP="$1" "$GUARD")"
  if printf '%s' "$out" | grep -q '"permissionDecision": *"deny"'; then echo deny; else echo allow; fi
}

check() { # <expect> <command> [loop-env]
  local expect="$1" cmd="$2" env="${3-1}" got
  got="$(run_bash "$env" "$cmd")"
  if [ "$got" = "$expect" ]; then pass=$((pass + 1))
  else fail=$((fail + 1)); printf 'FAIL expect=%s got=%s RALPH_LOOP=%s\n  cmd: %s\n' "$expect" "$got" "$env" "$cmd"; fi
}

# --- reference-cluster writes ------------------------------------------------
check deny  'psql -p 65433 -d tpch -c "ALTER TABLE lineitem ADD CONSTRAINT x PRIMARY KEY (l_orderkey)"'
check deny  'psql -h 127.0.0.1 -p 65432 -U postgres tpch -c "drop table foo"'
check deny  'PGPORT=65438 psql tpcds -c "ANALYZE store_sales"'
check deny  'psql "host=127.0.0.1 port=65433 dbname=tpch" -c "VACUUM lineitem"'
check deny  'psql postgresql://postgres@127.0.0.1:65433/tpch -c "insert into t values (1)"'
check deny  'psql -p 65433 -c "select pg_terminate_backend(pid) from pg_stat_activity"'
check deny  'psql -p 65438 -c "\copy store_sales from /tmp/x.dat"'
check deny  'psql -p 65433 -c "CREATE INDEX i ON t(a)"'
check deny  'rtk psql -p 65433 -c "truncate t"'
check deny  'psql -p 65432 <<EOF
BEGIN;
DELETE FROM orders WHERE o_orderkey = 1;
ROLLBACK;
EOF'
check deny  'psql -p 65433 -c "EXPLAIN ANALYZE UPDATE t SET a = 1"'
check deny  'pgbench -i -s 1 -p 65433 tpch'
check deny  'vacuumdb --analyze -p 65438 tpcds'
check deny  'python3 -c "import psycopg; psycopg.connect(port=65433).execute(\"DROP TABLE x\")"'
check allow 'psql -p 65438 -c "EXPLAIN SELECT count(*) FROM store_sales WHERE update_ts > 1"'
check allow 'psql -p 65433 -d tpch -c "EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM lineitem WHERE l_shipdate > date '"'"'1995-01-01'"'"'"'
check allow 'psql -p 65432 -c "EXPLAIN ANALYZE SELECT d_date, last_analyze FROM date_dim, pg_stat_user_tables"'
check allow 'psql -p 65438 -c "COPY (SELECT 1) TO STDOUT"'
check allow 'psql -p 65433 -At -c "select relname, n_live_tup from pg_stat_user_tables"'
check allow 'tail -5 tmp/x/cluster.log; psql -p 65433 -c "select 1"'
check allow 'git commit -m "bench: document that :65433 needs ANALYZE after reload"'
check allow 'grep -n 65433 CLAUDE.md | grep -i drop'
check allow 'psql -p 5533 -c "DROP TABLE foo"'
check allow 'pg_basebackup -p 65433 -D /tmp/clone -X stream'
# --- reference-cluster stop / reset / delete ---------------------------------
check deny  './bin/goopg stop -D bench/tpch/runtime_goopg/data'
check deny  'goopg stop -mode immediate -D /home/ryo/work/goopg/goopg/bench/tpch/runtime_goopg/data/'
check deny  'pg_ctl -D bench/tpch/runtime/pgdata restart'
check deny  'postgres/install/bin/pg_ctl stop -D bench/tpcds/runtime/pgdata -m fast'
check deny  'bench/tpch/stop_goopg.sh'
check deny  'cd bench/tpch && ./stop_pg.sh'
check deny  'bench/tpch/setup_goopg.sh --reset'
check deny  'bench/tpcds/server.sh stop sf025'
check deny  'rm -rf bench/tpch/runtime_goopg/data'
check deny  'rm -rf bench/tpch/runtime'
check deny  'kill -KILL $(head -1 bench/tpch/runtime_goopg/data/postmaster.pid)'
check allow 'scripts/ref-clusters-ensure.sh'
check allow 'bench/tpcds/server.sh status all'
check allow 'bench/tpch/setup_goopg.sh'
check allow './bin/goopg stop -D /tmp/m42clone'
check allow 'ls bench/tpch/runtime_goopg/data; rm -rf tmp/scratch'
check allow 'rm -rf bench/tpcds/runtime_goopg/data-sf025-clone-x'
check allow 'scripts/tpch-spotcheck.sh'
# --- gate bypass / history ---------------------------------------------------
check deny  'git commit -n -m "x" -- internal/foo.go'
check deny  'git commit --no-verify -m wip'
check deny  'rtk git commit -am "x" --no-verify'
check deny  'git commit -anm "x"'
check deny  'GOOPG_SKIP_PRECOMMIT=1 git commit -m x -- a.go'
check deny  'git revert HEAD'
check deny  'rtk git reset --hard HEAD~1'
check deny  'git push --force origin HEAD'
check deny  'git push -f'
check allow 'git commit -m "scripts(x): grep -n now; --no-verify is forbidden" -- scripts/x.sh'
check allow 'git commit -F - -- a.go <<'"'"'EOF'"'"'
optimizer(x): use grep -n and git revert wording
EOF'
check allow 'git commit -m "$(cat <<'"'"'EOF'"'"'
docs: mention -n and --no-verify
EOF
)" -- docs/a.md'
check allow 'git log --grep=revert --oneline -n 5'
check allow 'git reset --soft HEAD~1'
check allow 'git push origin HEAD'
check allow 'git commit -m x -- scripts/ralph-no-verify-doc.sh'
# --- process kills -----------------------------------------------------------
check deny  'pkill -f goopg'
check deny  'killall goopg'
check allow 'pkill -f "tmp/my-private-thing"'
# --- instruction files -------------------------------------------------------
check deny  'echo "rule" >> AGENT.md'
check deny  'sed -i "s/never/sometimes/" CLAUDE.md'
check deny  'printf x | tee -a /home/ryo/work/goopg/goopg/AGENT.md'
check deny  'perl -0pi -e "s/a/b/" AGENT.md'
check deny  'cp /tmp/new.md CLAUDE.md'
check allow 'grep -n Harness AGENT.md > /tmp/out.txt'
check allow 'sed -n 1,20p CLAUDE.md'
check allow 'cp AGENT.md /tmp/agent-backup.md'
# --- M5: disabling the guards / hooks ---------------------------------------
check deny  'unset RALPH_LOOP; git commit -m x'
check deny  'unset -v FOO RALPH_LOOP'
check deny  'RALPH_LOOP=0 git commit -m x -- a.go'
check deny  'RALPH_LOOP= git commit -m x'
check deny  'export RALPH_LOOP=0'
check deny  'FOO=1 RALPH_LOOP="" git commit -m x'
check deny  'bash -c "RALPH_LOOP=0 git commit -m x"'
check deny  'env -u RALPH_LOOP git commit -m x'
check deny  'env -i PATH=/usr/bin git commit -m x'
check deny  '/usr/bin/env --ignore-environment git commit -m x'
check deny  'export -n RALPH_LOOP'
check deny  'git config core.hooksPath /dev/null'
check deny  'git -c core.hooksPath=/tmp/none commit -m x'
check deny  'git config --unset core.hooksPath'
check deny  'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.hooksPath GIT_CONFIG_VALUE_0=/x git commit -m y'
check allow 'git config --get core.hooksPath'
check allow 'echo "$RALPH_LOOP"; grep -n RALPH_LOOP=1 scripts/ralph-loop.sh'
check allow 'env GOGC=100 go test ./internal/optimizer/'
check allow 'env | grep RALPH'
check allow 'git commit -m "ralph: note that RALPH_LOOP=0 and env -i are denied" -- docs/a.md'
# --- M5: harness mechanism files ---------------------------------------------
check deny  'sed -i "s/deny/allow/" scripts/ralph-bash-guard.sh'
check deny  'echo "exit 0" > .githooks/pre-commit'
check deny  'rm .githooks/commit-msg'
check deny  'mv scripts/ralph_protected_regions.py /tmp/x.py'
check deny  'chmod -x scripts/ralph-file-guard.sh'
check deny  'cp /tmp/s.json .claude/settings.json'
check deny  'cp /tmp/s.json .codex/hooks.json'
check deny  'printf x >> .ralph/PROMPT.md'
check deny  'perl -pi -e "s/a/b/" scripts/lib/gate-stamp.sh'
check deny  'truncate -s0 scripts/ref-clusters-ensure.sh'
check deny  'tee scripts/lib/ref-clusters.sh < /tmp/x'
check deny  'rm -f scripts/tpch-ref-recover.sh'
check deny  'echo x > ~/.ralph/state.json'
check deny  'rm -rf /home/ryo/.ralph/locks'
check deny  'git checkout HEAD~3 -- scripts/ralph-lineage-guard.py'
check deny  'echo "[core] hooksPath=x" >> .git/config'
check allow 'cat scripts/ralph-bash-guard.sh | head -20'
check allow 'bash scripts/ralph-bash-guard-test.sh'
check allow 'cp scripts/lib/gate-stamp.sh /tmp/gs-backup.sh'
check allow 'python3 scripts/ralph-lineage-guard-test.py > /tmp/out.txt'
check allow 'rm -rf .ralph/tmp-notes.md'
# --- M6: reference start / HOLD / preloss clones ------------------------------
check deny  'GOOPG_CG_UNIT=x scripts/goopg-test-run.sh ./bin/goopg start -D bench/tpch/runtime_goopg/data --listen 127.0.0.1:65433'
check deny  'postgres/install/bin/pg_ctl start -D bench/tpcds/runtime/pgdata'
check deny  'postgres/install/bin/postgres -D bench/tpch/runtime/pgdata -p 65432'
check deny  './bin/goopg start -D bench/tpch/runtime_goopg/preloss-clone-20260916 --listen 127.0.0.1:5533'
check allow 'scripts/ref-clusters-ensure.sh bench/tpch/runtime_goopg/data start'
check allow './bin/goopg start -D /tmp/m42clone --listen 127.0.0.1:5533'
check deny  'rm bench/tpch/runtime_goopg/data.HOLD'
check deny  'mv bench/tpch/runtime_goopg/data.HOLD /tmp/'
check deny  'cp /dev/null bench/tpch/runtime_goopg/data.HOLD'
check deny  ': > bench/tpch/runtime_goopg/data.HOLD'
check deny  'truncate -s 0 x.HOLD'
check allow 'cat bench/tpch/runtime_goopg/data.HOLD'
check allow 'cp bench/tpch/runtime_goopg/data.HOLD /tmp/hold-copy.txt'
check deny  'rm -rf bench/tpch/runtime_goopg/preloss-clone-20260916'
check deny  'echo x > bench/tpch/runtime_goopg/preloss-clone-20260916/postgresql.conf'
check deny  'rsync -a /tmp/src/ bench/tpch/runtime_goopg/preloss-clone-1/'
check deny  'cp -a /tmp/x bench/tpch/runtime_goopg/data/base/1'
check allow 'rsync -a bench/tpch/runtime_goopg/preloss-clone-20260916/ /tmp/pc/'
check allow 'cp -a bench/tpch/runtime_goopg/data /tmp/clone-data'
check allow 'du -sh bench/tpch/runtime_goopg/preloss-clone-20260916'
# --- M6: kills on reference ports / env-resolved ports -----------------------
check deny  'fuser -k 65433/tcp'
check deny  'kill -9 $(lsof -t -i:65433)'
check deny  'lsof -ti :65438 | xargs kill'
check allow 'lsof -i :65433'
check allow 'kill %1; psql -p 5533 -c "select 1"'
check deny  'source bench/tpch/env_goopg.sh && psql -c "DROP TABLE x"'
check deny  '. bench/tpcds/env_tpcds.sh; psql -p "$TPCDS_PG_PORT" -c "ANALYZE store_sales"'
check deny  'source bench/tpch/env_goopg.sh; kill $(lsof -t -i:$PG_PORT)'
check allow 'source bench/tpch/env_goopg.sh && psql -c "select count(*) from lineitem"'
# --- M6: more SQL keywords ---------------------------------------------------
check deny  'psql -p 65433 -c "DO \$\$ BEGIN PERFORM 1; END \$\$"'
check deny  "psql -p 65433 -c 'DO \$\$BEGIN NULL; END\$\$'"
check deny  'psql -p 65432 -c "CHECKPOINT"'
check deny  'psql -p 65432 -c "COMMENT ON TABLE t IS '"'"'x'"'"'"'
check deny  'psql -p 65438 -c "SECURITY LABEL ON TABLE t IS NULL"'
check deny  'psql -p 65438 -c "select pg_reload_conf()"'
check deny  'psql -p 65433 -c "ALTER SYSTEM SET work_mem = '"'"'1GB'"'"'"'
check deny  'psql -p 65433 -c "BEGIN; LOCK TABLE lineitem; COMMIT"'
check deny  'psql -p 65438 -c "\copy store_sales from stdin"'
check allow 'psql -p 65433 -c "SET enable_mergejoin = off; EXPLAIN SELECT 1"'
check allow 'for q in 1 2; do psql -p 65433 -c "select $q"; done'
check allow 'psql -p 65433 -c "select locktype from pg_locks"'
# --- M6: owner-only scripts --------------------------------------------------
check deny  'scripts/tpch-ref-recover.sh --from preloss-clone-1'
check deny  'bash scripts/tpch-ref-recover.sh'
check allow 'sed -n 1,40p scripts/tpch-ref-recover.sh'
HOLDROOT="$(mktemp -d)"; mkdir -p "$HOLDROOT/bench/tpch/runtime_goopg"
check_root() { # <expect> <command> <root>
  local got out
  out="$(bash_json "$2" | RALPH_LOOP=1 CLAUDE_PROJECT_DIR="$3" "$GUARD")"
  if printf '%s' "$out" | grep -q '"permissionDecision": *"deny"'; then got=deny; else got=allow; fi
  if [ "$got" = "$1" ]; then pass=$((pass + 1)); else fail=$((fail + 1)); printf 'FAIL(root) expect=%s got=%s cmd: %s\n' "$1" "$got" "$2"; fi
}
check_root allow 'scripts/tpch-estimate-audit-arm.sh Q3' "$HOLDROOT"
touch "$HOLDROOT/bench/tpch/runtime_goopg/data.HOLD"
check_root deny  'scripts/tpch-estimate-audit-arm.sh Q3' "$HOLDROOT"
check_root allow 'grep -n PORT scripts/tpch-estimate-audit-arm.sh' "$HOLDROOT"
rm -rf "$HOLDROOT"
# --- minor false positives ----------------------------------------------------
check allow 'git commit -m "bench(x): psql -p 65433 -c DROP TABLE t was the incident" -- docs/a.md'
check allow 'git commit -F - -- docs/a.md <<'"'"'EOF'"'"'
docs: psql -p 65433 -c "ALTER TABLE x" and rm .githooks/pre-commit
EOF'
check allow 'cat bench/tpch/stop_goopg.sh'
check allow 'grep -n PGDATA bench/tpch/stop_goopg.sh | head'
check allow 'less bench/tpch/stop_pg.sh'
check allow 'grep -rn GOOPG_SKIP_PRECOMMIT=1 .githooks/'
check deny  'export GOOPG_SKIP_PRECOMMIT=1; git commit -m x'
# --- hole C: .ralph/gate-exceptions.md is owner-only -------------------------
check deny  'printf "| P0-E5 | tpcds-sf025 | 99 | 2099-01-01 | x |\n" >> .ralph/gate-exceptions.md'
check deny  'sed -i "s/2026-09-20/2099-01-01/" .ralph/gate-exceptions.md'
check deny  'rm .ralph/gate-exceptions.md'
check deny  'cp /tmp/mine.md .ralph/gate-exceptions.md'
check allow 'cat .ralph/gate-exceptions.md'
check allow 'grep -n tpcds-sf025 .ralph/gate-exceptions.md'
check allow 'cp .ralph/gate-exceptions.md /tmp/copy.md'
check deny  'bash bench/tpch/stop_goopg.sh'
# --- interactive sessions are unaffected ------------------------------------
check allow 'pkill -f goopg' 0
check allow 'psql -p 65433 -c "DROP TABLE x"' ''
check allow 'git commit --no-verify -m x' 0

# --- denial logging: ci/logs/ralph-guard-denials.log --------------------------
LOGROOT="$(mktemp -d)"
( cd "$LOGROOT" && git init -q . ) >/dev/null 2>&1
bash_json 'psql -p 65433 -c "DROP TABLE x"' \
  | RALPH_LOOP=1 CLAUDE_PROJECT_DIR="$LOGROOT" "$GUARD" >/dev/null
if grep -q 'tool=Bash rule=ref-cluster-write:[0-9]* subject=psql -p 65433' "$LOGROOT/ci/logs/ralph-guard-denials.log" 2>/dev/null
then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) bash denial not logged"; cat "$LOGROOT/ci/logs/ralph-guard-denials.log" 2>/dev/null; fi
bash_json 'git commit --no-verify -m x' \
  | RALPH_LOOP=1 CLAUDE_PROJECT_DIR="$LOGROOT" "$GUARD" >/dev/null
if [ "$(wc -l < "$LOGROOT/ci/logs/ralph-guard-denials.log")" = 2 ] \
   && grep -q 'rule=gate-bypass' "$LOGROOT/ci/logs/ralph-guard-denials.log"
then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) second denial not appended"; fi
bash_json 'psql -p 5533 -c "select 1"' \
  | RALPH_LOOP=1 CLAUDE_PROJECT_DIR="$LOGROOT" "$GUARD" >/dev/null
if [ "$(wc -l < "$LOGROOT/ci/logs/ralph-guard-denials.log")" = 2 ]
then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) allowed command was logged"; fi
# a long command is truncated to 200 chars of subject and the hook still denies
LONG="psql -p 65433 -c \"DROP TABLE x\" # $(printf 'y%.0s' $(seq 1 400))"
if [ "$(run_bash 1 "$LONG")" = deny ]; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) long cmd not denied"; fi
if [ "$(awk -F'subject=' 'END{print length($2)}' "$LOGROOT/ci/logs/ralph-guard-denials.log")" -le 200 ]
then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) subject not truncated"; fi
# an unwritable log directory must not change the decision
UNW="$(mktemp -d)"; ( cd "$UNW" && git init -q . ) >/dev/null 2>&1; chmod 500 "$UNW"
if [ "$(bash_json 'psql -p 65433 -c "DROP TABLE x"' | RALPH_LOOP=1 CLAUDE_PROJECT_DIR="$UNW" "$GUARD" \
        | grep -c '"deny"')" = 1 ]
then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) unwritable log changed the decision"; fi
chmod 700 "$UNW"; rm -rf "$UNW"

# --- file guard (Edit|Write|MultiEdit) ---------------------------------------
TD="$(mktemp -d)"; trap 'rm -rf "$TD"' EXIT
mkdir -p "$TD/.ralph"
cat >"$TD/AGENT.md" <<'EOF'
# AGENT
intro text
<!-- PLAN-PARITY-HARNESS:BEGIN -->
rule one: never stop reference clusters
<!-- PLAN-PARITY-HARNESS:END -->
status note: old
EOF
cat >"$TD/.ralph/fix_plan.md" <<'EOF'
# Fix plan
## Current Priority
1. do the important thing
## Notes / rules
- [ ] **M0142-0001 — task**
EOF
printf 'claude\n' >"$TD/CLAUDE.md"

fcheck() { # <expect> <json>
  local expect="$1" got out
  out="$(printf '%s' "$2" | RALPH_LOOP=1 "$FGUARD" 2>/dev/null)"
  if printf '%s' "$out" | grep -q '"permissionDecision": *"deny"'; then got=deny; else got=allow; fi
  if [ "$got" = "$expect" ]; then pass=$((pass + 1))
  else fail=$((fail + 1)); printf 'FAIL(file) expect=%s got=%s\n  json: %s\n' "$expect" "$got" "$2"; fi
}
ej() { python3 -c 'import json,sys; t,p,o,n=sys.argv[1:5]; ti={"file_path":p}
if t=="Write": ti["content"]=o
elif t=="MultiEdit": ti["edits"]=[{"old_string":o,"new_string":n}]
else: ti.update(old_string=o,new_string=n)
print(json.dumps({"tool_name":t,"tool_input":ti}))' "$@"; }

fcheck deny  "$(ej Edit "$TD/CLAUDE.md" claude x)"
fcheck deny  "$(ej Edit "$TD/AGENT.md" 'rule one: never' 'rule one: rarely')"
fcheck deny  "$(ej MultiEdit "$TD/AGENT.md" '<!-- PLAN-PARITY-HARNESS:END -->' '')"
fcheck allow "$(ej Edit "$TD/AGENT.md" 'status note: old' 'status note: new')"
fcheck allow "$(ej Edit "$TD/AGENT.md" 'intro text' 'intro text v2')"
fcheck deny  "$(ej Write "$TD/AGENT.md" "$(sed 's/rule one/rule 1/' "$TD/AGENT.md")" '')"
fcheck allow "$(ej Write "$TD/AGENT.md" "$(sed 's/status note: old/status note: new/' "$TD/AGENT.md")" '')"
fcheck deny  "$(ej Edit "$TD/.ralph/fix_plan.md" '1. do the important thing' '1. do my thing')"
fcheck allow "$(ej Edit "$TD/.ralph/fix_plan.md" '- [ ] **M0142-0001' '- [x] **M0142-0001')"
fcheck allow "$(ej Edit "$TD/other.md" a b)"
mkdir -p "$TD/scripts/lib" "$TD/.githooks" "$TD/.claude" "$TD/.codex"
for f in scripts/ralph-bash-guard.sh scripts/lib/gate-stamp.sh .githooks/pre-commit .claude/settings.json .codex/hooks.json .ralph/PROMPT.md .ralph/gate-exceptions.md scripts/other.sh; do
  printf 'x\n' > "$TD/$f"
done
fcheck deny  "$(ej Edit "$TD/scripts/ralph-bash-guard.sh" x y)"
fcheck deny  "$(ej Write "$TD/.githooks/pre-commit" 'exit 0' '')"
fcheck deny  "$(ej MultiEdit "$TD/.claude/settings.json" x y)"
fcheck deny  "$(ej MultiEdit "$TD/.codex/hooks.json" x y)"
fcheck deny  "$(ej Edit "$TD/.ralph/PROMPT.md" x y)"
fcheck deny  "$(ej Edit "$TD/scripts/lib/gate-stamp.sh" x y)"
fcheck deny  "$(ej Edit "$TD/.ralph/gate-exceptions.md" x y)"
fcheck deny  "$(ej Write "$TD/.ralph/gate-exceptions.md" '| P0-E5 | tpcds-sf025 | 99 | 2099-01-01 | x |' '')"
fcheck deny  "$(ej Write "$HOME/.ralph/state.json" '{}' '')"
fcheck allow "$(ej Edit "$TD/scripts/other.sh" x y)"
sj() { python3 -c 'import json,sys; t,cwd=sys.argv[1:3]; ti=json.loads(sys.argv[3])
print(json.dumps({"tool_name":"mcp__serena__"+t,"cwd":cwd,"tool_input":ti}))' "$@"; }
fcheck deny  "$(sj replace_content "$TD" '{"relative_path":"scripts/ralph-bash-guard.sh","needle":"x","repl":"y","mode":"literal"}')"
fcheck deny  "$(sj create_text_file "$TD" '{"relative_path":".githooks/commit-msg","content":"exit 0"}')"
fcheck deny  "$(sj replace_symbol_body "$TD" '{"relative_path":"scripts/lib/gate-stamp.sh","name_path":"f","body":"x"}')"
fcheck deny  "$(sj insert_after_symbol "$TD" '{"relative_path":"CLAUDE.md","name_path":"f","body":"x"}')"
fcheck deny  "$(sj replace_content "$TD" '{"relative_path":"AGENT.md","needle":"rule one: never","repl":"rule one: rarely","mode":"literal"}')"
fcheck allow "$(sj replace_content "$TD" '{"relative_path":"AGENT.md","needle":"status note: old","repl":"status note: new","mode":"literal"}')"
fcheck deny  "$(sj replace_content "$TD" '{"relative_path":".ralph/fix_plan.md","needle":"do the.*thing","repl":"x","mode":"regex"}')"
fcheck allow "$(sj replace_content "$TD" '{"relative_path":"scripts/other.sh","needle":"x","repl":"y","mode":"literal"}')"
fcheck deny  "$(sj replace_in_files "$TD" '{"needle":"x","repl":"y","mode":"literal","relative_path":"scripts"}')"
fcheck allow "$(sj replace_in_files "$TD" '{"needle":"x","repl":"y","mode":"literal","relative_path":"scripts","paths_include_glob":"scripts/other.sh"}')"
fcheck allow "$(sj replace_in_files "$TD" '{"needle":"x","repl":"y","mode":"literal","dry_run":true}')"
fcheck allow "$(sj find_symbol "$TD" '{"relative_path":"CLAUDE.md","name_path_pattern":"x"}')"
fcheck deny  "$(sj execute_shell_command "$TD" '{"command":"rm .githooks/pre-commit"}')"
fcheck allow "$(sj execute_shell_command "$TD" '{"command":"ls"}')"
# file-guard denials are logged too
FLOGROOT="$(mktemp -d)"
ej Edit "$TD/CLAUDE.md" claude x | RALPH_LOOP=1 CLAUDE_PROJECT_DIR="$FLOGROOT" "$FGUARD" >/dev/null
if grep -q "tool=file-guard rule=protected-region subject=Edit $TD/CLAUDE.md" "$FLOGROOT/ci/logs/ralph-guard-denials.log" 2>/dev/null
then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) file-guard denial not logged"; cat "$FLOGROOT/ci/logs/ralph-guard-denials.log" 2>/dev/null; fi
ej Edit "$TD/other.md" a b | RALPH_LOOP=1 CLAUDE_PROJECT_DIR="$FLOGROOT" "$FGUARD" >/dev/null
if [ "$(wc -l < "$FLOGROOT/ci/logs/ralph-guard-denials.log")" = 1 ]
then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(log) allowed edit was logged"; fi
rm -rf "$FLOGROOT" "$LOGROOT"

out="$(ej Edit "$TD/CLAUDE.md" claude x 2>/dev/null | RALPH_LOOP=0 "$FGUARD")"
if [ -z "$out" ]; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL(file) RALPH_LOOP=0 not silent"; fi

echo "ralph-bash-guard-test: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
