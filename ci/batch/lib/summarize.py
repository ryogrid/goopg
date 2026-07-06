#!/usr/bin/env python3
"""summarize.py — S3 classification for the nightly batch.

Reads the run dir's stage outputs, joins them against the repo's authority
CSVs, and emits:
  <run>/summary.json, <run>/summary.md   (ci/design/04 §D)
  ci/logs/action-items.md                (ci/design/07 §A — stable path)
  ci/logs/history.jsonl                  (one condensed line per run)

Exit code IS the batch verdict: 0 pass / 2 fail / 3 inconclusive.

Classification (ci/design/02 §B):
  - any go-test FAIL in units/race/testport            -> regression
  - regress mismatch-skip of a baseline-`pass` case    -> regression
  - pgbench failed txns / stage fail                   -> regression
  - tpch spotcheck mismatch / query error / row-anchor -> regression
  - tpch timeout / not-run(budget) / total > 2x base   -> perf-drastic (fail)
  - build failure                                      -> regression (fail(build))
  - signal-9 kill with no test FAIL                    -> resource-kill (inconclusive)
  - expected-fail (ci/batch/expected-failures.csv) hit -> informational
  - expected-fail PASS                                 -> promotable notice
"""

import argparse
import csv
import json
import os
import re
import sys
from datetime import datetime, timezone

TPCH_BASELINE_TOTAL_S = 1469  # tpch_power_test_20260526.md; 2x rule per design 04 §C


