#!/usr/bin/env bash
# ralph-bash-guard.sh — Claude Code PreToolUse hook (matcher: Bash).
#
# Mechanism for the METHODLOGY3 audit actions H1/H4/H5: prose rules did not
# stop the Ralph loop from running DDL against / stopping a shared reference
# cluster, bypassing the pre-commit gate, or editing its own instruction files.
#
# Active ONLY when RALPH_LOOP=1 (exported by the loop driver). Interactive and
# human sessions are unaffected (silent exit 0).
#
# On a match it prints the PreToolUse deny decision JSON on stdout and exits 0:
#   {"hookSpecificOutput":{"hookEventName":"PreToolUse",
#     "permissionDecision":"deny","permissionDecisionReason":"..."}}
#
# Every denial is also appended to ci/logs/ralph-guard-denials.log (timestamp,
# tool, rule, first 200 chars of the command) so a denial the loop never reports
# is still visible to an audit. A log-write failure never fails the hook.
#
# Test: scripts/ralph-bash-guard-test.sh
#
# KNOWN LIMITATIONS (heuristic text matching, not a shell parser):
#   - SQL arriving via `psql -f file.sql`, a wrapper script, or a variable
#     ($PORT, $PGDATA) is invisible; the reference port / data dir must appear
#     literally in the command text, OR the command must source
#     bench/tpch/env_goopg.sh, bench/tpch/env_pg.sh or bench/tpcds/env_tpcds.sh
#     (then every port in the command is treated as a reference port).
#   - Commands fed through `cat script | bash`, `bash < script`, eval of a
#     computed string, or an interpreter one-liner (python open(...,'w')) are
#     not decoded; protected-file writes are recognised only for redirects,
#     tee, sed/perl -i, cp/install/ln/rsync (destination), rm/mv/chmod/...,
#     dd of=, git checkout/restore/rm/mv.
#   - Some quoted reads trip rules that match anywhere in the command, e.g.
#     `grep "unset RALPH_LOOP" f` or `grep core.hooksPath f`.
#   - Commit messages (`git commit -m/--message`, heredoc bodies on the commit
#     line) are stripped before the content rules, so they never trigger them.
#   - A read-only SELECT that names a column/identifier literally spelled like a
#     write keyword as a bare word (e.g. `SELECT "update" FROM t`, or
#     `select cluster from x`) is denied — rewrite the query or alias it.
#     Identifiers merely containing a keyword (update_ts, d_date, last_analyze,
#     cluster.log, ref-clusters-ensure.sh) are NOT matched.
#   - The port rule needs a DB client token (psql, pgbench, vacuumdb, ...,
#     python, perl, node, PGPORT=, DATABASE_URL=) in the same command, so that
#     `git commit -m "... 65433 ... ANALYZE ..."` or a grep is not denied.
#   - `EXPLAIN ANALYZE <select>` is allowed (executes but does not write);
#     `EXPLAIN ANALYZE INSERT ...` is still denied by the INSERT keyword.
set -uo pipefail

[ "${RALPH_LOOP:-}" = "1" ] || exit 0

input="$(cat)"
if command -v jq >/dev/null 2>&1; then
  cmd="$(printf '%s' "$input" | jq -r '.tool_input.command // empty' 2>/dev/null)"
  hook_cwd="$(printf '%s' "$input" | jq -r '.cwd // empty' 2>/dev/null)"
else
  cmd="$(printf '%s' "$input" | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("tool_input",{}).get("command","") or "")
except Exception: pass' 2>/dev/null)"
  hook_cwd="$(printf '%s' "$input" | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("cwd","") or "")
except Exception: pass' 2>/dev/null)"
fi
[ -n "$cmd" ] || exit 0

