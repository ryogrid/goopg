"""Tests for the two defect classes found in a deferral-ledger summary.

Both are cases where the table's *pipe* structure is intact and only the
rendered output is wrong, which is why the pipe-centric passes missed them:

* a raw ``<tag>`` in a cell — dropped by GitHub's sanitizer when the name is
  unknown (``<db>``), or, when it is a real element (``<table>``), opened
  inside the cell so that every following row nests inside it;
* a column separator deleted from the middle of a row, fusing two columns
  into one cell.  Padding the row at the END — what the tool used to do —
  changes nothing on GitHub, so the row is re-split instead, at the word
  boundary the neighbouring rows' column widths point at.
"""

from __future__ import annotations

import unittest

from tools.mdtablefix.models import Table
from tools.mdtablefix.parser import parse_document
from tools.mdtablefix.repair import repair_table
from tools.mdtablefix.tokenizer import escape_html_tags

HEADER = "| c1 | c2 | c3 |"
SEP = "| --- | --- | --- |"


def build_table(*data_lines: str) -> Table:
    """Parse a 3-column table made of *data_lines*."""
    text = "\n".join([HEADER, SEP, *data_lines]) + "\n"
    doc = parse_document(text)
    return [b for b in doc.blocks if isinstance(b, Table)][0]


class TestEscapeHtmlTags(unittest.TestCase):
    """Unit tests for escape_html_tags()."""

    def test_structural_tag_escaped(self):
        self.assertEqual(
            escape_html_tags("<table>_<col>_check"),
            "&lt;table>_&lt;col>_check",
        )

    def test_unknown_placeholder_escaped(self):
        self.assertEqual(
            escape_html_tags("base/<db>/2611"), "base/&lt;db>/2611"
        )

    def test_autolink_preserved(self):
        text = "see <https://example.com/a?b=1> for detail"
        self.assertEqual(escape_html_tags(text), text)

    def test_allowlisted_inline_tag_preserved(self):
        text = "first line<br>second line"
        self.assertEqual(escape_html_tags(text), text)

    def test_code_span_preserved(self):
        text = "the `<table>` element"
        self.assertEqual(escape_html_tags(text), text)

    def test_bare_less_than_is_not_a_tag(self):
        text = "a < b and 3<4 and x <- y"
        self.assertEqual(escape_html_tags(text), text)

    def test_idempotent(self):
        once = escape_html_tags("<table>_<col>_check")
        self.assertEqual(escape_html_tags(once), once)


class TestHtmlTagRepair(unittest.TestCase):
    """The repair pass escapes raw HTML and says which failure mode it was."""

    def test_structural_tag_reported_and_escaped(self):
        table = repair_table(
            build_table("| a | CONSTRAINT <table>_<col>_check | c |")
        )
        self.assertEqual(
            table.data_rows[0].cells[1].content,
            "CONSTRAINT &lt;table>_&lt;col>_check",
        )
        fix = next(f for f in table.fixes if f.type == "html_tag")
        self.assertTrue(fix.repaired)
        self.assertIn("nests every following row", fix.detail)

    def test_unknown_tag_reported_as_deleted_text(self):
        table = repair_table(build_table("| a | base/<db>/2611 | c |"))
        fix = next(f for f in table.fixes if f.type == "html_tag")
        self.assertIn("sanitizer deletes unknown tags", fix.detail)

    def test_header_is_swept_too(self):
        text = "\n".join(["| c1 | <table>x | c3 |", SEP, "| a | b | c |"])
        doc = parse_document(text + "\n")
        table = repair_table(
            [b for b in doc.blocks if isinstance(b, Table)][0]
        )
        self.assertEqual(table.header.cells[1].content, "&lt;table>x")

    def test_clean_table_reports_nothing(self):
        table = repair_table(build_table("| a | b | c |"))
        self.assertEqual([f for f in table.fixes if f.type == "html_tag"], [])


class TestLostSeparator(unittest.TestCase):
    """A separator deleted mid-row is re-inserted, never silently padded."""

    # Column 3 is populated and ~30 chars wide on every well-formed row, so
    # a short row whose column-2 cell has clearly absorbed one of those is a
    # fusion, not a row that simply ended early.
    WELL_FORMED = [
        "| - | resume point text here | why the work was deferred now |",
        "| - | another resume point x | why the work was deferred now |",
        "| - | third resume point yyy | why the work was deferred now |",
    ]

    def test_fused_row_is_re_split_at_the_right_boundary(self):
        fused = (
            "| - | resume point text here why the work was deferred now |"
        )
        table = repair_table(build_table(*self.WELL_FORMED, fused))
        row = table.data_rows[-1]
        self.assertEqual(len(row.cells), 3)
        self.assertEqual(row.cells[1].content, "resume point text here")
        self.assertEqual(row.cells[2].content, "why the work was deferred now")
        fix = next(f for f in table.fixes if f.type == "lost_separator")
        self.assertTrue(fix.repaired)
        self.assertEqual(fix.column, 2)
        self.assertEqual([f for f in table.fixes if f.type == "missing_cell"], [])

    def test_re_split_is_lossless_and_idempotent(self):
        """The repair only turns one space into ` | `, and settles there."""
        fused = (
            "| - | resume point text here why the work was deferred now |"
        )
        table = repair_table(build_table(*self.WELL_FORMED, fused))
        cells = [c.content for c in table.data_rows[-1].cells]
        self.assertEqual(
            " ".join(cells[1:]),
            "resume point text here why the work was deferred now",
        )
        rebuilt = "| " + " | ".join(cells) + " |"
        again = repair_table(build_table(*self.WELL_FORMED, rebuilt))
        self.assertEqual(
            [c.content for c in again.data_rows[-1].cells], cells
        )
        self.assertEqual(
            [f for f in again.fixes if f.type == "lost_separator"], []
        )

    def test_unsplittable_cell_is_reported_for_a_human(self):
        """No word boundary fits → report, do not guess and do not pad."""
        # One unbroken token: the only candidate boundaries are the spaces
        # around it, and both leave a half wildly off its column's width.
        fused = "| - | " + "x" * 60 + " y |"
        table = repair_table(build_table(*self.WELL_FORMED, fused))
        row = table.data_rows[-1]
        self.assertEqual(len(row.cells), 2)
        fix = next(f for f in table.fixes if f.type == "lost_separator")
        self.assertFalse(fix.repaired)
        self.assertIn("left to a", fix.detail)

    def test_genuinely_short_row_is_still_padded(self):
        short = "| - | tiny |"
        table = repair_table(build_table(*self.WELL_FORMED, short))
        row = table.data_rows[-1]
        self.assertEqual(len(row.cells), 3)
        self.assertEqual(row.cells[2].content, "")
        self.assertTrue(any(f.type == "missing_cell" for f in table.fixes))
        self.assertEqual(
            [f for f in table.fixes if f.type == "lost_separator"], []
        )

    def test_mostly_empty_last_column_never_trips_the_check(self):
        """If the last column is usually empty, padding is the right call."""
        rows = [
            "| - | resume point text here |  |",
            "| - | another resume point x |  |",
            "| - | third resume point yyy |  |",
            "| - | resume point text here why the work was deferred |",
        ]
        table = repair_table(build_table(*rows))
        self.assertEqual(
            [f for f in table.fixes if f.type == "lost_separator"], []
        )
        self.assertEqual(len(table.data_rows[-1].cells), 3)


if __name__ == "__main__":
    unittest.main()
