"""Data models for the mdtablefix tool."""

from __future__ import annotations

import dataclasses
from typing import Literal


@dataclasses.dataclass
class Fix:
    """A single repair action applied to a table cell or to its structure.

    Attributes:
        type: Kind of repair.  Cell-level: escaped_pipe, missing_cell,
            extra_cell.  Structural (whole-row / whole-table): blank_line
            (a body-splitting blank line was removed), missing_outer_pipe
            (a row's leading or trailing ``|`` was restored).
        line: 1-based line number in the original document.
        column: 1-based column index where the repair was applied.
            Structural fixes use column 1.
        detail: Human-readable explanation of the repair.
    """

    type: Literal[
        "escaped_pipe",
        "missing_cell",
        "extra_cell",
        "blank_line",
        "missing_outer_pipe",
    ]
    line: int
    column: int
    detail: str


@dataclasses.dataclass
class Cell:
    """A single table cell.

    Attributes:
        content: Raw cell text after splitting (whitespace stripped).
        is_code: True if the cell contains at least one backtick code span.
    """

    content: str
    is_code: bool = False


@dataclasses.dataclass
class Row:
    """A table row.

    Attributes:
        cells: List of cells in this row.
        line_number: 1-based line number in the original document.
    """

    cells: list[Cell]
    line_number: int


@dataclasses.dataclass
class Table:
    """A detected markdown table.

    Attributes:
        header: The header row.
        separator: The separator row (e.g. |---|:---:|---:|).
        data_rows: All data rows following the separator.
        alignment: Per-column alignment strings ("left", "right", "center").
        fixes: Repairs applied to this table.
        start_line: 1-based line number of the header row.
    """

    header: Row
    separator: Row
    data_rows: list[Row]
    alignment: list[str]
    fixes: list[Fix]
    start_line: int


@dataclasses.dataclass
class CodeBlock:
    """A fenced code block (``` ... ```).

    Attributes:
        lines: All lines including the opening and closing fences.
        start_line: 1-based line number of the opening fence.
    """

    lines: list[str]
    start_line: int


@dataclasses.dataclass
class TextBlock:
    """A non-table, non-code block of markdown text.

    Attributes:
        lines: Lines of text.
        start_line: 1-based line number of the first line.
    """

    lines: list[str]
    start_line: int


# A document block is one of these three types.
Block = CodeBlock | TextBlock | Table


@dataclasses.dataclass
class Document:
    """A parsed markdown document.

    Attributes:
        blocks: Ordered list of blocks (text, code, tables).
    """

    blocks: list[Block]
