"""Table formatter with column-width alignment.

Reconstructs a markdown table string from a Table object,
computing per-column widths and applying alignment markers
from the original separator row.
"""

from __future__ import annotations

from .models import Row, Table


def _display_width(text: str) -> int:
    """Return the display width of *text*.

    For pure ASCII this equals ``len(text)``.
    """
    return len(text)


def _compute_column_widths(table: Table) -> list[int]:
    """Compute the display width for each column.

    Width is the maximum cell width across header, separator, and
    all data rows, with a floor of 3 (for alignment markers ``:--``).
    """
    num_cols = len(table.header.cells)
    widths = [3] * num_cols

    all_rows: list[Row] = [table.header] + table.data_rows
    for row in all_rows:
        for idx, cell in enumerate(row.cells):
            if idx < num_cols:
                w = _display_width(cell.content)
                if w > widths[idx]:
                    widths[idx] = w

    return widths


def _pad_cell(content: str, width: int, align: str) -> str:
    """Pad *content* to *width* characters using *align*.

    Args:
        content: The cell text.
        width: Target display width (minimum 1).
        align: One of ``"left"``, ``"right"``, ``"center"``.
    """
    pad_total = max(0, width - _display_width(content))
    if align == "right":
        return " " * pad_total + content
    elif align == "center":
        left_pad = pad_total // 2
        right_pad = pad_total - left_pad
        return " " * left_pad + content + " " * right_pad
    else:  # left (default)
        return content + " " * pad_total


def _format_separator(alignment: list[str], widths: list[int]) -> str:
    """Build a separator row string from alignment and widths."""
    parts: list[str] = []
    for align, w in zip(alignment, widths):
        w = max(w, 3)
        if align == "center":
            parts.append(":" + "-" * (w - 2) + ":")
        elif align == "right":
            parts.append("-" * (w - 1) + ":")
        else:  # left
            parts.append(":" + "-" * (w - 1))
    return "| " + " | ".join(parts) + " |"


def format_table(table: Table, compact: bool = False) -> str:
    """Format a Table as a markdown table string.

    When *compact* is False (the default), columns are padded to
    uniform width using the computed maximum per column.  When
    *compact* is True, cells use minimal formatting: one space
    around each cell — suitable for tables with very wide cells
    where uniform-width padding would produce unreadable output.

    Alignment markers from the original separator row are
    preserved in both modes.
    """
    alignment = table.alignment
    num_cols = len(alignment)
    widths = _compute_column_widths(table)

    lines: list[str] = []

    if compact:
        # Header — minimal.
        hdr = "| " + " | ".join(
            c.content for c in table.header.cells[:num_cols]
        ) + " |"
        lines.append(hdr)

        # Separator — preserve original alignment markers at width 3.
        sep_parts: list[str] = []
        for al in alignment:
            if al == "center":
                sep_parts.append(":---:")
            elif al == "right":
                sep_parts.append("---:")
            else:
                sep_parts.append(":---")
            # Keep original longer markers if they exist.
        lines.append(_format_separator(alignment, [3] * num_cols))

        # Data rows — minimal.
        for row in table.data_rows:
            row_content = "| " + " | ".join(
                row.cells[i].content if i < len(row.cells) else ""
                for i in range(num_cols)
            ) + " |"
            lines.append(row_content)

        return "\n".join(lines)

    # Full-width alignment mode.
    # Header.
    header_parts = [
        _pad_cell(cell.content, widths[i], alignment[i])
        for i, cell in enumerate(table.header.cells[:num_cols])
    ]
    lines.append("| " + " | ".join(header_parts) + " |")

    # Separator.
    lines.append(_format_separator(alignment, widths))

    # Data rows.
    for row in table.data_rows:
        row_parts: list[str] = []
        for i in range(num_cols):
            content = row.cells[i].content if i < len(row.cells) else ""
            row_parts.append(_pad_cell(content, widths[i], alignment[i]))
        lines.append("| " + " | ".join(row_parts) + " |")

    return "\n".join(lines)