# Every denial is appended to ci/logs/ralph-guard-denials.log, so a denial the
# loop does not report in its task body is still visible to an audit. RULE is
# set per rule section below. Logging NEVER fails the hook.
RULE="unknown"
guard_log() { # <tool> <rule> <subject>
  local root logf
  root="${CLAUDE_PROJECT_DIR:-}"
  [ -n "$root" ] || root="$(git -C "${hook_cwd:-.}" rev-parse --show-toplevel 2>/dev/null)" || true
  [ -n "$root" ] || return 0
  logf="$root/ci/logs/ralph-guard-denials.log"
  mkdir -p "$root/ci/logs" 2>/dev/null || return 0
  printf '%s tool=%s rule=%s subject=%s\n' \
    "$(date -Iseconds 2>/dev/null || echo unknown-time)" "$1" "$2" \
    "$(printf '%s' "$3" | tr '\n\t' '  ' | cut -c1-200)" \
    >>"$logf" 2>/dev/null || true
  return 0
}

deny() {
  local reason="$1" esc
  guard_log Bash "${RULE}:${BASH_LINENO[0]:-0}" "$cmd" || true
  esc="$(printf '%s' "RALPH_LOOP guard: $reason" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')"
  printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"%s"}}\n' "$esc"
  exit 0
}

# m <ERE> <text> — case-insensitive extended regex match.
m() { printf '%s' "$2" | grep -Eqi -- "$1"; }
# mc <ERE> <text> — case-sensitive.
mc() { printf '%s' "$2" | grep -Eq -- "$1"; }

# Normalise the rtk proxy form (the user-level `rtk hook claude` rewrites
# `git …` into `rtk git …`; hooks run in parallel so both forms reach us).
norm="$(printf '%s' "$cmd" | perl -pe 's/(^|[\s;&|(`])rtk\s+(?:proxy\s+)?(?=git\b|psql\b|pg_ctl\b|kill\b|pkill\b)/$1/g')"
# Collapse backslash-newline continuations.
norm="$(printf '%s' "$norm" | perl -0pe 's/\\\n/ /g')"
# Unquoted skeleton: quoted strings emptied, heredoc bodies dropped. Used for
# flag parsing (git commit -n) so commit messages cannot trigger it.
unq="$(printf '%s' "$norm" | perl -0pe '
  s/"(?:[^"\\]|\\.)*"|\x27[^\x27]*\x27/""/gs;   # quoted strings
  s/<<-?\s*\S+[^\n]*\n.*\z//s;                   # heredoc bodies (to EOF)
