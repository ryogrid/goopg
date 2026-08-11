"""Document block parser.

Splits a markdown document into blocks: fenced code blocks,
tables, and plain text.  Table-like lines inside fenced code
blocks are ignored.
"""

from __future__ import annotations

from .detector import detect_table, is_separator_row
from .models import CodeBlock, Document, TextBlock


def _is_table_line(line: str) -> bool:
    """Return True if *line* looks like a table row candidate."""
    stripped = line.strip()
    return stripped.startswith("|") or stripped.endswith("|")


def _starts_new_table(lines: list[str], idx: int) -> bool:
    """Return True if a *new* table begins at ``lines[idx]``.

    A fresh table is a header line immediately followed by a separator row.
    This is what distinguishes two independent tables separated by a blank
    line — which must stay separate — from a single table whose body was
    accidentally split by one (they have no second header/separator pair).
    """
    return (
        idx + 1 < len(lines)
        and _is_table_line(lines[idx])
        and is_separator_row(lines[idx + 1])
    )


def _is_fence_line(line: str) -> bool:
    """Return True if *line* is a fenced-code-block delimiter.

    Handles both ``` and ~~~ fences with optional info string.
    """
    stripped = line.strip()
    return stripped.startswith("```") or stripped.startswith("~~~")


def _find_closing_fence(lines: list[str], start: int, fence_char: str) -> int:
    """Return the index of the closing fence line, or len(lines) if unmatched."""
    # The opening fence may have an info string; the closing fence is the
    # fence char repeated 3+ times with optional trailing whitespace.
    fence_prefix = fence_char * 3
    for idx in range(start, len(lines)):
        stripped = lines[idx].strip()
        if stripped.startswith(fence_prefix) and all(
            c == fence_char for c in stripped
        ):
            return idx
    return len(lines)


def parse_document(text: str) -> Document:
    """Split *text* into ordered blocks.

    - Lines between `` ``` `` / `` ~~~ `` fences become **CodeBlock**.
    - Consecutive ``|``-lines that pass table validation become **Table**.
    - Everything else becomes **TextBlock**.

    Args:
        text: The full markdown document as a string.

    Returns:
        A Document whose ``blocks`` list preserves input order.
    """
    raw_lines = text.split("\n")
    blocks: list[CodeBlock | TextBlock | Table] = []
    i = 0

    while i < len(raw_lines):
        line = raw_lines[i]

        # Skip trailing empty lines at end of document.
        if line == "" and i == len(raw_lines) - 1:
            i += 1
            continue

        # ---- fenced code block ---------------------------------------
        if _is_fence_line(line):
            stripped = line.strip()
            fence_char = stripped[0]  # '`' or '~'
            fence_start = i
            close_idx = _find_closing_fence(raw_lines, i + 1, fence_char)
            if close_idx < len(raw_lines):
                block_lines = raw_lines[fence_start : close_idx + 1]
                i = close_idx + 1
            else:
                # Unclosed fence — include everything to EOF.
                block_lines = raw_lines[fence_start:]
                i = len(raw_lines)
            blocks.append(CodeBlock(lines=block_lines, start_line=fence_start + 1))
            continue

        # ---- table candidate ------------------------------------------
        if _is_table_line(line):
            table_lines: list[str] = []
            start_line = i + 1  # 1-based

            # Collect consecutive table lines.  A run of blank lines is
            # absorbed when what follows continues THIS table body (a
            # formatting error: in GFM the blank line ends the table and the
            # rest renders as literal text).  A run followed by a fresh
            # header+separator pair is left alone — those are two separate
            # tables and merging them would corrupt both.
            while i < len(raw_lines):
                if _is_table_line(raw_lines[i]):
                    table_lines.append(raw_lines[i])
                    i += 1
                    continue
                if raw_lines[i].strip() != "":
                    break

                blank_end = i
                while (
                    blank_end < len(raw_lines)
                    and raw_lines[blank_end].strip() == ""
                ):
                    blank_end += 1
                if (
                    blank_end < len(raw_lines)
                    and _is_table_line(raw_lines[blank_end])
                    and not _starts_new_table(raw_lines, blank_end)
                ):
                    table_lines.extend(raw_lines[i:blank_end])
                    i = blank_end
                else:
                    break

            table = detect_table(table_lines, start_line)
            if table is not None:
                blocks.append(table)
            else:
                # Not a valid table — treat as text.
                blocks.append(TextBlock(lines=table_lines, start_line=start_line))
            continue

        # ---- plain text -----------------------------------------------
        blocks.append(TextBlock(lines=[line], start_line=i + 1))
        i += 1

    return Document(blocks=blocks)
