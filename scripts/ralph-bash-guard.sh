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
# MATCHED — set before a deny() to record which token/pattern actually
# matched; it lands in the denial log (`match=`) and in the deny reason so a
# false positive can be diagnosed from the denial itself (added 2026-10-02 —
# the denial log previously held rule+line only, which made heuristic false
# positives indistinguishable from real ones).
MATCHED=""
first_hit() { # <ERE> <text> — first 3 matches of the pattern, space-joined
  printf '%s' "$2" | grep -oiE "$1" 2>/dev/null | head -3 | tr '\n' ' '
}
guard_log() { # <tool> <rule> <subject>
  local root logf mf=""
  root="${CLAUDE_PROJECT_DIR:-}"
  [ -n "$root" ] || root="$(git -C "${hook_cwd:-.}" rev-parse --show-toplevel 2>/dev/null)" || true
  [ -n "$root" ] || return 0
  logf="$root/ci/logs/ralph-guard-denials.log"
  mkdir -p "$root/ci/logs" 2>/dev/null || return 0
  [ -n "$MATCHED" ] && mf=" match=$(printf '%s' "$MATCHED" | tr '\n\t' '  ' | cut -c1-80)"
  printf '%s tool=%s rule=%s%s subject=%s\n' \
    "$(date -Iseconds 2>/dev/null || echo unknown-time)" "$1" "$2" "$mf" \
    "$(printf '%s' "$3" | tr '\n\t' '  ' | cut -c1-200)" \
    >>"$logf" 2>/dev/null || true
  return 0
}

