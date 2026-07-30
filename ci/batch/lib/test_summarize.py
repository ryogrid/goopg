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


if __name__ == "__main__":
    unittest.main()
