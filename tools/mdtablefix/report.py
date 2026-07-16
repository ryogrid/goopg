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
        ``summary`` (counts by type), and ``details`` (per-fix records).
    """
    by_type: dict[str, list[dict]] = {}
    for fix in fixes:
        by_type.setdefault(fix.type, []).append(
            {
                "line": fix.line,
                "column": fix.column,
                "detail": fix.detail,
            }
        )

    return {
        "file": filepath,
        "total_issues": len(fixes),
        "summary": {
            "escaped_pipes": sum(1 for f in fixes if f.type == "escaped_pipe"),
            "missing_cells": sum(1 for f in fixes if f.type == "missing_cell"),
            "extra_cells": sum(1 for f in fixes if f.type == "extra_cell"),
        },
        "details": by_type,
    }
