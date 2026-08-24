"""Structured JSON diagnostics for table repairs."""

from __future__ import annotations

from .models import Fix


def build_report(filepath: str, fixes: list[Fix]) -> dict:
    """Build a structured JSON-serialisable report from repair actions.

    Args:
        filepath: Path to the file that was processed.
        fixes: All repair actions recorded during processing.

    Returns:
        A dict suitable for ``json.dump`` with ``total_issues``,
        ``unrepaired_issues`` (findings the tool cannot fix on its own),
        ``summary`` (counts by type), and ``details`` (per-fix records).
    """
    by_type: dict[str, list[dict]] = {}
    for fix in fixes:
        by_type.setdefault(fix.type, []).append(
            {
                "line": fix.line,
                "column": fix.column,
                "detail": fix.detail,
                "repaired": fix.repaired,
            }
        )

    return {
        "file": filepath,
        "total_issues": len(fixes),
        "unrepaired_issues": sum(1 for f in fixes if not f.repaired),
        "summary": {
            "escaped_pipes": sum(1 for f in fixes if f.type == "escaped_pipe"),
            "missing_cells": sum(1 for f in fixes if f.type == "missing_cell"),
            "extra_cells": sum(1 for f in fixes if f.type == "extra_cell"),
            "blank_lines": sum(1 for f in fixes if f.type == "blank_line"),
            "missing_outer_pipes": sum(
                1 for f in fixes if f.type == "missing_outer_pipe"
            ),
            "html_tags": sum(1 for f in fixes if f.type == "html_tag"),
            "lost_separators": sum(
                1 for f in fixes if f.type == "lost_separator"
            ),
        },
        "details": by_type,
    }
