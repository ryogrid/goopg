#!/usr/bin/env python3
"""Regression test for summarize.py's per-package units/race classification.

Guards against the 2026-07-15 bug: a single genuine `--- FAIL` anywhere in
the combined ~40-package `units` log (e.g. one real assertion failure) used
to mask the resource-kill signature (`signal: killed`, no `--- FAIL` of its
own) on every OTHER package killed by the shared nightly cgroup memory cap,
misreporting pure resource kills as regressions (AI-20260715-010036-001/002/004
against `cmd/goopg`/`internal/access/amcheck`/`internal/mvcc`).

Run: python3 ci/batch/lib/test_summarize.py
"""

import importlib.util
import json
import os
import tempfile
import unittest

_HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("summarize", os.path.join(_HERE, "summarize.py"))
summarize = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(summarize)


SYNTHETIC_UNITS_LOG = """\
ok  \tgithub.com/goopg/goopg/internal/access/btree\t6.262s
--- FAIL: TestStripeAppendConcurrentDrainConsistency (0.00s)
    stripe_append_test.go:42: drain goroutine never ran
FAIL
FAIL\tgithub.com/goopg/goopg/internal/wal\t5.336s
ok  \tgithub.com/goopg/goopg/internal/executor\t8.108s
SIGQUIT: quit
PC=0x48cec1 m=0 sigcode=0

*** Test killed with quit: ran too long (33m0s).
signal: killed
FAIL\tgithub.com/goopg/goopg/internal/access/amcheck\t2160.522s
*** Test killed with quit: ran too long (33m0s).
signal: killed
FAIL\tgithub.com/goopg/goopg/cmd/goopg\t2160.144s
ok  \tgithub.com/goopg/goopg/internal/mctx\t0.005s
"""


class SplitGoTestPkgBlocksTest(unittest.TestCase):
    def test_boundaries_and_status(self):
        blocks = summarize.split_go_test_pkg_blocks(SYNTHETIC_UNITS_LOG)
        got = [(pkg, status) for pkg, status, _text in blocks]
        self.assertEqual(
            got,
            [
                ("github.com/goopg/goopg/internal/access/btree", "ok"),
                ("github.com/goopg/goopg/internal/wal", "FAIL"),
                ("github.com/goopg/goopg/internal/executor", "ok"),
                ("github.com/goopg/goopg/internal/access/amcheck", "FAIL"),
                ("github.com/goopg/goopg/cmd/goopg", "FAIL"),
                ("github.com/goopg/goopg/internal/mctx", "ok"),
            ],
        )

    def test_real_fail_block_does_not_leak_into_sibling_blocks(self):
        blocks = summarize.split_go_test_pkg_blocks(SYNTHETIC_UNITS_LOG)
        by_pkg = {pkg: text for pkg, _status, text in blocks}
        self.assertIn("--- FAIL", by_pkg["github.com/goopg/goopg/internal/wal"])
        self.assertNotIn("--- FAIL", by_pkg["github.com/goopg/goopg/internal/access/amcheck"])
        self.assertNotIn("--- FAIL", by_pkg["github.com/goopg/goopg/cmd/goopg"])


