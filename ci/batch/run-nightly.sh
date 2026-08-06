#!/bin/bash
# run-nightly.sh — nightly whole-suite regression batch orchestrator.
# Design: ci/design/ (01 architecture, 03 resources, 04 logging, 05 tpch, 06 locks).
#
# Single entrypoint (also `make nightly-batch`). Stage flow:
#   S0 preflight -> S1 [Lane L: units,race | Lane H: testport,pgbench] (parallel)
#   -> barrier -> S2 tpch (solo) -> S3 tpcds (solo) -> S4 summary.
#
# Exit codes (normative, ci/design/04 §D): 0 pass, 2 fail, 3 inconclusive,
# 4 aborted, 5 run lock already held.
#
# Env knobs: NIGHTLY_STAGES (csv subset, default all), NIGHTLY_GO_P (4),
# NIGHTLY_KEEP (14), NIGHTLY_PORT_WAIT (900), NIGHTLY_TPCH_BUDGET (7200),
# NIGHTLY_TPCH_QUERIES (test override, e.g. "12,13").
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BATCH_DIR="${REPO_ROOT}/ci/batch"
LOGS_DIR="${REPO_ROOT}/ci/logs"
export REPO_ROOT
export NIGHTLY_GO_P="${NIGHTLY_GO_P:-4}"
export NIGHTLY_PORT_WAIT="${NIGHTLY_PORT_WAIT:-900}"
NIGHTLY_KEEP="${NIGHTLY_KEEP:-14}"
NIGHTLY_STAGES="${NIGHTLY_STAGES:-preflight,units,race,testport,pgbench,tpch,tpcds,summary}"

# --- run lock (fd 8): manual + scheduled firings never overlap (design 06 §C)
LOCK_DIR="${HOME}/.ralph/locks"; mkdir -p "${LOCK_DIR}"
exec 8>"${LOCK_DIR}/goopg-nightly-run.lock"
if ! flock -n 8; then
    echo "nightly batch already running (lock: ${LOCK_DIR}/goopg-nightly-run.lock) — exiting" >&2
    exit 5
fi

# --- run dir FIRST, so even a failing build has a home (design 01 §S0 step 0)
RUN_ID="$(date +%Y%m%d-%H%M%S)"
RUN_DIR="${LOGS_DIR}/${RUN_ID}"
export RUN_DIR RUN_ID
mkdir -p "${RUN_DIR}/stages"
ln -sfn "${RUN_DIR}" "${LOGS_DIR}/latest"
source "${BATCH_DIR}/lib/common.sh"

want() { [[ ",${NIGHTLY_STAGES}," == *",$1,"* ]]; }

# --- meta.json ----------------------------------------------------------------
sha="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"
dirty="$(git -C "${REPO_ROOT}" status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
src_fp="$(source_fingerprint)"
export SOURCE_FP_START="${src_fp}"
python3 - "$RUN_DIR" "$RUN_ID" "$sha" "$dirty" "$src_fp" <<'PY'
import json, subprocess, sys, platform
run_dir, run_id, sha, dirty, src_fp = sys.argv[1:6]
try:
    gover = subprocess.run(["go", "version"], capture_output=True, text=True).stdout.strip()
except Exception:
    gover = "unknown"
import os, datetime
cfg = {k: os.environ.get(k) for k in
       ("NIGHTLY_STAGES", "NIGHTLY_GO_P", "NIGHTLY_PORT_WAIT", "NIGHTLY_KEEP",
        "NIGHTLY_TPCH_BUDGET", "NIGHTLY_TPCH_QUERIES", "NIGHTLY_TPCH_PORT",
        "NIGHTLY_PGBENCH_SCALE", "NIGHTLY_PGBENCH_CLIENTS",
        "NIGHTLY_PGBENCH_THREADS", "NIGHTLY_PGBENCH_T") if os.environ.get(k)}
try:
    dirty_list = subprocess.run(
        ["git", "status", "--porcelain"], capture_output=True, text=True
    ).stdout.splitlines()[:50]
except Exception:
    dirty_list = []
meta = {"run_id": run_id, "sha": sha, "dirty_files": int(dirty),
        "dirty_list": dirty_list, "source_fp": src_fp,
        "started": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "host": platform.node(), "go": gover, "config": cfg}
with open(f"{run_dir}/meta.json", "w") as f:
    json.dump(meta, f, indent=1)