')"
# Command text with git commit MESSAGES removed (heredoc bodies opened on a
# `git ... commit` line, then -m/--message arguments after the first commit).
# Content rules (ports + SQL, protected paths, env tricks) run on this so a
# commit message describing a forbidden command is not itself denied.
nomsg="$(printf '%s' "$norm" | perl -0777 -pe '
  s/(\bgit\b[^\n]*\bcommit\b[^\n]*<<-?\s*(["\x27]?)(\w+)\2[^\n]*\n).*?\n[ \t]*\3[ \t]*(?=\n|\z)/$1/gs;
  if (/(?:^|[^A-Za-z0-9_.\/-])git(?:\s+(?:-[cC]\s+\S+|--\S+))*\s+commit\b/) {
    my $pos = $-[0]; my $head = substr($_, 0, $pos); my $tail = substr($_, $pos);
    $tail =~ s/((?:\s-[A-Za-z]*m|\s--message)(?:=|\s*))("(?:[^"\\]|\\.)*"|\x27[^\x27]*\x27|[^\s;&|]+)/$1""/gs;
    $_ = $head . $tail;
  }')"

REF_ESCAPE="Anything that writes goes to a PRIVATE CLONE on a 55xx port (see CLAUDE.md, Running a server manually). If a reference cluster is broken or a task truly needs this, write an escalation block into the task and mark it [!] — do not repair it yourself."

# Keyword boundary: not adjacent to identifier/path characters, so update_ts,
# d_date, cluster.log, ref-clusters, last_analyze do not match.
L='(^|[^A-Za-z0-9_./$-])'
R='([^A-Za-z0-9_./-]|$)'
# Command-name boundary: may be preceded by a path (./bin/goopg, /usr/bin/psql).
LC='(^|[^A-Za-z0-9_.-])'
Q="[\"']"

# segs <text> [separators] — one command segment per line.
segs() { printf '%s\n' "$1" | tr "${2:-;&|}" '\n\n\n'; }

# first_word <segment> — basename of the command word, skipping leading
# assignments and wrappers (sudo, time, nohup, exec, env, timeout N, rtk).
first_word() {
  printf '%s' "$1" | perl -ne '
    s/^[\s({!`]+//; s/^\$\(\s*//;
    my $asg = qr/[A-Za-z_]\w*=(?:"(?:[^"\\]|\\.)*"|\x27[^\x27]*\x27|\S*)\s+/;
    1 while s/^(?:$asg|(?:sudo|time|nohup|exec|command|builtin|then|do|else)\s+|env(?:\s+-\S+)*\s+|timeout(?:\s+-\S+)*\s+\S+\s+|rtk(?:\s+proxy)?\s+)//;
    if (/^([^\s;&|)]+)/) { my $w = $1; $w =~ s{.*/}{}; print $w }
    last;'
}
# is_reader <segment> — the segment only reads/prints what it names.
is_reader() {
  local w; w="$(first_word "$1")"
  case "$w" in
    cat|less|more|head|tail|grep|egrep|fgrep|rg|ag|ugrep|wc|ls|stat|file|diff|cmp|sha256sum|md5sum|bat|nl|od|xxd|strings|echo|printf|git) return 0 ;;
    sed) m '(^|[[:space:]])(-[A-Za-z]*i|--in-place)' "$1" && return 1; return 0 ;;
  esac
  return 1
}

# assigns <VAR> <value-regex> <text> — VAR=value in assignment position
# (segment start, after other assignments, export/declare/env/..., or at the
# start of a `bash -c '...'` / eval string). `grep VAR=1 file` is not one.
assigns() {
  printf '%s' "$3" | VAR="$1" VAL="$2" perl -0777 -ne '
    my $v = quotemeta $ENV{VAR}; my $val = $ENV{VAL};
    my $asg = qr/[A-Za-z_]\w*=(?:"(?:[^"\\]|\\.)*"|\x27[^\x27]*\x27|[^\s;&|]*)\s+/;
    exit 0 if /(?:^|[;&|(\n`{]|\$\(|\b(?:bash|sh|zsh|eval)\b[^;&|\n]*?["\x27])\s*(?:(?:then|do|else|exec|time|nohup|sudo|command)\s+)*(?:(?:export|declare|typeset|local|readonly|env)(?:\s+-[^\s;&|]*)*\s+)?(?:$asg)*$v=$val/m;
    exit 1'
}

# writes_to <path-ERE> <text> — a shell write/remove/move/chmod naming the path.
writes_to() {
  local p="$1" t="$2"
  m "(>>?|>\||&>>?)[[:space:]]*${Q}?[^[:space:];&|]*(${p})" "$t" \
    || m "${LC}tee${R}[^;&|]*(${p})" "$t" \
    || m "${LC}(sed|perl)[[:space:]]([^;&|]*[[:space:]])?(-[A-Za-z0-9]*i|--in-place)[^;&|]*(${p})" "$t" \
    || m "${LC}(rm|rmdir|unlink|shred|truncate|mv|chmod|chown|chgrp|chattr)${R}[^;&|]*(${p})" "$t" \
    || m "${LC}rsync${R}[^;&|]*--remove-source-files[^;&|]*(${p})" "$t" \
    || m "${LC}dd${R}[^;&|]*of=[^[:space:];&|]*(${p})" "$t" \
    || m "${LC}git([[:space:]]+(-[cC][[:space:]]+[^[:space:]]+|--[^[:space:]]+))*[[:space:]]+(checkout|restore|rm|mv)${R}[^;&|]*(${p})" "$t" \
    && return 0
  # cp/install/ln/rsync: only the DESTINATION (last argument) counts, so a
  # read-only copy FROM a protected/reference path is allowed.
  local seg last
  while IFS= read -r seg; do
    m "${LC}(cp|install|ln|rsync)[[:space:]]" "$seg" || continue
    last="$(printf '%s' "$seg" | perl -ne 's/[\s)]+$//; /([^\s]+)$/ && print $1')"
    last="${last//\"/}"; last="${last//\'/}"
    m "(${p})" "$last " && return 0
  done < <(segs "$t")
  return 1
}

# Reference ports named literally, or via a sourced bench env file.
REFENV=0
if m "(^|[[:space:];&|(])(source|\.)[[:space:]]+${Q}?[^[:space:];&|]*bench/(tpch/env_goopg|tpch/env_pg|tpcds/env_tpcds)\.sh" "$nomsg"; then
  REFENV=1
fi
REFPORT_RE='(^|[^0-9])6543[238]([^0-9]|$)'
REFPORT=0
if mc "$REFPORT_RE" "$nomsg" || [ "$REFENV" -eq 1 ]; then REFPORT=1; fi

# ---------------------------------------------------------------------------
# 0. Disabling the loop guards themselves (RALPH_LOOP, hooks path)
# ---------------------------------------------------------------------------
RULE=loop-guard-disable
LOOP_ESCAPE="The RALPH_LOOP guards and git hooks are part of the harness; the loop may not disable, unset or re-route them. If a gate cannot run, mark the task [!] with an escalation block."
if assigns RALPH_LOOP '' "$nomsg"; then
  deny "RALPH_LOOP= assignment. $LOOP_ESCAPE"
fi
if m "${LC}unset[[:space:]]+(-[fvn][[:space:]]+)*([A-Za-z_][A-Za-z0-9_]*[[:space:]]+)*RALPH_LOOP${R}" "$nomsg" \
   || m "${LC}(export[[:space:]]+-n|(declare|typeset)[[:space:]]+\+x)[^;&|]*RALPH_LOOP" "$nomsg"; then
  deny "unset/un-export of RALPH_LOOP. $LOOP_ESCAPE"
fi
if m "${LC}env([[:space:]]+(-[^[:space:];&|]+|[A-Za-z_][A-Za-z0-9_]*=[^[:space:];&|]*))*[[:space:]]+(-[A-Za-z]*i[A-Za-z]*|--ignore-environment|-|-u[[:space:]]*RALPH_LOOP|--unset[= ]RALPH_LOOP)([[:space:]]|$)" "$nomsg"; then
  deny "env -i / env -u RALPH_LOOP drops the loop environment. $LOOP_ESCAPE"
fi
while IFS= read -r seg; do
  m 'core\.hookspath' "$seg" || continue
  m "${LC}git${R}[^;&|]*config[^;&|]*--get(-all|-regexp)?${R}" "$seg" && continue
  deny "core.hooksPath (git config / git -c) re-routes the git hooks. $LOOP_ESCAPE"
done < <(segs "$nomsg")
if mc 'GIT_CONFIG_(PARAMETERS|COUNT|KEY_[0-9]|VALUE_[0-9]|GLOBAL|SYSTEM|NOSYSTEM)' "$nomsg"; then
  deny "GIT_CONFIG_* environment overrides can re-route the git hooks. $LOOP_ESCAPE"
fi

# ---------------------------------------------------------------------------
# 1. Writes / backend kills against reference clusters :65432 :65433 :65438
# ---------------------------------------------------------------------------
RULE=ref-cluster-write
if [ "$REFPORT" -eq 1 ]; then
  client='(^|[^A-Za-z0-9_-])(psql|pgbench|vacuumdb|reindexdb|clusterdb|createdb|dropdb|createuser|dropuser|pg_restore|python3?|perl|ruby|node|PGPORT=|DATABASE_URL=)'
  if m "$client" "$nomsg"; then
    if m '(^|[^A-Za-z0-9_-])(pgbench|vacuumdb|reindexdb|clusterdb|createdb|dropdb|createuser|dropuser|pg_restore)([^A-Za-z0-9_-]|$)' "$nomsg"; then
      deny "a write-capable client tool (pgbench/vacuumdb/createdb/dropdb/pg_restore/...) targets a READ-ONLY reference cluster (:65432/:65433/:65438). $REF_ESCAPE"
    fi
    # Neutralise EXPLAIN ANALYZE / EXPLAIN (ANALYZE, ...) — read-only unless the
    # explained statement itself is a write (caught below).
    sql="$(printf '%s' "$nomsg" | perl -pe 's/\bexplain\s*\([^)]*\)/EXPLAIN/gi; s/\bexplain\s+analy[sz]e\b/EXPLAIN/gi')"
    kw='(alter|create|drop|insert|update|delete|truncate|analy[sz]e|vacuum|grant|revoke|reindex|cluster|pg_terminate_backend|pg_cancel_backend|checkpoint|pg_reload_conf|lock|comment[[:space:]]+on|security[[:space:]]+label|refresh[[:space:]]+materialized|merge[[:space:]]+into|call[[:space:]]+[a-z_]+|nextval|setval|pg_switch_wal|pg_stat_reset[a-z_]*|lo_import|lo_unlink|import[[:space:]]+foreign)'
    if m "${L}${kw}${R}" "$sql" || m "${L}do[[:space:]]+(\\\\?\\\$|'|language${R})" "$sql"; then
      deny "DDL/DML/ANALYZE/VACUUM/GRANT/DO/CHECKPOINT/LOCK/COMMENT/backend-kill against a READ-ONLY reference cluster (:65432/:65433/:65438). Only SELECT/EXPLAIN/pg_basebackup are allowed there. $REF_ESCAPE"
    fi
    if m "${L}copy${R}.*${L}from${R}" "$sql"; then
      deny "COPY/\\copy ... FROM (a write) against a READ-ONLY reference cluster. $REF_ESCAPE"
    fi
  fi
  # kill / fuser -k / lsof -t | xargs kill aimed at a reference port.
  while IFS= read -r seg; do
    if mc "$REFPORT_RE" "$seg" || { [ "$REFENV" -eq 1 ] && m 'PORT|fuser|lsof' "$seg"; }; then
      if m "${LC}(kill|pkill|killall)${R}" "$seg" || m "${LC}fuser${R}[^;&|]*-[A-Za-z]*k" "$seg"; then
        deny "kill / fuser -k / lsof -t kill pipeline aimed at a reference cluster port (:65432/:65433/:65438). $REF_ESCAPE"
      fi
    fi
  done < <(segs "$nomsg" ';&')
fi

# ---------------------------------------------------------------------------
# 2. Starting / stopping / resetting / deleting a reference cluster
# ---------------------------------------------------------------------------
RULE=ref-cluster-lifecycle
REFDIR='bench/tpch/runtime_goopg/data([^A-Za-z0-9_.-]|$)|bench/tpch/runtime/pgdata|bench/tpcds/runtime/pgdata|bench/tpch/runtime_goopg/preloss-clone-'
while IFS= read -r seg; do
  if m "${LC}(stop_goopg|stop_pg)\.sh" "$seg" && ! is_reader "$seg"; then
    deny "stop_goopg.sh / stop_pg.sh stop a shared reference cluster. Reference clusters are started only via scripts/ref-clusters-ensure.sh and never stopped by the loop. $REF_ESCAPE"
  fi
  if m "${LC}tpch-ref-recover\.sh" "$seg" && ! is_reader "$seg"; then
    deny "scripts/tpch-ref-recover.sh is owner-only (reference-cluster recovery). Write an escalation block into the task and mark it [!]."
  fi
  if m "${LC}tpch-estimate-audit-arm\.sh" "$seg" && ! is_reader "$seg"; then
    root="${CLAUDE_PROJECT_DIR:-}"
    [ -n "$root" ] || root="$(git -C "${hook_cwd:-.}" rev-parse --show-toplevel 2>/dev/null)"
    if [ -n "$root" ] && [ -e "$root/bench/tpch/runtime_goopg/data.HOLD" ]; then
      deny "scripts/tpch-estimate-audit-arm.sh while bench/tpch/runtime_goopg/data.HOLD exists: the TPC-H goopg reference is on hold (owner recovery pending). Do not run it; mark dependent work [!] with the HOLD as the blocker."
    fi
  fi
done < <(segs "$nomsg")
if m "(setup_goopg|setup_pg)\.sh[^;&|]*--reset" "$nomsg"; then
  deny "setup_*.sh --reset wipes a shared reference cluster. $REF_ESCAPE"
fi
if m '(^|[^A-Za-z0-9_-])server\.sh[[:space:]]+(stop|restart)' "$nomsg"; then
  deny "bench/tpcds/server.sh stop/restart stops shared TPC-DS clusters (incl. the :65438 PG reference). Use scripts/ref-clusters-ensure.sh to START servers; never stop them. $REF_ESCAPE"
fi
# Per command segment (split on ; & | newline) so a harmless `ls <refdir>`
# next to an unrelated `rm tmp/x` does not trip the rule.
while IFS= read -r seg; do
  m "$REFDIR" "$seg" || continue
  if m "${LC}(goopg|pg_ctl)${R}" "$seg" && m "${L}(stop|restart|kill)${R}" "$seg"; then
    deny "goopg stop / pg_ctl stop|restart|kill on a reference cluster data dir. $REF_ESCAPE"
  fi
  if ! m 'ref-clusters-ensure\.sh' "$seg"; then
    if { m "${LC}(goopg|pg_ctl)${R}" "$seg" && m "${L}start${R}" "$seg"; } \
       || m "${LC}(postgres|postmaster)${R}[^;&|]*-D" "$seg"; then
      deny "starting a server on a reference cluster data dir outside scripts/ref-clusters-ensure.sh. Run scripts/ref-clusters-ensure.sh (it owns reference start-up) or use a private clone. $REF_ESCAPE"
    fi
  fi
  if m '(^|[[:space:](])(rm|mv)[[:space:]]' "$seg"; then
    deny "rm/mv on a reference cluster data dir. $REF_ESCAPE"
  fi
done < <(segs "$nomsg")
if writes_to "$REFDIR" "$nomsg"; then
  deny "write/copy-over/remove/chmod into a reference cluster data dir or a preloss clone (read-only copies FROM it are fine). $REF_ESCAPE"
fi
if writes_to "\.HOLD([\"'[:space:];&|)/]|$)" "$nomsg"; then
  deny "rm/mv/cp-over/truncate/redirect of a *.HOLD file: HOLD markers are placed and cleared by the owner only. $REF_ESCAPE"
fi
if m "$REFDIR" "$nomsg" && m "${LC}kill${R}" "$nomsg" && m 'postmaster\.pid' "$nomsg"; then
  deny "kill of a reference cluster postmaster (PID from its postmaster.pid). $REF_ESCAPE"
fi
# rm/mv of an ancestor of a reference data dir.
if m '(^|[[:space:];&|(])(rm|mv)[[:space:]][^;&|]*(^|[[:space:]"'"'"'/])bench(/tpch(/runtime(_goopg)?)?|/tpcds(/runtime)?)?/?(["'"'"'[:space:];&|)]|$)' "$nomsg"; then
  deny "rm/mv of a directory containing a reference cluster data dir. $REF_ESCAPE"
fi

# ---------------------------------------------------------------------------
# 3. Gate bypass / history rewriting
# ---------------------------------------------------------------------------
RULE=gate-bypass
if assigns GOOPG_SKIP_PRECOMMIT "[\"']?1" "$nomsg"; then
  deny "GOOPG_SKIP_PRECOMMIT=1 bypasses the pre-commit gate. Run the commit normally; if the gate cannot run, mark the task [!] with an escalation block."
fi
GITPFX='(^|[^A-Za-z0-9_./-])git([[:space:]]+(-[cC][[:space:]]+[^[:space:]]+|--[^[:space:]]+))*[[:space:]]+'
if mc "${GITPFX}revert([[:space:]]|$)" "$unq"; then
  deny "git revert is not allowed in the loop (reverting landed work is an owner decision). Write an escalation block into the task and mark it [!]."
fi
if mc "${GITPFX}reset([[:space:]][^;&|]*)?--hard" "$unq"; then
  deny "git reset --hard discards work (possibly a concurrent session's). Use a scoped git restore -- <pathspec> on files you own, or escalate."
fi
if mc "${GITPFX}push([[:space:]][^;&|]*)?[[:space:]](--force(-with-lease)?(=[^[:space:]]*)?|-[A-Za-z]*f[A-Za-z]*|\+[^[:space:]]+)([[:space:]]|$)" "$unq"; then
  deny "git push --force is not allowed in the loop."
fi
# git commit -n / --no-verify: walk the unquoted segment after `commit`.
while IFS= read -r seg; do
  [ -n "$seg" ] || continue
  # shellcheck disable=SC2086
  set -f; set -- $seg; set +f
  for tok in "$@"; do
    case "$tok" in
      --) break ;;
      --no-v|--no-ve|--no-ver|--no-veri|--no-verif|--no-verify)
        deny "git commit --no-verify bypasses the pre-commit gate (pgbench smoke + RALPH_LOOP checks). Commit without it; fix what the hook reports." ;;
      --*) ;;
      -*)
        flags="${tok#-}"
        i=0
        while [ $i -lt ${#flags} ]; do
          ch="${flags:$i:1}"
          case "$ch" in
            n) deny "git commit -n (--no-verify) bypasses the pre-commit gate. Commit without it; fix what the hook reports." ;;
            m|F|c|C|t|S|u) break ;;   # rest of the cluster is an argument
          esac
          i=$((i + 1))
        done ;;
    esac
  done
done < <(printf '%s\n' "$unq" | perl -ne '
  while (/(?:^|[^A-Za-z0-9_.\/-])git(?:\s+(?:-[cC]\s+\S+|--\S+))*\s+commit\b([^;&|\n]*)/g) { print "$1\n" }')

# ---------------------------------------------------------------------------
# 4. Blanket process kills
# ---------------------------------------------------------------------------
RULE=blanket-kill
if m "${LC}pkill${R}[^;&|]*-[A-Za-z]*f[^;&|]*goopg" "$nomsg" || m "${LC}killall${R}[^;&|]*goopg" "$nomsg"; then
  deny "pkill -f goopg / killall goopg self-matches the shell and kills peers' and reference servers. Stop YOUR private server via goopg stop -D <your dir> or its PID file."
fi

# ---------------------------------------------------------------------------
# 5. Shell writes to instruction files and harness mechanism files
# ---------------------------------------------------------------------------
RULE=protected-file-write
IF='(CLAUDE|AGENT)\.md'
if writes_to "$IF" "$nomsg"; then
  deny "shell write to CLAUDE.md/AGENT.md. The loop does not edit its instruction files (H4). A rule that looks wrong is an escalation: write an escalation block into the task and mark it [!]."
fi
HB="([\"'[:space:];&|)/]|$)"
PROT="ralph-[A-Za-z0-9_-]*guard[A-Za-z0-9_.-]*|ralph_protected_regions\.py|ralph-githooks-test\.sh|\.githooks${HB}|\.claude/settings\.json|\.ralph/PROMPT\.md|\.ralph/gate-exceptions\.md|ref-clusters-ensure\.sh|lib/ref-clusters\.sh|tpch-ref-recover\.sh|lib/gate-stamp\.sh|(~|\\\$HOME|\\\$\{HOME\}|/home/[A-Za-z0-9_.-]+|/root)/\.ralph${HB}|\.git/(config|hooks)"
if writes_to "$PROT" "$nomsg"; then
  deny "shell write/rm/mv/chmod of a harness mechanism file (ralph guards, .githooks, .claude/settings.json, .ralph/PROMPT.md, .ralph/gate-exceptions.md, ref-cluster / gate-stamp plumbing, .git/config, ~/.ralph/). These are owner-only (M5): write an escalation block into the task and mark it [!]."
fi

exit 0
