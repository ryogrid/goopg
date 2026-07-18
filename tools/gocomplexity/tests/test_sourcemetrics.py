"""Unit tests for the Go source scanner: LOC, Halstead, MI, duplication."""

from __future__ import annotations

import unittest

from tools.gocomplexity.sourcemetrics import (
    FileSource,
    duplication_pct,
    maintainability_index,
    scan_file,
)

SAMPLE = '''\
package demo

// a line comment
import "fmt"

/* a block
   comment */
func Add(a, b int) int {
\treturn a + b // trailing comment
}
'''


class ScanFileTest(unittest.TestCase):
    def test_loc_excludes_comments_and_blanks(self) -> None:
        fs = scan_file(SAMPLE)
        # code lines: package, import, func, return, closing brace = 5
        self.assertEqual(fs.loc, 5)

    def test_string_literal_is_operand_not_split(self) -> None:
        fs = scan_file('package p\nvar s = "a|b|c"\n')
        # the string counts as a single operand, not three
        self.assertIn('"s"', "".join(fs.norm_lines.values()))

    def test_keywords_are_operators(self) -> None:
        fs = scan_file(SAMPLE)
        h = fs.halstead
        self.assertGreater(h.N1, 0)  # func/return/import/package are operators
        self.assertGreater(h.N2, 0)  # identifiers/literals are operands
        self.assertGreater(h.volume, 0.0)

    def test_block_comment_line_count(self) -> None:
        fs = scan_file(SAMPLE)
        self.assertEqual(fs.total_lines, SAMPLE.count("\n") + 1)


class MaintainabilityIndexTest(unittest.TestCase):
    def test_bounds(self) -> None:
        self.assertEqual(maintainability_index(0.0, 0.0, 0), 100.0)  # empty file
        mi = maintainability_index(1000.0, 5.0, 200)
        self.assertGreaterEqual(mi, 0.0)
        self.assertLessEqual(mi, 100.0)

    def test_more_complex_lowers_mi(self) -> None:
        simple = maintainability_index(500.0, 2.0, 100)
        complex_ = maintainability_index(5000.0, 40.0, 1000)
        self.assertGreater(simple, complex_)


class DuplicationTest(unittest.TestCase):
    def _src(self, rel: str, lines: dict[int, str]) -> FileSource:
        from tools.gocomplexity.sourcemetrics import Halstead

        return FileSource(file=rel, total_lines=len(lines), loc=len(lines),
                          halstead=Halstead(), norm_lines=lines)

    def test_detects_identical_block(self) -> None:
        block = {i: f"tok{i % 3}" for i in range(1, 7)}  # 6 code lines
        a = self._src("a.go", dict(block))
        b = self._src("b.go", dict(block))
        pct, dup, total = duplication_pct({"a.go": a, "b.go": b}, min_lines=6)
        self.assertEqual(total, 12)
        self.assertEqual(dup, 12)  # all lines duplicated across the two files
        self.assertEqual(pct, 100.0)

    def test_no_duplication(self) -> None:
        a = self._src("a.go", {i: f"unique_a_{i}" for i in range(1, 7)})
        b = self._src("b.go", {i: f"unique_b_{i}" for i in range(1, 7)})
        pct, dup, total = duplication_pct({"a.go": a, "b.go": b}, min_lines=6)
        self.assertEqual(dup, 0)
        self.assertEqual(pct, 0.0)


if __name__ == "__main__":
    unittest.main()
