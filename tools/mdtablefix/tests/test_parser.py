"""Tests for the document block parser."""

from __future__ import annotations

import unittest

from tools.mdtablefix.models import CodeBlock, Table, TextBlock
from tools.mdtablefix.parser import parse_document


class TestParseDocument(unittest.TestCase):
    """Tests for parse_document()."""

    def test_simple_table(self):
        text = "| a | b |\n| --- | --- |\n| x | y |\n"
        doc = parse_document(text)
        self.assertEqual(len(doc.blocks), 1)
        self.assertIsInstance(doc.blocks[0], Table)

    def test_table_with_preamble(self):
        text = "Some text.\n\n| a | b |\n| --- | --- |\n| x | y |\n"
        doc = parse_document(text)
        self.assertGreaterEqual(len(doc.blocks), 2)
        self.assertIsInstance(doc.blocks[0], TextBlock)
        # There should be a Table somewhere.
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)

    def test_code_fence_ignores_table_like_lines(self):
        text = (
            "```go\n"
            "| not | a | table |\n"
            "```\n"
            "\n"
            "| real | table |\n"
            "|------|-------|\n"
            "| yes  | here  |\n"
        )
        doc = parse_document(text)
        # Should have a CodeBlock and a Table.
        code_blocks = [b for b in doc.blocks if isinstance(b, CodeBlock)]
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(code_blocks), 1)
        self.assertEqual(len(tables), 1)

    def test_unclosed_fence(self):
        text = "```go\n| not | a | table |\n"
        doc = parse_document(text)
        code_blocks = [b for b in doc.blocks if isinstance(b, CodeBlock)]
        self.assertEqual(len(code_blocks), 1)

    def test_multiple_tables(self):
        """Two tables separated by non-blank text are detected separately."""
        text = (
            "| a | b |\n| --- | --- |\n| 1 | 2 |\n"
            "\n"
            "Some text between tables.\n"
            "\n"
            "| c | d |\n| --- | --- |\n| 3 | 4 |\n"
        )
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 2)

    def test_non_table_pipe_lines(self):
        """Lines starting with | but not a valid table become TextBlock."""
        text = "| just | one | row |\n"
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 0)
        # Should be a TextBlock.
        text_blocks = [b for b in doc.blocks if isinstance(b, TextBlock)]
        self.assertEqual(len(text_blocks), 1)


class TestBlankLineHandling(unittest.TestCase):
    """A blank line inside a table body must be absorbed, not honoured.

    In GFM a blank line terminates the table, so every row after it renders
    as literal ``|``-text.  The parser rejoins the fragments — but only when
    the fragment is a continuation, never when a second table starts.
    """

    def test_body_split_by_blank_line_is_rejoined(self):
        text = (
            "| a | b |\n| --- | --- |\n| 1 | 2 |\n"
            "\n"
            "| 3 | 4 |\n"
        )
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)
        self.assertEqual(len(tables[0].data_rows), 2)

    def test_multiple_blank_lines_are_absorbed(self):
        text = (
            "| a | b |\n| --- | --- |\n| 1 | 2 |\n"
            "\n\n\n"
            "| 3 | 4 |\n"
        )
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)
        self.assertEqual(len(tables[0].data_rows), 2)

    def test_blank_separated_tables_are_not_merged(self):
        """A header+separator pair after the blank starts a NEW table."""
        text = (
            "| a | b |\n| --- | --- |\n| 1 | 2 |\n"
            "\n"
            "| c | d |\n| --- | --- |\n| 3 | 4 |\n"
        )
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 2)
        self.assertEqual(tables[0].header.cells[0].content, "a")
        self.assertEqual(tables[1].header.cells[0].content, "c")

    def test_trailing_paragraph_stays_outside_the_table(self):
        text = (
            "| a | b |\n| --- | --- |\n| 1 | 2 |\n"
            "\n"
            "Some prose.\n"
        )
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)
        self.assertEqual(len(tables[0].data_rows), 1)
        text_blocks = [b for b in doc.blocks if isinstance(b, TextBlock)]
        self.assertTrue(
            any("Some prose." in "\n".join(b.lines) for b in text_blocks)
        )


if __name__ == "__main__":
    unittest.main()