PY

progress "RUN" "start run_id=${RUN_ID} sha=${sha:0:8} dirty=${dirty} src_fp=${src_fp} stages=${NIGHTLY_STAGES}"

# --- abort handling -------------------------------------------------------------
COMPLETED=0
LANE_PIDS=""
CUR_STAGE_PID=""

# kill_tree <pid>... — TERM a process and all its descendants (deepest first).
kill_tree() {
    local pid kids
    for pid in "$@"; do
        [[ -n "${pid}" ]] || continue
        kids="$(ps -o pid= --ppid "${pid}" 2>/dev/null | tr -d ' ')"
        [[ -n "${kids}" ]] && kill_tree ${kids}
        kill -TERM "${pid}" 2>/dev/null || true
    done
}

abort_cleanup() {
    local rc=$?
    if [[ ${COMPLETED} -eq 0 ]]; then
        progress "RUN" "ABORTED (rc=${rc}) — killing lanes/stages and stopping nightly scopes"
        # Kill the lane subshells and any in-flight stage FIRST — otherwise a
        # lane would keep starting new stages after the scopes are stopped.
        kill_tree ${LANE_PIDS} ${CUR_STAGE_PID}
        for u in goopg-nightly-units goopg-nightly-race goopg-nightly-testport goopg-nightly-pgbench goopg-nightly-tpch goopg-nightly-tpcds; do
            stop_scope "${u}"
        done
        # Minimal machine record so an aborted run is still accounted for.
        python3 - "$RUN_DIR" "$RUN_ID" <<'PY' 2>/dev/null || true
import json, sys
run_dir, run_id = sys.argv[1:3]
with open(f"{run_dir}/summary.json", "w") as f:
    json.dump({"run_id": run_id, "status": "aborted"}, f, indent=1)
PY
    fi
}
trap abort_cleanup EXIT
trap 'exit 4' INT TERM

