"""Core repair engine for malformed markdown tables.

Repair strategies:
- **Oversplit** (too many columns): greedily merge excess cells
  right-to-left, escaping the pipe between merged cells as ``\\|``.
- **Undersplit** (too few columns): pad with trailing empty cells.
"""

from __future__ import annotations

from .models import Cell, Fix, Row, Table
from .tokenizer import escape_embedded_pipes, tokenize_row


def repair_table(table: Table) -> Table:
    """Repair a malformed table in-place (mutates and returns *table*).

    1. Determine the expected column count from the header row.
    2. For each data row whose column count differs from expected:
       - Too many → right-to-left greedy merge.
       - Too few  → trailing empty-cell padding.
    3. Record each repair in ``table.fixes``.
    """
    expected_cols = len(table.header.cells)
    table.fixes = []

    for row in table.data_rows:
        actual = len(row.cells)
        if actual == expected_cols:
            continue

        if actual > expected_cols:
            _repair_oversplit_row(row, expected_cols, table)
        else:
            _repair_undersplit_row(row, expected_cols, table)

    return table


def _repair_oversplit_row(row: Row, expected: int, table: Table) -> None:
    """Merge excess cells into the rightmost columns.

    The rightmost ``expected`` cells absorb all overflow from the
    extra cells.  The separating pipes are replaced with ``\\|``
    inside the merged cell content.

    Example with 9 cells, expected=7::

        [A, B, C, D, E, F, G, H, I]
        → merge H|I  →  [..., G, H\\|I]        (2 excess remaining)
        → merge G|H\\|I →  [..., F, G\\|H\\|I]  (1 excess remaining)
        → [A, B, C, D, E, F, G\\|H\\|I]         (done)
    """
    excess = len(row.cells) - expected

    # Keep the first (expected-1) cells untouched.
    # Merge the remaining (excess+1) cells into the last column.
    keep = expected - 1
    merge_group = row.cells[keep:]  # (excess + 1) cells

    # Join the merge-group cells, escaping embedded pipes.
    merged_parts: list[str] = []
    for cell in merge_group:
        merged_parts.append(escape_embedded_pipes(cell.content))
    merged_content = " \\| ".join(merged_parts)

    new_cells = row.cells[:keep]
    new_cells.append(Cell(content=merged_content, is_code=False))
    row.cells = new_cells

    # Record fixes.
    for idx in range(excess):
        col = keep + idx + 2  # 1-based column of the merged separator
        table.fixes.append(
            Fix(
                type="escaped_pipe",
                line=row.line_number,
                column=col,
                detail=(
                    f"Escaped pipe: merged extra column {col} "
                    f"into column {keep + 1}"
                ),
            )
        )


def _repair_undersplit_row(row: Row, expected: int, table: Table) -> None:
    """Pad a row with empty trailing cells to match *expected* columns."""
    missing = expected - len(row.cells)
    for idx in range(missing):
        col = len(row.cells) + 1
        row.cells.append(Cell(content="", is_code=False))
        table.fixes.append(
            Fix(
                type="missing_cell",
                line=row.line_number,
                column=col,
                detail=f"Added missing cell at column {col}",
            )
        )
