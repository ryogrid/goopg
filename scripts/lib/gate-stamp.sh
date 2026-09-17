#!/usr/bin/env bash
# gate-stamp.sh — machine-written gate result stamps (METHODLOGY3 04-actions H5).
#
# A gate that only prints PASS to a terminal leaves nothing a hook can check a
# commit against. Each gate script sources this file and, from its EXIT trap,
# writes tmp/gate-stamps/<gate>.json:
#
#   {"gate": "...", "tree": "<git write-tree>", "head": "<HEAD sha>",
#    "code_tree": "<sha256 of `git ls-files -s -- internal cmd go.mod go.sum`>",
#    "dirty_code": true|false,
#    "binary_sha256": "<sha256 of the engine binary, or empty>",
#    "result": "PASS|FAIL|FAIL-TIMEOUT|NO-COMPARE|SKIP-BLOCKED|SKIP",
#    "reason": "<why this is not a PASS; always set for FAIL/FAIL-TIMEOUT/
#                NO-COMPARE/SKIP-BLOCKED, from the caller or a default>",
#    "time": "<date -Iseconds>"}
#
# `code_tree` is INDEX-based (the staged blob ids of the engine source), so it
# names exactly what a commit of the current index would contain. `dirty_code`
# is true when the WORKTREE differs from the index under those paths
# (`git diff --quiet -- internal cmd go.mod go.sum` fails): the gate's
# `go build` compiled the worktree, not the index, so its verdict is not
# evidence for the tree being stamped — the result is then forced to FAIL with
# reason "unstaged code changes: gate built a tree that is not the index".
#
# `tree` is `git write-tree`, i.e. the tree of the INDEX at the time the gate
# finished — that is the contract: a commit-msg hook compares it with the
# tree being committed, so stage first, then run the gate.
#
# Gate names in use: tpch-spotcheck, tpch-acceptance-arm, tpcds-sf025.
#
# The write is atomic (temp file in the same dir + mv) and never fails the
# caller: a stamp problem is reported on stderr and swallowed, so it cannot
# turn a gate's own verdict into something else.

_gate_stamp_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
: "${GATE_STAMP_ROOT:=$(cd "${_gate_stamp_lib_dir}/../.." && pwd)}"

# _gate_stamp_json_str <s> — minimal JSON string escaping (\ and ").
_gate_stamp_json_str() {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    printf '"%s"' "${s}"
}

# gate_stamp_write <gate> <result> [binary_path] [reason]
# The 4th argument, or GATE_STAMP_REASON, is recorded as "reason": WHY the
# result is not a PASS. Every non-PASS/non-SKIP result gets one — when the
# caller passes none, a generic per-result default is filled in, so an audit
# never reads a bare "FAIL"/"SKIP-BLOCKED" with an empty reason field (the
# commit-msg hook quotes it back when it rejects a commit). The dirty_code
# override replaces whatever the caller passed.
_gate_stamp_default_reason() { # <gate> <result>
    case "${2}" in
        SKIP-BLOCKED) printf '%s' "${1}: a precondition of the gate is unavailable (blocked); the gate did not compare anything. A blocked gate is a failed gate unless an owner row in .ralph/gate-exceptions.md covers it." ;;
        FAIL)         printf '%s' "${1}: gate reported FAIL (non-zero exit); see the gate's own log for the failing case." ;;
        FAIL-TIMEOUT) printf '%s' "${1}: at least one query exceeded the gate timeout; a timeout is a FAIL, not a slow PASS." ;;
        NO-COMPARE)   printf '%s' "${1}: gate exited 0 without comparing against its baseline (subset probe, missing baseline, or digest off), so it is not evidence for this tree." ;;
        *)            printf '%s' "" ;;
    esac
}