class AnalyzeUnitsClassificationTest(unittest.TestCase):
    def _run_analyze(self, log_text):
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = os.path.join(tmp, "20260101-000000")
            os.makedirs(os.path.join(run_dir, "units"))
            os.makedirs(os.path.join(run_dir, "stages"))
            with open(os.path.join(run_dir, "units", "go-test.log"), "w") as f:
                f.write(log_text)
            with open(os.path.join(run_dir, "stages", "units.status"), "w") as f:
                f.write("fail 2160\n")
            # repo_root doesn't need to be real: every read_csv/read_file call
            # in analyze() tolerates a missing path (returns [] / "").
            it, _stages, _timings, _tpcds_timings, _spot, _extra = summarize.analyze(run_dir, tmp, "20260101-000000")
            return it

    def test_resource_kills_and_regression_classified_independently(self):
        it = self._run_analyze(SYNTHETIC_UNITS_LOG)

        rk_pkgs = {r["pkg"] for r in it.resource_kills}
        self.assertEqual(rk_pkgs, {"internal/access/amcheck", "cmd/goopg"})

        reg_subjects = {r["subject"] for r in it.regressions}
        self.assertIn("units/internal/wal", reg_subjects)
        # The two pure resource-kills must NOT also show up as regressions —
        # this is exactly the bug: pre-fix, every FAIL package in a log
        # containing any "--- FAIL" was reported as a regression.
        self.assertNotIn("units/internal/access/amcheck", reg_subjects)
        self.assertNotIn("units/cmd/goopg", reg_subjects)

    def test_build_failed_packages_collapse_into_one_infra_item(self):
        """Run 20260813-005117: one broken file, 8 phantom "regressions".

        `undefined: pgDateTimeKeywords` in internal/executor failed every
        package that imports it. The units stage had PASSED 53 s earlier on the
        same sha (dirty=62), so nothing was wrong with the code — the tree was
        edited mid-run. Packages that never compiled must be ONE infra item,
        while a package that genuinely ran and failed still reports.
        """
        log = (
            "?   \tgithub.com/goopg/goopg/cmd/diag\t[no test files]\n"
            "# github.com/goopg/goopg/internal/executor\n"
            "internal/executor/time_zone_token.go:116:6: undefined: pgDateTimeKeywords\n"
            "FAIL\tgithub.com/goopg/goopg/cmd/gen-oracle-report [build failed]\n"
            "FAIL\tgithub.com/goopg/goopg/cmd/goopg [build failed]\n"
            "FAIL\tgithub.com/goopg/goopg/internal/executor [build failed]\n"
            "FAIL\tgithub.com/goopg/goopg/internal/initdb [build failed]\n"
            "ok  \tgithub.com/goopg/goopg/internal/planner\t16.887s\n"
            "--- FAIL: TestCheckpointerVolumeTrigger (2.02s)\n"
            "    checkpointer_test.go:281: volume trigger did not fire within 2s\n"
            "FAIL\tgithub.com/goopg/goopg/internal/wal\t13.478s\n"
        )
        it = self._run_analyze(log)

        reg_subjects = {r["subject"] for r in it.regressions}
        # The package that actually ran and failed is still a real regression.
        self.assertIn("units/internal/wal", reg_subjects)
        # The four that never compiled are not.
        for rel in ("cmd/gen-oracle-report", "cmd/goopg", "internal/executor", "internal/initdb"):
            self.assertNotIn(f"units/{rel}", reg_subjects)

        self.assertEqual(len(it.build_kills), 1)
        bk = it.build_kills[0]
        self.assertEqual(bk["kind"], "infra")
        self.assertEqual(bk["subject"], "units/build-broke-mid-stage")
        self.assertIn("4 package(s)", bk["what"])
        # The compiler's own text must ride along so triage needn't open the log.
        self.assertIn("undefined: pgDateTimeKeywords", bk["what"])

    def test_pure_resource_kill_log_with_no_real_fail(self):
        log = SYNTHETIC_UNITS_LOG.replace(
            "--- FAIL: TestStripeAppendConcurrentDrainConsistency (0.00s)\n"
            "    stripe_append_test.go:42: drain goroutine never ran\nFAIL\n"
            "FAIL\tgithub.com/goopg/goopg/internal/wal\t5.336s\n",
            "ok  \tgithub.com/goopg/goopg/internal/wal\t1.1s\n",
        )
        it = self._run_analyze(log)
        self.assertEqual(len(it.regressions), 0)
        rk_pkgs = {r["pkg"] for r in it.resource_kills}
        self.assertEqual(rk_pkgs, {"internal/access/amcheck", "cmd/goopg"})


SYNTHETIC_TESTPORT_LOG = """\
=== RUN   TestPort_IsolationEvalPlanQual
    isolation_port_test.go:88: output mismatch at line 1027
--- FAIL: TestPort_IsolationEvalPlanQual (21.56s)
=== RUN   TestPort_PgBasebackup010StreamWAL
--- PASS: TestPort_PgBasebackup010StreamWAL (15.36s)
=== RUN   TestPort_PgBasebackup010FetchWAL
    pgbasebackup_port_test.go:316: init: init failed: # github.com/goopg/goopg/internal/planner
        internal/planner/joinsearchlevel.go:109:6: s.traceFailed undefined (type *searchCtx has no field or method traceFailed)
--- FAIL: TestPort_PgBasebackup010FetchWAL (0.18s)
=== RUN   TestPort_PgDumpConnectionSetup
    pgdump_connsetup_test.go:397: start failed; process exited early (see /tmp/x/data/cluster.log)
--- FAIL: TestPort_PgDumpConnectionSetup (0.45s)
FAIL
FAIL\tgithub.com/goopg/goopg/internal/testport\t1079.0s
"""


