"""Unit tests for the statistics engine and aggregation."""

from __future__ import annotations

import unittest

from tools.gocomplexity.config import Config
from tools.gocomplexity.models import FunctionMetric
from tools.gocomplexity.stats import build_summary


def _fm(cc: int, cog: int | None, pkg: str, fn: str, file: str, line: int) -> FunctionMetric:
    import os

    return FunctionMetric(
        cyclomatic=cc,
        cognitive=cog,
        package=pkg,
        function=fn,
        file=file,
        line=line,
        directory=os.path.dirname(file) or ".",
    )


class StatsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.metrics = [
            _fm(1, 0, "a", "F1", "internal/a/x.go", 1),
            _fm(5, 3, "a", "F2", "internal/a/x.go", 20),
            _fm(10, 8, "a", "F3", "internal/a/y.go", 5),
            _fm(20, 25, "b", "F4", "internal/b/z.go", 3),
        ]
        self.config = Config(thresholds=[5, 15]).normalized()

    def test_counts(self) -> None:
        s = build_summary(
            self.metrics, self.config, "now", num_files=3, sources={},
            duplication=(0.0, 0, 0),
        )
        self.assertEqual(s.num_functions, 4)
        self.assertEqual(s.num_packages, 2)
        self.assertEqual(s.num_files, 3)

    def test_cyclomatic_stats(self) -> None:
        s = build_summary(
            self.metrics, self.config, "now", num_files=3, sources={},
            duplication=(0.0, 0, 0),
        )
        self.assertEqual(s.cyclomatic.maximum, 20)
        self.assertEqual(s.cyclomatic.mean, 9.0)  # (1+5+10+20)/4
        self.assertEqual(s.cyclomatic.median, 7.5)  # median of 1,5,10,20
        # strictly greater than threshold
        self.assertEqual(s.cyclomatic.above_thresholds[5], 2)   # 10, 20
        self.assertEqual(s.cyclomatic.above_thresholds[15], 1)  # 20

    def test_cognitive_ignores_none(self) -> None:
        metrics = self.metrics + [_fm(3, None, "b", "F5", "internal/b/z.go", 40)]
        s = build_summary(
            metrics, self.config, "now", num_files=3, sources={},
            duplication=(0.0, 0, 0),
        )
        # cognitive count excludes the None entry
        self.assertEqual(s.cognitive.count, 4)
        self.assertEqual(s.cognitive.maximum, 25)

    def test_package_aggregate_ranked_by_total_cc(self) -> None:
        s = build_summary(
            self.metrics, self.config, "now", num_files=3, sources={},
            duplication=(0.0, 0, 0),
        )
        # package 'a' total cc = 16, package 'b' = 20 -> b ranks first
        self.assertEqual(s.packages[0].key, "b")
        self.assertEqual(s.packages[0].total_cyclomatic, 20)
        self.assertEqual(s.packages[1].key, "a")
        self.assertEqual(s.packages[1].total_cyclomatic, 16)

    def test_empty_metrics(self) -> None:
        s = build_summary(
            [], self.config, "now", num_files=0, sources={}, duplication=(0.0, 0, 0)
        )
        self.assertEqual(s.num_functions, 0)
        self.assertEqual(s.cyclomatic.maximum, 0)
        self.assertEqual(s.cyclomatic.above_thresholds[5], 0)


if __name__ == "__main__":
    unittest.main()
