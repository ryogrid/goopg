#!/usr/bin/env python3
"""Regression test for summarize.py's per-package units/race classification.

Guards against the 2026-07-15 bug: a single genuine `--- FAIL` anywhere in
the combined ~40-package `units` log (e.g. one real assertion failure) used
to mask the resource-kill signature (`signal: killed`, no `--- FAIL` of its
own) on every OTHER package killed by the shared nightly cgroup memory cap,
misreporting pure resource kills as regressions (AI-20260715-010036-001/002/004
against `cmd/goopg`/`internal/amcheck`/`internal/mvcc`).

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
FAIL\tgithub.com/goopg/goopg/internal/amcheck\t2160.522s
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
                ("github.com/goopg/goopg/internal/amcheck", "FAIL"),
                ("github.com/goopg/goopg/cmd/goopg", "FAIL"),
                ("github.com/goopg/goopg/internal/mctx", "ok"),
            ],
        )

    def test_real_fail_block_does_not_leak_into_sibling_blocks(self):
        blocks = summarize.split_go_test_pkg_blocks(SYNTHETIC_UNITS_LOG)
        by_pkg = {pkg: text for pkg, _status, text in blocks}
        self.assertIn("--- FAIL", by_pkg["github.com/goopg/goopg/internal/wal"])
        self.assertNotIn("--- FAIL", by_pkg["github.com/goopg/goopg/internal/amcheck"])
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
        self.assertEqual(rk_pkgs, {"internal/amcheck", "cmd/goopg"})

        reg_subjects = {r["subject"] for r in it.regressions}
        self.assertIn("units/internal/wal", reg_subjects)
        # The two pure resource-kills must NOT also show up as regressions —
        # this is exactly the bug: pre-fix, every FAIL package in a log
        # containing any "--- FAIL" was reported as a regression.
        self.assertNotIn("units/internal/amcheck", reg_subjects)
        self.assertNotIn("units/cmd/goopg", reg_subjects)

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
        self.assertEqual(rk_pkgs, {"internal/amcheck", "cmd/goopg"})


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


if __name__ == "__main__":
    unittest.main()