class MidRunBuildBreakTest(unittest.TestCase):
    """Guards AI-20260806-011323-002..-015: ONE mid-run compile error became 14
    separate "regression" action items, because the nightly builds the LIVE
    working tree and a Ralph loop committed between preflight's `make build`
    (which passed) and the testport stage.
    """

    def _run_analyze(self, log_text, meta=None, stage_fps=None):
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = os.path.join(tmp, "20260101-000000")
            os.makedirs(os.path.join(run_dir, "testport"))
            os.makedirs(os.path.join(run_dir, "stages"))
            with open(os.path.join(run_dir, "testport", "go-test.log"), "w") as f:
                f.write(log_text)
            with open(os.path.join(run_dir, "stages", "testport.status"), "w") as f:
                f.write("fail 1079\n")
            if meta is not None:
                with open(os.path.join(run_dir, "meta.json"), "w") as f:
                    json.dump(meta, f)
            for stage, fp in (stage_fps or {}).items():
                with open(os.path.join(run_dir, "stages", f"{stage}.fp"), "w") as f:
                    f.write(fp + "\n")
            it, *_ = summarize.analyze(run_dir, tmp, "20260101-000000")
            return it

    def test_boundary_is_the_first_compile_error(self):
        idx = summarize.build_error_line(SYNTHETIC_TESTPORT_LOG)
        lines = SYNTHETIC_TESTPORT_LOG.splitlines()
        self.assertIsNotNone(idx)
        self.assertIn("init: init failed:", lines[idx])
        # A plain test-log line (`file.go:LINE:`, one number) must not look
        # like a compile error (`file.go:LINE:COL:`) — otherwise every stage
        # with normal t.Errorf output would collapse to "build broke".
        self.assertIsNone(summarize.build_error_line(
            "    isolation_port_test.go:88: output mismatch at line 1027\n"))

    def test_post_boundary_fails_collapse_and_pre_boundary_survives(self):
        it = self._run_analyze(SYNTHETIC_TESTPORT_LOG)
        subjects = {r["subject"] for r in it.regressions}
        # The genuine six-night regression failed BEFORE the break — it is a
        # real result and must still be reported.
        self.assertIn("testport/TestPort_IsolationEvalPlanQual", subjects)
        # Both post-break victims collapse, including the pg_dump-shaped one
        # whose body carries no compiler text at all ("process exited early").
        self.assertNotIn("testport/TestPort_PgBasebackup010FetchWAL", subjects)
        self.assertNotIn("testport/TestPort_PgDumpConnectionSetup", subjects)
        self.assertEqual(len(it.build_kills), 1)
        what = it.build_kills[0]["what"]
        self.assertIn("2 test(s)", what)
        self.assertIn("TestPort_PgDumpConnectionSetup", what)

    def test_fingerprint_drift_names_the_cause(self):
        it = self._run_analyze(
            SYNTHETIC_TESTPORT_LOG,
            meta={"source_fp": "aaaaaaaaaaaaaaaa"},
            stage_fps={"testport": "bbbbbbbbbbbbbbbb"},
        )
        self.assertIn("MUTATED mid-run", it.build_kills[0]["what"])

    def test_no_build_error_leaves_every_fail_reported(self):
        clean = SYNTHETIC_TESTPORT_LOG.replace(
            "    pgbasebackup_port_test.go:316: init: init failed: # github.com/goopg/goopg/internal/planner\n"
            "        internal/planner/joinsearchlevel.go:109:6: s.traceFailed undefined (type *searchCtx has no field or method traceFailed)\n",
            "    pgbasebackup_port_test.go:316: WAL segment never arrived\n",
        )
        it = self._run_analyze(clean)
        self.assertEqual(it.build_kills, [])
        self.assertEqual(
            {r["subject"] for r in it.regressions},
            {
                "testport/TestPort_IsolationEvalPlanQual",
                "testport/TestPort_PgBasebackup010FetchWAL",
                "testport/TestPort_PgDumpConnectionSetup",
            },
        )


class RegressBaselineTest(unittest.TestCase):
    def _write_inventory(self, tmp, rows):
        import csv
        d = os.path.join(tmp, "docs", "test-port")
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "postgres-oracle-target-inventory.csv"), "w", newline="") as f:
            w = csv.writer(f, lineterminator="\n")
            w.writerow(["id", "suite_id", "kind", "item_path", "status", "pass_required", "deferred_to", "rationale"])
            for r in rows:
                w.writerow(r)

    def test_basename_stripped_and_regress_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            self._write_inventory(tmp, [
                ["", "regress-sql", "regress", "postgres/src/test/regress/sql/boolean.sql", "pass", "yes", "-", "Confirmed pass"],
                ["", "regress-sql", "regress", "postgres/src/test/regress/sql/mvcc.sql", "failed", "no", "-", "diverges"],
                ["P-001", "client-tools-tap", "tap", "postgres/src/bin/pg_ctl/t/001_start_stop.pl", "port", "yes", "-", "TestPort_PgCtl001StartStop"],
            ])
            baseline = summarize.regress_baseline(tmp)
            self.assertEqual(baseline["boolean"], "pass")
            self.assertEqual(baseline["mvcc"], "failed")
            self.assertNotIn("001_start_stop", baseline)  # tap row excluded
            self.assertNotIn("boolean.sql", baseline)      # extension stripped

    def test_missing_csv_is_empty(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(summarize.regress_baseline(tmp), {})


if __name__ == "__main__":
    unittest.main()