# --- persist top-level batch logs to git on completion --------------------------
# Commit & push ONLY the top-level report files, via explicit pathspec —
# never `-A` / `.` / `commit -a` — so the concurrently-running ralph loop's
# unrelated working-tree WIP is never swept into this commit. Best-effort: any
# git failure is logged (to the run dir + a one-line progress note) but never
# changes the batch verdict. Contention with the ralph loop's own commits is
# absorbed by retrying around the shared .git index lock; the push is always a
# fast-forward (or up-to-date) because origin only ever advances from this
# single local clone.
commit_and_push_logs() {
    local glog="${RUN_DIR}/git-logs-push.log" branch try paths=() f
    for f in ci/logs/action-items.md ci/logs/launch.log ci/logs/scheduler.log ci/logs/history.jsonl; do
        [[ -e "${REPO_ROOT}/${f}" ]] && paths+=("${f}")
    done
    [[ ${#paths[@]} -gt 0 ]] || return 0
    branch="$(git -C "${REPO_ROOT}" rev-parse --abbrev-ref HEAD 2>/dev/null)"
    if [[ -z "${branch}" || "${branch}" == "HEAD" ]]; then
        progress "RUN" "log-push skipped (detached HEAD)"
        return 0
    fi

    : > "${glog}"
    # Stage only our three files (also picks up launch.log / scheduler.log the
    # first time, when they are newly un-ignored and still untracked).
    for try in 1 2 3 4 5; do
        git -C "${REPO_ROOT}" add -- "${paths[@]}" >>"${glog}" 2>&1 && break
        sleep 3
    done
    if git -C "${REPO_ROOT}" diff --cached --quiet -- "${paths[@]}" 2>/dev/null; then
        progress "RUN" "log-push: no changes in batch log files — nothing to commit"
        return 0
    fi
    for try in 1 2 3 4 5; do
        git -C "${REPO_ROOT}" commit \
            -m "chore(ci): nightly batch ${RUN_ID} logs (action-items, launch, scheduler, history)" \
            -- "${paths[@]}" >>"${glog}" 2>&1 && break
        sleep 3
    done
    local pushed=0
    for try in 1 2 3 4 5; do
        if git -C "${REPO_ROOT}" push origin "${branch}" >>"${glog}" 2>&1; then pushed=1; break; fi
        sleep 5
    done
    if [[ ${pushed} -eq 1 ]]; then
        progress "RUN" "log-push: committed + pushed batch logs to origin/${branch}"
    else
        progress "RUN" "log-push: commit done but push FAILED after retries — see ${glog#${REPO_ROOT}/}"
    fi
}

# --- stage runner ---------------------------------------------------------------
run_stage() {  # run_stage <name>  (records "<status> <elapsed>" in stages/<name>.status)
    local name="$1" t0=${SECONDS} rc=0 sp
    mkdir -p "${RUN_DIR}/${name}"
    # Stamp the tree this stage is about to compile against. A stage whose fp
    # differs from meta.json's `source_fp` ran on a DIFFERENT tree than
    # preflight validated, so its failures are unattributable (see
    # source_fingerprint in lib/common.sh). Written before the stage starts,
    # so it survives a stage that dies.
    source_fingerprint > "${RUN_DIR}/stages/${name}.fp" 2>/dev/null || true
    # Run the stage as a background child + wait: `wait` is interruptible, so
    # an INT/TERM to the orchestrator aborts promptly (mid-stage) instead of
    # being deferred to the stage boundary; abort_cleanup kills CUR_STAGE_PID.
    bash "${BATCH_DIR}/stages/stage-${name}.sh" \
        > "${RUN_DIR}/${name}/stage.log" 2>&1 &
    sp=$!
    CUR_STAGE_PID=${sp}
    wait "${sp}" || rc=$?
    CUR_STAGE_PID=""
    local elapsed=$(( SECONDS - t0 ))
    local status
    status="$(head -1 "${RUN_DIR}/stages/${name}.status" 2>/dev/null | awk '{print $1}')"
    if [[ -z "${status}" ]]; then
        status=$([[ ${rc} -eq 0 ]] && echo pass || echo fail)
    fi
    printf '%s %s\n' "${status}" "${elapsed}" > "${RUN_DIR}/stages/${name}.status"
    return ${rc}
}

# --- S0 -------------------------------------------------------------------------
build_failed=0
if want preflight; then
    if ! run_stage preflight; then
        build_failed=1
        progress "RUN" "preflight failed — skipping S1/S2, going straight to summary"
    fi
fi

# --- S1: two parallel lanes -------------------------------------------------------
if [[ ${build_failed} -eq 0 ]]; then
    lane_l() {
        if want units; then run_stage units || true; fi
        if want race;  then run_stage race  || true; fi
    }
    lane_h() {
        if want testport; then run_stage testport || true; fi
        if want pgbench;  then run_stage pgbench  || true; fi
    }
    progress "RUN" "S1 lanes start (L: units,race | H: testport,pgbench — filtered by NIGHTLY_STAGES)"
    lane_l & pid_l=$!
    lane_h & pid_h=$!
    LANE_PIDS="${pid_l} ${pid_h}"
    wait "${pid_l}" "${pid_h}"
    LANE_PIDS=""
    progress "RUN" "S1 barrier reached"

    # --- S2: TPC-H solo ---------------------------------------------------------
    if want tpch; then
        run_stage tpch || true
    fi

    # --- S3: TPC-DS solo --------------------------------------------------------
    if want tpcds; then
        run_stage tpcds || true
    fi
fi

# --- S4: summary (its exit code is the batch verdict) -----------------------------
final_rc=0
if want summary; then
    run_stage summary || final_rc=$?
else
    # No summary stage requested (test subsets): the verdict still must not
    # go green over a red stage — derive it from the stage statuses.
    for f in "${RUN_DIR}"/stages/*.status; do
        [[ -f "${f}" ]] || continue
        if head -1 "${f}" | grep -q '^fail'; then final_rc=2; fi
    done
fi

# The verdict is final from here on — a signal during retention must not
# overwrite the real summary.json with an "aborted" one.
COMPLETED=1

# retention: keep newest NIGHTLY_KEEP run dirs (never touches scheduler/launch/history)
( cd "${LOGS_DIR}" 2>/dev/null && \
  ls -1d 2*/ 2>/dev/null | sort -r | tail -n "+$(( NIGHTLY_KEEP + 1 ))" | \
  while read -r d; do rm -rf -- "${d}"; done ) || true

progress "RUN" "end status_rc=${final_rc} (0 pass / 2 fail / 3 inconclusive) — see summary.md"

# Publish the top-level batch logs/report (must run AFTER the final progress
# line above so the committed launch.log includes this run's "end" record).
commit_and_push_logs || true

exit ${final_rc}
