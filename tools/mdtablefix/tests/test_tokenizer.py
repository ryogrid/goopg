"""Tests for the backtick-aware cell tokenizer."""

from __future__ import annotations

import unittest

from tools.mdtablefix.tokenizer import (
    _find_backtick_spans,
    escape_embedded_pipes,
    tokenize_row,
)


class TestFindBacktickSpans(unittest.TestCase):
    """Tests for _find_backtick_spans()."""

    def test_no_backticks(self):
        spans = _find_backtick_spans("hello world")
        self.assertEqual(spans, [])

    def test_single_backtick_span(self):
        spans = _find_backtick_spans("hello `code` world")
        self.assertEqual(spans, [(6, 12)])

    def test_double_backtick_span(self):
        spans = _find_backtick_spans("hello ``code`` world")
        self.assertEqual(spans, [(6, 14)])

    def test_multiple_spans(self):
        spans = _find_backtick_spans("`a` and `b`")
        self.assertEqual(len(spans), 2)
        self.assertEqual(spans[0], (0, 3))
        # `b` starts at index 8: "`a` and " = 8 chars
        self.assertEqual(spans[1], (8, 11))

    def test_unmatched_backtick_treated_as_text(self):
        spans = _find_backtick_spans("hello `world")
        self.assertEqual(spans, [])

    def test_nested_single_in_double(self):
        # `` ` `` is a double-backtick span containing a single backtick.
        spans = _find_backtick_spans("hello `` ` `` world")
        self.assertEqual(spans, [(6, 13)])


class TestTokenizeRow(unittest.TestCase):
    """Tests for tokenize_row()."""

    def test_simple_row(self):
        cells = tokenize_row("| a | b | c |")
        self.assertEqual(len(cells), 3)
        self.assertEqual([c.content for c in cells], ["a", "b", "c"])

    def test_backtick_protected_pipes(self):
        cells = tokenize_row("| a | `code|with|pipes` | b |")
        self.assertEqual(len(cells), 3)
        self.assertEqual(cells[1].content, "`code|with|pipes`")
        self.assertTrue(cells[1].is_code)

    def test_double_backtick_protected_pipes(self):
        cells = tokenize_row("| a | ``code|with|pipes`` | b |")
        self.assertEqual(len(cells), 3)
        self.assertEqual(cells[1].content, "``code|with|pipes``")
        self.assertTrue(cells[1].is_code)

    def test_escaped_pipe_not_a_separator(self):
        cells = tokenize_row(r"| a \| b | c |")
        self.assertEqual(len(cells), 2)
        self.assertEqual(cells[0].content, r"a \| b")
        self.assertEqual(cells[1].content, "c")

    def test_empty_cell(self):
        cells = tokenize_row("| a || c |")
        self.assertEqual(len(cells), 3)
        self.assertEqual(cells[1].content, "")

    def test_multiple_empty_cells(self):
        cells = tokenize_row("| a ||| d |")
        self.assertEqual(len(cells), 4)
        self.assertEqual(cells[1].content, "")
        self.assertEqual(cells[2].content, "")

    def test_whitespace_around_cells(self):
        cells = tokenize_row("|  a  |  b  |  c  |")
        self.assertEqual([c.content for c in cells], ["a", "b", "c"])

    def test_seven_column_deferral_row(self):
        """A typical 7-column row from deferral_ledger.md."""
        line = (
            "| resolved | 2026-06-13 | M0100-0006b "
            "| part (c): `(step notices N)` blocker parsing "
            "| parts (a)/(b): spectoken/transactionid rows "
            "| `internal/executor/spec_insert_registry.go` "
            "| spectoken pg_locks reporting integration |"
        )
        cells = tokenize_row(line)
        self.assertEqual(len(cells), 7)

    def test_code_span_detection(self):
        cells = tokenize_row("| a | `code` | b |")
        self.assertFalse(cells[0].is_code)
        self.assertTrue(cells[1].is_code)
        self.assertFalse(cells[2].is_code)


class TestEscapeEmbeddedPipes(unittest.TestCase):
    """Tests for escape_embedded_pipes()."""

    def test_no_pipes(self):
        result = escape_embedded_pipes("hello world")
        self.assertEqual(result, "hello world")

    def test_bare_pipes_escaped(self):
        result = escape_embedded_pipes("a | b | c")
        self.assertEqual(result, r"a \| b \| c")

    def test_pipes_inside_backticks_preserved(self):
        result = escape_embedded_pipes("a `x|y` b | c")
        self.assertEqual(result, r"a `x|y` b \| c")

    def test_already_escaped_pipes_preserved(self):
        result = escape_embedded_pipes(r"a \| b")
        self.assertEqual(result, r"a \| b")

    def test_mixed_escaped_and_bare(self):
        result = escape_embedded_pipes(r"a \| b | c `d|e` | f")
        self.assertEqual(result, r"a \| b \| c `d|e` \| f")


if __name__ == "__main__":
    unittest.main()