# --------------------------------------------------------------------------- io
def read_file(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read()
    except OSError:
        return ""


def read_csv(path):
    try:
        with open(path, newline="", encoding="utf-8") as f:
            return list(csv.DictReader(f))
    except OSError:
        return []


def read_stage_statuses(run_dir):
    out = {}
    d = os.path.join(run_dir, "stages")
    if not os.path.isdir(d):
        return out
    for fn in os.listdir(d):
        if not fn.endswith(".status"):
            continue
        parts = read_file(os.path.join(d, fn)).split()
        name = fn[: -len(".status")]
        status = parts[0] if parts else "unknown"
        elapsed = int(parts[1]) if len(parts) > 1 and parts[1].isdigit() else None
        out[name] = {"status": status, "elapsed_s": elapsed}
    return out


# ------------------------------------------------------------------ go test -v
RUN_RE = re.compile(r"^=== RUN\s+(\S+)")
CONT_RE = re.compile(r"^=== CONT\s+(\S+)")
PAUSE_RE = re.compile(r"^=== PAUSE\s+(\S+)")
RESULT_RE = re.compile(r"^\s*--- (PASS|FAIL|SKIP): (\S+) \(([\d.]+)s\)")
PKG_FAIL_RE = re.compile(r"^FAIL\s+(\S+)(\s|$)")


def parse_go_test(log_text):
    """Return (results: name -> (status, elapsed), skip_reasons, failed_pkgs).

    Attribution of body lines follows `=== RUN` / `=== CONT` (t.Parallel tests
    resume via CONT; attributing to the last RUN would mix reasons across
    interleaved parallel subtests — the isolation suite is parallel).
    """
    results, skip_reasons, failed_pkgs = {}, {}, []
    current = None
    pending = {}  # name -> accumulated log text while the (sub)test runs
    for line in log_text.splitlines():
        m = RUN_RE.match(line) or CONT_RE.match(line)
        if m:
            current = m.group(1)
            pending.setdefault(current, [])
            continue
        if PAUSE_RE.match(line):
            current = None
            continue
        m = RESULT_RE.match(line)
        if m:
            status, name, elapsed = m.group(1), m.group(2), m.group(3)
            results[name] = (status, elapsed)
            if status == "SKIP" and pending.get(name):
                skip_reasons[name] = "\n".join(pending[name])
            continue
        m = PKG_FAIL_RE.match(line)
        if m:
            failed_pkgs.append(m.group(1))
            continue
        if current is not None:
            pending.setdefault(current, []).append(line.strip())
    return results, skip_reasons, failed_pkgs


def looks_resource_killed(log_text):
    return bool(re.search(r"signal: killed|Killed\b|exit status 137", log_text))


# -------------------------------------------------------------------- tpch csv
def parse_timings(path):
    rows = []
    for r in read_csv(path):
        rows.append(
            {
                "query": r.get("query", ""),
                "elapsed_s": float(r["elapsed_s"]) if r.get("elapsed_s") else None,
                "rows": int(r["rows"]) if r.get("rows") else None,
                "status": r.get("status", ""),
            }
        )
    return rows


# ------------------------------------------------------------------- item model
class Items:
    def __init__(self):
        self.regressions = []      # gating -> fail
        self.perf_drastic = []     # gating -> fail
        self.resource_kills = []   # -> inconclusive (when no regressions)
        self.known_fails = []      # informational
        self.promotable = []       # notices
        self.notes = []            # informational

    def add_regression(self, subject, what, repro, evidence, kind="regression"):
        self.regressions.append(
            {"kind": kind, "subject": subject, "what": what, "repro": repro, "evidence": evidence}
        )

    def add_perf(self, subject, what, repro, evidence):
        self.perf_drastic.append(
            {"kind": "perf-drastic", "subject": subject, "what": what, "repro": repro, "evidence": evidence}
        )


# --------------------------------------------------------------------- analyze
def analyze(run_dir, repo_root, run_id):
    it = Items()
    stages = read_stage_statuses(run_dir)
    ev = lambda rel: f"ci/logs/{run_id}/{rel}"

    expected_rows = read_csv(os.path.join(repo_root, "ci/batch/expected-failures.csv"))
    expected_ids = [r["case_id"] for r in expected_rows if r.get("case_id")]

    def is_expected(name_or_subject):
        # Match both the raw case id and its .spec-stripped stem — Go test
        # names carry the stem ("TestPort_IsolationSuite/deadlock-parallel"),
        # not the ".spec" file name.
        for eid in expected_ids:
            if eid in name_or_subject or eid.removesuffix(".spec") in name_or_subject:
                return eid
        return None

    # ---- preflight / build
    if stages.get("preflight", {}).get("status", "").startswith("fail"):
        it.add_regression(
            "build", "make build failed in preflight",
            "make build", ev("preflight/build.log"),
        )

    # ---- units / race (package-level)
    for stage, repro_tpl in (
        ("units", "go test -timeout 10m ./{pkg}/"),
        ("race", "go test -race -timeout 15m ./{pkg}/"),
    ):
        st = stages.get(stage, {}).get("status", "")
        if not st.startswith("fail"):
            continue
        log = read_file(os.path.join(run_dir, stage, "go-test.log"))
        if looks_resource_killed(log) and "--- FAIL" not in log:
            it.resource_kills.append({"stage": stage, "evidence": ev(f"{stage}/go-test.log")})
            continue
        _, _, failed_pkgs = parse_go_test(log)
        pkgs = failed_pkgs[:10] or ["(suite)"]
        for pkg in pkgs:
            rel = pkg.split("github.com/goopg/goopg/")[-1] if pkg != "(suite)" else ""
            it.add_regression(
                f"{stage}/{rel or 'suite'}",
                f"{stage} suite failed" + (f" in package {pkg}" if rel else ""),
                repro_tpl.format(pkg=rel) if rel else f"see {stage} stage command in ci/batch/stages/stage-{stage}.sh",
                ev(f"{stage}/go-test.log"),
            )

    # ---- testport
    tp_log = read_file(os.path.join(run_dir, "testport", "go-test.log"))
    tp_results, tp_skip_reasons, tp_failed_pkgs = parse_go_test(tp_log)
    tp_status = stages.get("testport", {}).get("status", "")
    if tp_status.startswith("fail") and looks_resource_killed(tp_log) and "--- FAIL" not in tp_log:
        it.resource_kills.append({"stage": "testport", "evidence": ev("testport/go-test.log")})
    # Compile failure ("FAIL <pkg> [build failed]") has no --- FAIL lines.
    if tp_status.startswith("fail") and "[build failed]" in tp_log:
        it.add_regression(
            "testport/build",
            "internal/testport failed to COMPILE — the whole oracle suite did not run",
            "go vet ./internal/testport/ && go test -run xxx ./internal/testport/",
            ev("testport/go-test.log"),
        )

    # Per-test results.csv (doc 04 §A layout).
    if tp_results:
        try:
            with open(os.path.join(run_dir, "testport", "results.csv"), "w") as f:
                f.write("name,status,elapsed_s\n")
                for name in sorted(tp_results):
                    s, el = tp_results[name]
                    f.write(f"{name},{s},{el}\n")
        except OSError:
            pass

    top_fails = sorted(
        n for n, s in tp_results.items() if s[0] == "FAIL" and "/" not in n
    )
    for name in top_fails:
        subj = f"testport/{name}"
        exp = is_expected(name)
        subfails = sorted(n for n, s in tp_results.items() if s[0] == "FAIL" and n.startswith(name + "/"))
        what = f"testport {name} FAILed"
        if subfails:
            what += " (subtests: " + ", ".join(s.split("/", 1)[1] for s in subfails[:8]) + ")"
        if exp:
            it.known_fails.append({"subject": subj, "case_id": exp, "what": what})
        else:
            it.add_regression(subj, what, f"go test -v -run '^{name}$' ./internal/testport/", ev("testport/go-test.log"))

    # regress mismatch-skip join (design 02 §A): a diverging regress case is a
    # t.Skip("deferred: output mismatch...") — join against the baseline CSV.
    baseline = {
        r["name"]: r["status"]
        for r in read_csv(os.path.join(repo_root, "docs/test-port/regress-diff-baseline.csv"))
        if r.get("name")
    }
    for name, status in sorted(tp_results.items()):
        if status[0] != "SKIP" or not name.startswith("TestPort_RegressSuite/"):
            continue
        case = name.split("/", 1)[1]
        reason = tp_skip_reasons.get(name, "")
        diverged = (
            "output mismatch" in reason
            or "execution error" in reason
            or "no result returned" in reason
        )
        if diverged and baseline.get(case) == "pass":
            exp = is_expected(case)
            item = {
                "subject": f"regress/{case}",
                "what": f"regress case {case} (baseline pass) diverged: {reason.splitlines()[-1] if reason else 'output mismatch'}",
            }
            if exp:
                it.known_fails.append({**item, "case_id": exp})
            else:
                it.add_regression(
                    item["subject"], item["what"],
                    f"go test -v -run 'TestPort_RegressSuite/{case}' ./internal/testport/",
                    ev("testport/go-test.log"),
                )

    # promotable: an expected-fail case that PASSed
    for eid in expected_ids:
        stem = eid.removesuffix(".spec")
        for name, status in tp_results.items():
            if status[0] == "PASS" and (eid in name or stem in name):
                it.promotable.append({"case_id": eid, "test": name})
                break

    # ---- pgbench (nightly heavy params: s=50 c=100 j=20 T=180 x3 workloads)
    if stages.get("pgbench", {}).get("status", "").startswith("fail"):
        log = read_file(os.path.join(run_dir, "pgbench", "pgbench.log")) + \
              read_file(os.path.join(run_dir, "pgbench", "server.log"))
        if looks_resource_killed(log):
            it.resource_kills.append({"stage": "pgbench", "evidence": ev("pgbench/pgbench.log")})
        else:
            failed = sum(int(m) for m in re.findall(r"number of failed transactions: (\d+)", log))
            first_err = next(
                (l.strip() for l in log.splitlines() if "pgbench: error:" in l), ""
            )
            what = f"pgbench nightly stage failed ({failed} failed transactions"
            what += f"; first error: {first_err[:160]})" if first_err else ")"
            it.add_regression(
                "pgbench/nightly", what,
                "REPO_ROOT=$PWD RUN_DIR=$(mktemp -d) bash ci/batch/stages/stage-pgbench.sh  # s=50 c=100 j=20 T=180",
                ev("pgbench/pgbench.log"),
            )

    # ---- tpch
    tpch_repro_note = "# start a server on bench/tpch/runtime_goopg/data (65433, when free) or a copy first — ci/design/05-tpch-stage.md §A (superuser@postgres)"
    tpch_status = stages.get("tpch", {}).get("status", "")
    spot = read_file(os.path.join(run_dir, "tpch", "spotcheck.log"))
    spot_line = next((l for l in spot.splitlines() if l.startswith("SPOTCHECK:")), "")
    if spot_line.startswith("SPOTCHECK: FAIL"):
        it.add_regression(
            "tpch/spotcheck", f"TPC-H spotcheck row-count mismatch ({spot_line})",
            f"tmp/tpch-nightly-runner -port 65433 -db postgres -user <superuser> -queries 12,13 -per-query-timeout 600s   {tpch_repro_note}",
            ev("tpch/spotcheck.log"),
        )

    # Anchors: `structural`/`pinned` rows GATE; `load-dependent` rows (measured
    # on a pre-reload dataset) only produce notes until re-pinned (design 05 §E).
    anchor_rows = read_csv(os.path.join(repo_root, "ci/batch/tpch-row-anchors.csv"))
    anchors = {
        r["query"]: (int(r["rows"]), r.get("kind", "structural"))
        for r in anchor_rows
        if r.get("query") and r.get("rows")
    }
    timings = parse_timings(os.path.join(run_dir, "tpch", "timings.csv"))
    completed = [t for t in timings if t["status"] == "ok"]
    for t in timings:
        q = t["query"]
        repro = (
            f"tmp/tpch-nightly-runner -port 65433 -db postgres -user <superuser> "
            f"-queries {q.lstrip('Q')} -per-query-timeout 1200s   {tpch_repro_note}"
        )
        anchor = anchors.get(q)
        if t["status"] == "ok" and anchor and t["rows"] is not None and t["rows"] != anchor[0]:
            if anchor[1] == "load-dependent":
                it.notes.append(
                    f"tpch {q}: rows={t['rows']} vs pre-reload anchor {anchor[0]} "
                    f"(load-dependent; verify & re-pin in ci/batch/tpch-row-anchors.csv)"
                )
            else:
                it.add_regression(
                    f"tpch/{q}-rows",
                    f"{q} returned {t['rows']} rows, anchor is {anchor[0]} ({anchor[1]}; ci/batch/tpch-row-anchors.csv)",
                    repro, ev("tpch/timings.csv"),
                )
        elif t["status"] == "error":
            it.add_regression(f"tpch/{q}-error", f"{q} errored during the sweep", repro, ev("tpch/run.log"))
        elif t["status"] == "timeout":
            it.add_perf(f"tpch/{q}-timeout", f"{q} hit its per-query budget (57014/cancel)", repro, ev("tpch/run.log"))
        elif t["status"].startswith("not-run"):
            it.add_perf(f"tpch/{q}-not-run", f"{q} not run: total budget exhausted", repro, ev("tpch/timings.csv"))
    total_elapsed = sum(t["elapsed_s"] or 0 for t in timings)
    if len(completed) == len(timings) and timings and total_elapsed > 2 * TPCH_BASELINE_TOTAL_S:
        it.add_perf(
            "tpch/total-time",
            f"sweep total {total_elapsed:.0f}s > 2x baseline ({TPCH_BASELINE_TOTAL_S}s) with all queries completed",
            "see ci/logs/latest/tpch/timings.csv", ev("tpch/timings.csv"),
        )
    # EXPLAIN errors are gating (a query that can't plan can't run — doc 05 §C).
    explain_log = read_file(os.path.join(run_dir, "tpch", "explain-run.log"))
    for m in re.finditer(r"^(Q\S+): ERROR ", explain_log, re.M):
        it.add_regression(
            f"tpch/{m.group(1)}-explain",
            f"EXPLAIN {m.group(1)} errored during the plan-capture pass",
            f"tmp/tpch-nightly-runner -port 65433 -db postgres -user <superuser> -queries {m.group(1).lstrip('Q').split('-')[0].rstrip('ab')} -explain -per-query-timeout 60s   {tpch_repro_note}",
            ev("tpch/explain-run.log"),
        )

    if tpch_status in ("fail(startup)", "fail(probe)", "fail(build)"):
        it.add_regression(
            "tpch/stage", f"tpch stage failed before queries ran ({tpch_status})",
            "bash ci/batch/stages/stage-tpch.sh (with RUN_DIR/REPO_ROOT set; see stage.log)",
            ev("tpch/stage.log"),
        )
    # tpch resource-kill: a kill signature in the stage/server logs with a
    # failed status is host/cgroup OOM, not a product regression (doc 03 §C).
    if tpch_status.startswith("fail"):
        tpch_all_logs = "".join(
            read_file(os.path.join(run_dir, "tpch", f))
            for f in ("stage.log", "server.log", "run.log")
        )
        if looks_resource_killed(tpch_all_logs):
            it.resource_kills.append({"stage": "tpch", "evidence": ev("tpch/server.log")})

    # ---- skips / plan-drift notes
    for name, stg in sorted(stages.items()):
        if stg["status"].startswith("skip"):
            it.notes.append(f"stage {name}: {stg['status']}")
    plan_log = read_file(os.path.join(run_dir, "tpch", "plan-gate.log"))
    if plan_log and re.search(r"differs|mismatch|changed", plan_log, re.I):
        it.notes.append("plan-drift: structural plan diff vs m0077-final reported differences (informational; see tpch/plan-gate.log)")

    # must-pass presence check (warning only): port/yes rows whose pinned Go
    # func never appeared in the testport log. Aggregated to one note so a
    # partial run (NIGHTLY_STAGES subset / early abort) doesn't flood notices.
    if tp_results:
        missing = []
        for r in read_csv(os.path.join(repo_root, "docs/test-port/postgres-oracle-port-status.csv")):
            if r.get("status") == "port" and r.get("pass_required") == "yes":
                funcs = re.findall(r"Test(?:Port|E2E)_\w+", r.get("rationale", ""))
                if funcs and not any(f in tp_results for f in funcs):
                    missing.append(f"{r.get('id')}:{funcs[0]}")
        if missing:
            head = ", ".join(missing[:5])
            more = f" (+{len(missing) - 5} more)" if len(missing) > 5 else ""
            it.notes.append(
                f"must-pass presence warning: {len(missing)} port/yes rows' funcs "
                f"not found in testport output — {head}{more} "
                f"(expected on partial NIGHTLY_STAGES runs; investigate on a full run)"
            )

    # ---- CATCH-ALL: a failed stage must never vanish from the verdict.
    # If a stage reports fail* but nothing above produced an item for it
    # (compile failure, -timeout panic, script death before logs), add a
    # generic regression so the batch can NEVER go green over a red stage.
    covered = {i2["subject"].split("/")[0] for i2 in it.regressions + it.perf_drastic}
    covered |= {rk["stage"] for rk in it.resource_kills}
    stage_prefixes = {
        "preflight": {"build", "preflight"},
        "units": {"units"},
        "race": {"race"},
        "testport": {"testport", "regress"},
        "pgbench": {"pgbench"},
        "tpch": {"tpch"},
    }
    for name, stg in stages.items():
        if name == "summary" or not stg["status"].startswith("fail"):
            continue
        if covered & stage_prefixes.get(name, {name}):
            continue
        it.add_regression(
            f"{name}/stage",
            f"stage {name} FAILED ({stg['status']}) with no parseable cause — inspect the stage logs",
            f"bash ci/batch/stages/stage-{name}.sh (REPO_ROOT/RUN_DIR set; see ci/logs/<run>/{name}/)",
            ev(f"{name}/stage.log"),
        )

    # ---- extras for the summary schema / env-drift
    pg_log = read_file(os.path.join(run_dir, "pgbench", "pgbench.log"))
    tp_skips_top = sorted(n for n, s in tp_results.items() if s[0] == "SKIP" and "/" not in n)
    extra = {
        "testport_counts": {
            "pass": sum(1 for s in tp_results.values() if s[0] == "PASS"),
            "fail": sum(1 for s in tp_results.values() if s[0] == "FAIL"),
            "skip": sum(1 for s in tp_results.values() if s[0] == "SKIP"),
        } if tp_results else None,
        "testport_skips_top": tp_skips_top,
        "pgbench_failed_txns": sum(
            int(m) for m in re.findall(r"number of failed transactions: (\d+)", pg_log)
        ) if pg_log else None,
        "tpch_timeouts": sum(1 for t in timings if t["status"] == "timeout"),
        "tpch_not_run": sum(1 for t in timings if t["status"].startswith("not-run")),
    }
    return it, stages, timings, spot_line, extra


# --------------------------------------------------------------------- outputs
def load_prev(logs_dir, run_id):
    """Previous run's summary.json (for first-seen) — newest run dir < run_id.

    Tolerates a corrupt/truncated previous summary (SIGKILL mid-dump) — a bad
    prior night must not break every subsequent summarize.
    """
    try:
        runs = sorted(
            d for d in os.listdir(logs_dir)
            if re.match(r"^\d{8}-\d{6}$", d) and d < run_id
        )
        if runs:
            return json.loads(read_file(os.path.join(logs_dir, runs[-1], "summary.json")) or "{}")
    except (OSError, ValueError):
        pass
    return {}


def prev_inconclusive(logs_dir):
    lines = read_file(os.path.join(logs_dir, "history.jsonl")).strip().splitlines()
    if not lines:
        return False
    try:
        return json.loads(lines[-1]).get("status") == "inconclusive"
    except json.JSONDecodeError:
        return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--run-dir", required=True)
    ap.add_argument("--repo-root", required=True)
    args = ap.parse_args()
    run_dir = os.path.abspath(args.run_dir)
    repo_root = os.path.abspath(args.repo_root)
    logs_dir = os.path.dirname(run_dir)
    run_id = os.path.basename(run_dir)
    try:
        meta = json.loads(read_file(os.path.join(run_dir, "meta.json")) or "{}")
    except ValueError:
        meta = {}
    now = datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds")

    it, stages, timings, spot_line, extra = analyze(run_dir, repo_root, run_id)

    gating = it.regressions + it.perf_drastic
    if gating:
        status = "fail"
    elif it.resource_kills:
        status = "inconclusive"
    else:
        status = "pass"

    # [investigate] on 2 consecutive inconclusive runs (design 07 §A)
    investigate = status == "inconclusive" and prev_inconclusive(logs_dir)

    prev = load_prev(logs_dir, run_id)
    prev_subjects = {
        i.get("subject") for i in prev.get("regressions", []) + prev.get("perf_drastic", [])
    }

    # env-drift: testport top-level tests newly skipped vs the previous run
    # (a tool that was present last night has gone missing — informational).
    prev_skips = set(prev.get("testport_skips_top") or [])
    if prev_skips and extra["testport_skips_top"]:
        for name in extra["testport_skips_top"]:
            if name not in prev_skips:
                it.notes.append(f"env-drift: {name} newly SKIPPED (was not skipped in the previous run)")

    # ---- summary.json
    spot_m = re.search(r"q12=(\d+) q13=(\d+)", spot_line or "")
    total_s = round(sum(t["elapsed_s"] or 0 for t in timings), 1)
    summary = {
        "run_id": run_id,
        "sha": meta.get("sha", "unknown"),
        "dirty_files": meta.get("dirty_files", 0),
        "generated": now,
        "status": status,
        "stages": stages,
        "regressions": it.regressions,
        "perf_drastic": it.perf_drastic,
        "resource_kills": it.resource_kills,
        "known_fails": it.known_fails,
        "promotable": it.promotable,
        "notes": it.notes,
        "testport_counts": extra["testport_counts"],
        "testport_skips_top": extra["testport_skips_top"],
        "pgbench_failed_txns": extra["pgbench_failed_txns"],
        "tpch": {
            "spotcheck": spot_line,
            "q12": int(spot_m.group(1)) if spot_m else None,
            "q13": int(spot_m.group(2)) if spot_m else None,
            "completed": sum(1 for t in timings if t["status"] == "ok"),
            "of": len(timings),
            "timeouts": extra["tpch_timeouts"],
            "not_run": extra["tpch_not_run"],
            "total_s": total_s,
            "total_vs_baseline": round(total_s / TPCH_BASELINE_TOTAL_S, 2) if timings else None,
        },
    }
    with open(os.path.join(run_dir, "summary.json"), "w") as f:
        json.dump(summary, f, indent=1)

    # ---- summary.md
    md = [f"# Nightly run {run_id} — **{status.upper()}**", ""]
    md.append(f"sha `{summary['sha'][:12]}` dirty={summary['dirty_files']} generated {now}")
    md.append("")
    md.append("| stage | status | elapsed_s |")
    md.append("|-------|--------|----------:|")
    for name in ("preflight", "units", "race", "testport", "pgbench", "tpch", "summary"):
        if name in stages:
            s = stages[name]
            md.append(f"| {name} | {s['status']} | {s['elapsed_s'] if s['elapsed_s'] is not None else ''} |")
    for title, rows in (
        ("Regressions", it.regressions),
        ("Perf-drastic", it.perf_drastic),
    ):
        md.append("")
        md.append(f"## {title} ({len(rows)})")
        for r in rows:
            md.append(f"- **{r['subject']}** — {r['what']}  \n  repro: `{r['repro']}`  \n  evidence: {r['evidence']}")
        if not rows:
            md.append("- none")
    if timings:
        md.append("")
        md.append("## TPC-H timings")
        md.append("| query | elapsed_s | rows | status |")
        md.append("|-------|----------:|-----:|--------|")
        for t in timings:
            md.append(
                f"| {t['query']} | {t['elapsed_s'] if t['elapsed_s'] is not None else ''} "
                f"| {t['rows'] if t['rows'] is not None else ''} | {t['status']} |"
            )
    for title, rows, render in (
        ("Promotable", it.promotable, lambda r: f"- {r['case_id']} PASSED ({r['test']}) — consider promotion (ci/design/02 §D)"),
        ("Known-fails hit", it.known_fails, lambda r: f"- {r['subject']} ({r.get('case_id','')})"),
        ("Resource kills", it.resource_kills, lambda r: f"- stage {r['stage']} — check cgroup OOM via dmesg/journalctl (design 03 §C); {r['evidence']}"),
        ("Notes", it.notes, lambda r: f"- {r}"),
    ):
        if rows:
            md.append("")
            md.append(f"## {title}")
            md.extend(render(r) for r in rows)
    with open(os.path.join(run_dir, "summary.md"), "w") as f:
        f.write("\n".join(md) + "\n")

    # ---- action-items.md (stable path; design 07 §A)
    items = gating[:]
    ai = [
        "# NIGHTLY ACTION ITEMS — generated by ci/batch, DO NOT EDIT",
        f"run: {run_id}   sha: {summary['sha'][:12]}   status: {status}",
        f"generated: {now}",
        f"items: {len(items) + (1 if investigate else 0)}",
        "",
    ]
    seq = 0
    for r in items:
        seq += 1
        first_seen = "(new tonight)" if r["subject"] not in prev_subjects else "(also failed in the previous run)"
        ai += [
            f"## AI-{run_id}-{seq:03d}  [{r['kind']}] {r['subject']}",
            f"- subject: {r['subject']}",
            f"- what: {r['what']}",
            f"- repro: {r['repro']}",
            f"- evidence: {r['evidence']}",
            f"- first-seen: {run_id[:8]} {first_seen}",
            "",
        ]
    if investigate:
        seq += 1
        ai += [
            f"## AI-{run_id}-{seq:03d}  [investigate] batch/resource-kill-streak",
            "- subject: batch/resource-kill-streak",
            "- what: 2 consecutive inconclusive runs (resource kills) — lower batch caps or move NIGHTLY_FIRE_HOUR (ci/design/03 §C, 06 §E)",
            "- repro: check dmesg / journalctl --user for cgroup OOM events in the run windows",
            f"- evidence: ci/logs/{run_id}/summary.json + ci/logs/history.jsonl",
            "",
        ]
    notices = []
    for r in it.promotable:
        notices.append(f"- promotable: {r['case_id']} PASSED ({r['test']}) — consider promotion per ci/design/02-test-selection.md §D")
    for n in it.notes:
        notices.append(f"- {n}")
    if notices:
        ai += ["# NON-BLOCKING NOTICES (no action required; harvest opportunistically)"] + notices + [""]
    with open(os.path.join(logs_dir, "action-items.md"), "w") as f:
        f.write("\n".join(ai) + "\n")

    # ---- history.jsonl
    hist = {
        "run_id": run_id,
        "sha": summary["sha"],
        "status": status,
        "regressions": len(it.regressions),
        "perf_drastic": len(it.perf_drastic),
        "stages": {k: v["status"] for k, v in stages.items()},
        "durations": {k: v["elapsed_s"] for k, v in stages.items()},
    }
    with open(os.path.join(logs_dir, "history.jsonl"), "a") as f:
        f.write(json.dumps(hist) + "\n")

    print(f"STATUS={status} regressions={len(it.regressions)} perf_drastic={len(it.perf_drastic)} "
          f"resource_kills={len(it.resource_kills)} promotable={len(it.promotable)}")
    sys.exit(0 if status == "pass" else (3 if status == "inconclusive" else 2))


if __name__ == "__main__":
    main()