gate_stamp_write() {
    local gate="${1:-}" result="${2:-}" bin="${3:-}" argreason="${4:-}"
    local dir tree head sha="" now tmpf code_tree dirty_code=false reason="${argreason:-${GATE_STAMP_REASON:-}}" orig="${2:-}"
    case "${result}" in
        PASS|FAIL|FAIL-TIMEOUT|NO-COMPARE|SKIP-BLOCKED|SKIP) ;;
        *) echo "gate-stamp: invalid result '${result}' for gate '${gate}' — not stamped" >&2; return 0 ;;
    esac
    [[ "${gate}" =~ ^[A-Za-z0-9._-]+$ ]] || {
        echo "gate-stamp: invalid gate name '${gate}' — not stamped" >&2; return 0; }
    # Fill the reason for every non-PASS result the caller left unexplained.
    if [[ -z "${reason}" ]]; then
        reason="$(_gate_stamp_default_reason "${gate}" "${result}")"
    fi
    dir="${GATE_STAMP_DIR:-${GATE_STAMP_ROOT}/tmp/gate-stamps}"
    mkdir -p "${dir}" 2>/dev/null || {
        echo "gate-stamp: cannot create ${dir} — not stamped" >&2; return 0; }
    tree="$(git -C "${GATE_STAMP_ROOT}" write-tree 2>/dev/null || true)"
    head="$(git -C "${GATE_STAMP_ROOT}" rev-parse HEAD 2>/dev/null || true)"
    code_tree="$(git -C "${GATE_STAMP_ROOT}" ls-files -s -- internal cmd go.mod go.sum 2>/dev/null \
        | sha256sum | cut -d' ' -f1)"
    if ! git -C "${GATE_STAMP_ROOT}" diff --quiet -- internal cmd go.mod go.sum 2>/dev/null; then
        dirty_code=true
        result=FAIL
        reason="unstaged code changes: gate built a tree that is not the index"
        [[ "${orig}" != "FAIL" ]] && reason="${reason} (gate verdict was ${orig})"
        echo "gate-stamp: ${gate}: ${reason} — stamping FAIL" >&2
    fi
    if [[ -n "${bin}" && -f "${bin}" ]]; then
        sha="$(sha256sum "${bin}" 2>/dev/null | awk '{print $1}')"
    fi
    now="$(date -Iseconds)"
    tmpf="$(mktemp "${dir}/.${gate}.json.XXXXXX" 2>/dev/null)" || {
        echo "gate-stamp: mktemp failed in ${dir} — not stamped" >&2; return 0; }
    {
        printf '{"gate": %s, "tree": %s, "head": %s, "code_tree": %s, "dirty_code": %s, "binary_sha256": %s, "result": %s, "reason": %s, "time": %s}\n' \
            "$(_gate_stamp_json_str "${gate}")" "$(_gate_stamp_json_str "${tree}")" \
            "$(_gate_stamp_json_str "${head}")" "$(_gate_stamp_json_str "${code_tree}")" \
            "${dirty_code}" "$(_gate_stamp_json_str "${sha}")" \
            "$(_gate_stamp_json_str "${result}")" "$(_gate_stamp_json_str "${reason}")" \
            "$(_gate_stamp_json_str "${now}")"
    } >"${tmpf}" && chmod 0644 "${tmpf}" && mv -f "${tmpf}" "${dir}/${gate}.json" || {
        rm -f "${tmpf}"
        echo "gate-stamp: write failed for ${dir}/${gate}.json" >&2
        return 0
    }
    echo "gate-stamp: ${gate} ${result} -> ${dir}/${gate}.json" >&2
    return 0
}

# gate_stamp_result_for_rc <rc> [skip_blocked_rc] [skip_flag]
# Maps an exit code to a result: 0 -> PASS (or SKIP when skip_flag=1),
# <skip_blocked_rc> -> SKIP-BLOCKED, anything else -> FAIL.
gate_stamp_result_for_rc() {
    local rc="$1" blocked_rc="${2:-}" skip_flag="${3:-0}"
    if [[ "${rc}" == "0" ]]; then
        [[ "${skip_flag}" == "1" ]] && echo SKIP || echo PASS
    elif [[ -n "${blocked_rc}" && "${rc}" == "${blocked_rc}" ]]; then
        echo SKIP-BLOCKED
    else
        echo FAIL
    fi
}
