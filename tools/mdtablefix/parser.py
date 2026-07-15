"""Document block parser.

Splits a markdown document into blocks: fenced code blocks,
tables, and plain text.  Table-like lines inside fenced code
blocks are ignored.
"""

from __future__ import annotations

from .detector import detect_table
from .models import CodeBlock, Document, TextBlock


def _is_table_line(line: str) -> bool:
    """Return True if *line* looks like a table row candidate."""
    stripped = line.strip()
    return stripped.startswith("|") or stripped.endswith("|")


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

            # Collect consecutive table lines.  Blank lines between
            # table rows are absorbed (they are formatting errors
            # within the table body).
            while i < len(raw_lines):
                stripped = raw_lines[i].strip()
                if _is_table_line(raw_lines[i]):
                    table_lines.append(raw_lines[i])
                    i += 1
                elif stripped == "" and i + 1 < len(raw_lines) and _is_table_line(raw_lines[i + 1]):
                    # Blank line followed by another table row — absorb it.
                    table_lines.append(raw_lines[i])
                    i += 1
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