deny() {
  local reason="$1" esc
  guard_log Bash "${RULE}:${BASH_LINENO[0]:-0}" "$cmd" || true
  esc="$(printf '%s' "RALPH_LOOP guard: $reason${MATCHED:+ (matched: $MATCHED)}" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')"
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

# nomsgd — nomsg with path tokens under PRIVATE scratch/doc roots neutralised.
# A private clone or mirror checkout under tmp/ legitimately ends in
# bench/tpch/runtime_goopg/data (and `ln -sfn $PWD/bench/.../data
# /tmp/mirror/bench/.../data` is a write into a private path, not into the
# reference cluster). Only the reference-data-dir checks (rule 2) scan this
# variant (added 2026-10-02). The token stops at any shell metachar
# (incl. & < >) so `tmp/x&rm -rf bench/...` cannot glue a write onto a
# private path, and a token containing `..` is never neutralised — it can
# traverse out of the private root back into bench/.
nomsgd="$(printf '%s' "$nomsg" | perl -pe '
  s{(^|[\s;&|<>()"`'"'"'])(?:/tmp/|\.?/?tmp/|\.?/?analysis/|\.?/?docs/|\.?/?ci/logs/)([^\s;&|<>()"`'"'"']*)}{
    my ($b, $t, $whole) = ($1, $2, $&);
    $t =~ m{(?:^|/)\.\.(?:/|$)} ? $whole : "${b}PRIVPATH"
  }ge')"

# strip_writer_heredocs — drop heredoc bodies whose CONSUMING command is a
# pure file-writer (`cat >f`, `cat <<EOF >f`, `tee f`, `dd of=f`). Those
# bodies are file content — evidence/README/design text — not SQL; SQL
# keywords in them used to deny
# `cat > analysis/.../README.md <<EOF ... psql -p 65432 ... ANALYZE ... EOF`.
# The test is the command, not the presence of `>` — `psql <<EOF > out`
# and `bash <<EOF > log` feed the body to a live program and must NOT be
# stripped (a `>` on the opener would otherwise be a deterministic bypass:
# 2026-10-02 review). `cat <<EOF` with no file target (stdout, maybe piped
# to psql) is kept too.
strip_writer_heredocs() {
  perl -0777 -pe '
    my $src = $_; my $out = ""; my $pos = 0;
    while ($src =~ /<<-?[ \t]*(["\x27]?)([A-Za-z_]\w*)\1/g) {
      # Capture the tag NOW — every $seg/$w regex below rewrites $1/$2.
      my ($hs, $tag) = ($-[0], $2);
      my $nl = index($src, "\n", $hs);
      last if $nl < 0;
      my $opener = substr($src, rindex($src, "\n", $hs) + 1, $nl - rindex($src, "\n", $hs) - 1);
      # The command that consumes the heredoc is the segment holding the
      # `<<` — the opener line can be `psql ...; cat >f <<EOF`, so take the
      # first word of the segment containing the heredoc token, not of the
      # whole line.
      my $seg = substr($opener, 0, $hs - rindex($src, "\n", $hs) - 1);
      $seg =~ s{^.*[;&|()]}{};
      my ($w) = $seg =~ /^[\s({]*([^\s;&|(<>`]+)/;
      $w = "" unless defined $w; $w =~ s{.*/}{};
      my $writer =
        ($w eq "cat" && $opener =~ />>?/) ||
        ($w eq "tee" && $opener =~ /(?:^|\s)tee\s+\S/) ||
        ($w eq "dd"  && $opener =~ /\bof=/);
      next unless $writer;
      my $body_end = index($src, "\n$tag\n", $nl);
      # Terminator as the last line without a trailing newline (index() is
      # literal — \z would never match there).
      my $term_at_end = substr($src, $nl) =~ /\n\Q$tag\E[ \t]*\z/;
      my $skip_to;
      if    ($body_end >= 0) { $skip_to = $body_end + 1 + length($tag) + 1 }
      elsif ($term_at_end)   { $skip_to = length($src) }
      else                   { $skip_to = $nl + 1 }
      $out .= substr($src, $pos, $nl + 1 - $pos);
      $pos = $skip_to;
      pos($src) = $skip_to;
    }
    $_ = $out . substr($src, $pos);' <<<"$1"
}

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
  # Client detection runs on nomsgd (quotes and non-writer heredoc bodies
  # retained): `psql` inside `bash -c '…'`/`eval "…"`/a bash heredoc is a
  # real invocation, not doc text (2026-10-02 review — detecting only on
  # the unquoted skeleton let every quoted psql through). The doc-text
  # false positives that motivated unq are instead handled by
  # strip_writer_heredocs below (writer openers) and by the kw scan still
  # needing a write keyword. Interpreters count as clients only with a
  # connect marker — a python heredoc editing a file that mentions a port
  # or a SQL keyword in its strings is not driving a connection; DB-driving
  # code always carries one of these tokens (2026-10-02).
  client='(^|[^A-Za-z0-9_-])(psql|pgbench|vacuumdb|reindexdb|clusterdb|createdb|dropdb|createuser|dropuser|pg_restore|PGPORT=|DATABASE_URL=)'
  interp='(^|[^A-Za-z0-9_-])(python3?|perl|ruby|node)([^A-Za-z0-9_-]|$)'
  conmark='psycopg|pg8000|asyncpg|DBD::Pg|DBI[-:>]|pg_connect|node-postgres|postgres://|postgresql://|\.connect\(|connect\(.*(5432|6543[0-9])|psql|pg_ctl|pg_dump|pg_basebackup|require.{0,4}pg|new[[:space:]]+(Client|Pool)\b|PG::Connection|subprocess'
  # nomsgd_s: nomsgd minus writer-heredoc bodies — client detection sees
  # quoted/wrapped invocations but not doc text being written to files.
  nomsgd_s="$(strip_writer_heredocs "$nomsgd")"
  if m "$client" "$nomsgd_s" || { m "$interp" "$unq" && m "$conmark" "$nomsg"; }; then
    if m '(^|[^A-Za-z0-9_-])(pgbench|vacuumdb|reindexdb|clusterdb|createdb|dropdb|createuser|dropuser|pg_restore)([^A-Za-z0-9_-]|$)' "$nomsgd_s"; then
      MATCHED="client:$(first_hit 'pgbench|vacuumdb|reindexdb|clusterdb|createdb|dropdb|createuser|dropuser|pg_restore' "$nomsgd_s")"
      deny "a write-capable client tool (pgbench/vacuumdb/createdb/dropdb/pg_restore/...) targets a READ-ONLY reference cluster (:65432/:65433/:65438). $REF_ESCAPE"
    fi
    # Neutralise EXPLAIN ANALYZE / EXPLAIN (ANALYZE, ...) — read-only unless the
    # explained statement itself is a write (caught below). Heredoc bodies
    # feeding a file-writer are doc text, not SQL (strip_writer_heredocs).
    sql="$(printf '%s' "$nomsg" | perl -pe 's/\bexplain\s*\([^)]*\)/EXPLAIN/gi; s/\bexplain\s+analy[sz]e\b/EXPLAIN/gi')"
    sql="$(strip_writer_heredocs "$sql")"
    kw='(alter|create|drop|insert|update|delete|truncate|analy[sz]e|vacuum|grant|revoke|reindex|cluster|pg_terminate_backend|pg_cancel_backend|checkpoint|pg_reload_conf|lock|comment[[:space:]]+on|security[[:space:]]+label|refresh[[:space:]]+materialized|merge[[:space:]]+into|call[[:space:]]+[a-z_]+|nextval|setval|pg_switch_wal|pg_stat_reset[a-z_]*|lo_import|lo_unlink|import[[:space:]]+foreign)'
    if m "${L}${kw}${R}" "$sql"; then
      MATCHED="kw:$(first_hit "$kw" "$sql")"
      deny "DDL/DML/ANALYZE/VACUUM/GRANT/DO/CHECKPOINT/LOCK/COMMENT/backend-kill against a READ-ONLY reference cluster (:65432/:65433/:65438). Only SELECT/EXPLAIN/pg_basebackup are allowed there. $REF_ESCAPE"
    fi
    # DO needs a dollar-quote tag or a quoted/language body — a bare `do $VAR`
    # in a for loop (the bash keyword) used to trip `do \$` (2026-10-02).
    # `[$]` not `\$`: inside double quotes `\$` becomes a bare mid-pattern `$`
    # (an anchor) to grep -E. The tag may arrive shell-escaped as `\$`.
    if m "${L}do[[:space:]]+('|[\\\\]?[$][A-Za-z0-9_]*[\\\\]?[$]|language${R})" "$sql"; then
      MATCHED="do-block"
      deny "DO anonymous block against a READ-ONLY reference cluster (:65432/:65433/:65438). $REF_ESCAPE"
    fi
    # COPY/\copy: only `<target> FROM` is a write — `\copy (select ...) to`
    # has `from` inside the parenthesised query and is a read. The R boundary
    # already consumed the space after `copy`, so the target starts at once;
    # an optional ` (col, ...)` list may sit between it and FROM (2026-10-02).
    if m "${L}copy${R}[^[:space:]]+([[:space:]]*\([^)]*\))?[[:space:]]+from${R}" "$sql"; then
      MATCHED="copy-from"
      deny "COPY/\\copy ... FROM (a write) against a READ-ONLY reference cluster. $REF_ESCAPE"
    fi
  fi
  # kill / fuser -k / lsof -t | xargs kill aimed at a reference port.
  while IFS= read -r seg; do
    if mc "$REFPORT_RE" "$seg" || { [ "$REFENV" -eq 1 ] && m 'PORT|fuser|lsof' "$seg"; }; then
      if m "${LC}(kill|pkill|killall)${R}" "$seg" || m "${LC}fuser${R}[^;&|]*-[A-Za-z]*k" "$seg"; then
        MATCHED="kill-pipe"
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
  # No --status exemption: stop_goopg.sh / stop_pg.sh ignore argv and stop
  # the cluster unconditionally (verified 2026-10-02).
  if m "${LC}(stop_goopg|stop_pg)\.sh" "$seg" && ! is_reader "$seg"; then
    MATCHED="stop-script"
    deny "stop_goopg.sh / stop_pg.sh stop a shared reference cluster. Reference clusters are started only via scripts/ref-clusters-ensure.sh and never stopped by the loop. $REF_ESCAPE"
  fi
  if m "${LC}tpch-ref-recover\.sh" "$seg" && ! is_reader "$seg"; then
    MATCHED="recover-script"
    deny "scripts/tpch-ref-recover.sh is owner-only (reference-cluster recovery). Write an escalation block into the task and mark it [!]."
  fi
  if m "${LC}tpch-estimate-audit-arm\.sh" "$seg" && ! is_reader "$seg"; then
    MATCHED="audit-arm"
    root="${CLAUDE_PROJECT_DIR:-}"
    [ -n "$root" ] || root="$(git -C "${hook_cwd:-.}" rev-parse --show-toplevel 2>/dev/null)"
    if [ -n "$root" ] && [ -e "$root/bench/tpch/runtime_goopg/data.HOLD" ]; then
      deny "scripts/tpch-estimate-audit-arm.sh while bench/tpch/runtime_goopg/data.HOLD exists: the TPC-H goopg reference is on hold (owner recovery pending). Do not run it; mark dependent work [!] with the HOLD as the blocker."
    fi
  fi
done < <(segs "$nomsg")
if m "(setup_goopg|setup_pg)\.sh[^;&|]*--reset" "$nomsg"; then
  MATCHED="setup-reset"
  deny "setup_*.sh --reset wipes a shared reference cluster. $REF_ESCAPE"
fi
if m '(^|[^A-Za-z0-9_-])server\.sh[[:space:]]+(stop|restart)' "$nomsg"; then
  MATCHED="server-stop"
  deny "bench/tpcds/server.sh stop/restart stops shared TPC-DS clusters (incl. the :65438 PG reference). Use scripts/ref-clusters-ensure.sh to START servers; never stop them. $REF_ESCAPE"
fi
# Per command segment (split on ; & | newline) so a harmless `ls <refdir>`
# next to an unrelated `rm tmp/x` does not trip the rule. REFDIR checks run
# on nomsgd: paths under private roots (tmp/, /tmp/, analysis/, docs/,
# ci/logs/) are neutralised there, so e.g. `rm -rf /tmp/mirror/bench/...`
# or `ln -sfn ... /tmp/x/bench/tpch/runtime_goopg/data` is private work, not
# a reference-cluster write (2026-10-02).
while IFS= read -r seg; do
  m "$REFDIR" "$seg" || continue
  MATCHED="refdir:$(first_hit "$REFDIR" "$seg")"
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
done < <(segs "$nomsgd")
MATCHED=""
if writes_to "$REFDIR" "$nomsgd"; then
  MATCHED="refdir:$(first_hit "$REFDIR" "$nomsgd")"
  deny "write/copy-over/remove/chmod into a reference cluster data dir or a preloss clone (read-only copies FROM it are fine). $REF_ESCAPE"
fi
if writes_to "\.HOLD([\"'[:space:];&|)/]|$)" "$nomsgd"; then
  MATCHED="hold-file"
  deny "rm/mv/cp-over/truncate/redirect of a *.HOLD file: HOLD markers are placed and cleared by the owner only. $REF_ESCAPE"
fi
if m "$REFDIR" "$nomsgd" && m "${LC}kill${R}" "$nomsg" && m 'postmaster\.pid' "$nomsg"; then
  MATCHED="pid-kill"
  deny "kill of a reference cluster postmaster (PID from its postmaster.pid). $REF_ESCAPE"
fi
# rm/mv of an ancestor of a reference data dir.
if m '(^|[[:space:];&|(])(rm|mv)[[:space:]][^;&|]*(^|[[:space:]"'"'"'/])bench(/tpch(/runtime(_goopg)?)?|/tpcds(/runtime)?)?/?(["'"'"'[:space:];&|)]|$)' "$nomsgd"; then
  MATCHED="refdir-ancestor"
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
PROT="ralph-[A-Za-z0-9_-]*guard[A-Za-z0-9_.-]*|ralph_protected_regions\.py|ralph-githooks-test\.sh|\.githooks${HB}|\.claude/settings(\.local)?\.json|\.codex${HB}|\.devin${HB}|\.ralph/PROMPT\.md|\.ralph/gate-exceptions\.md|ref-clusters-ensure\.sh|lib/ref-clusters\.sh|tpch-ref-recover\.sh|lib/gate-stamp\.sh|(~|\\\$HOME|\\\$\{HOME\}|/home/[A-Za-z0-9_.-]+|/root)/\.ralph${HB}|\.git/(config|hooks)"
if writes_to "$PROT" "$nomsg"; then
  deny "shell write/rm/mv/chmod of a harness mechanism file (ralph guards, .githooks, .claude/settings(.local).json, .codex/ and .devin/ hook wiring, .ralph/PROMPT.md, .ralph/gate-exceptions.md, ref-cluster / gate-stamp plumbing, .git/config, ~/.ralph/). These are owner-only (M5): write an escalation block into the task and mark it [!]."
fi

exit 0
