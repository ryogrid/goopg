#!/usr/bin/env python3
"""CLI entry point for mdtablefix.

Detect and repair malformed GitHub-Flavored Markdown tables.

Modes:
    --check    Report issues, exit non-zero if malformed.
    --fix      Print repaired document to stdout.
    --inplace  Overwrite the file with repaired content.
"""

from __future__ import annotations

import argparse
import json
import sys

from .formatter import format_table
from .models import CodeBlock, Table, TextBlock
from .parser import parse_document
from .repair import repair_table
from .report import build_report


def build_parser() -> argparse.ArgumentParser:
    """Construct the argument parser."""
    parser = argparse.ArgumentParser(
        prog="mdtablefix",
        description="Detect and repair malformed Markdown tables.",
    )

    mode = parser.add_mutually_exclusive_group()
    mode.add_argument(
        "--check",
        action="store_true",
        help="Report issues to stderr; exit 1 if any table is malformed.",
    )
    mode.add_argument(
        "--fix",
        action="store_true",
        help="Print the repaired document to stdout.",
    )
    mode.add_argument(
        "--inplace",
        action="store_true",
        help="Overwrite FILE with the repaired content.",
    )

    parser.add_argument(
        "--report",
        type=str,
        default=None,
        metavar="PATH",
        help="Write structured diagnostics as JSON to PATH.",
    )
    parser.add_argument(
        "--align",
        action="store_true",
        help="Pad columns to uniform width (default: compact, no padding).",
    )
    parser.add_argument(
        "file",
        type=str,
        metavar="FILE",
        help="Markdown file to process.",
    )

    return parser


def _render_block(
    block: CodeBlock | TextBlock | Table, compact: bool = True
) -> str:
    """Render a single document block to its text representation."""
    if isinstance(block, Table):
        return format_table(block, compact=compact)
    else:
        return "\n".join(block.lines)


def _warn_unrepaired(unrepaired: list) -> None:
    """Print the findings the tool deliberately did not rewrite."""
    if not unrepaired:
        return
    print(
        f"{len(unrepaired)} issue(s) need manual repair:", file=sys.stderr
    )
    for fix in unrepaired:
        print(
            f"  Line {fix.line}, col {fix.column}: [{fix.type}] {fix.detail}",
            file=sys.stderr,
        )


def main(argv: list[str] | None = None) -> None:
    """Run the mdtablefix CLI.

    Args:
        argv: Command-line arguments (defaults to ``sys.argv[1:]``).
    """
    parser = build_parser()
    args = parser.parse_args(argv)

    # Determine mode (default: check).
    mode: str = "check"
    if args.fix:
        mode = "fix"
    elif args.inplace:
        mode = "inplace"

    # Read input file.
    try:
        with open(args.file, encoding="utf-8") as fh:
            text = fh.read()
    except FileNotFoundError:
        print(f"Error: file not found: {args.file}", file=sys.stderr)
        sys.exit(1)
    except UnicodeDecodeError as exc:
        print(f"Error: invalid UTF-8 in {args.file}: {exc}", file=sys.stderr)
        sys.exit(1)

    # Parse → repair → format.
    doc = parse_document(text)
    all_fixes: list = []
    has_malformed = False

    for block in doc.blocks:
        if isinstance(block, Table):
            repaired = repair_table(block)
            if repaired.fixes:
                has_malformed = True
                all_fixes.extend(repaired.fixes)

    unrepaired = [f for f in all_fixes if not f.repaired]

    # ---- write report (before any mode may exit) ---------------------
    if args.report:
        report_data = build_report(args.file, all_fixes)
        with open(args.report, "w", encoding="utf-8") as fh:
            json.dump(report_data, fh, indent=2)
            fh.write("\n")

    # ---- output -------------------------------------------------------
    if mode == "check":
        for fix in all_fixes:
            mark = "" if fix.repaired else " NEEDS MANUAL REPAIR:"
            print(
                f"Line {fix.line}, col {fix.column}: "
                f"[{fix.type}]{mark} {fix.detail}",
                file=sys.stderr,
            )
        if all_fixes:
            print(
                f"\nFound {len(all_fixes)} issue(s) in {args.file}"
                + (
                    f" ({len(unrepaired)} need manual repair)."
                    if unrepaired
                    else "."
                ),
                file=sys.stderr,
            )
            sys.exit(1)
        else:
            print(f"No issues found in {args.file}.")
            sys.exit(0)

    # For --fix and --inplace, render the full document.
    rendered_blocks = [
        _render_block(b, compact=not args.align) for b in doc.blocks
    ]
    output = "\n".join(rendered_blocks)

    if mode == "fix":
        sys.stdout.write(output)
        if not output.endswith("\n"):
            sys.stdout.write("\n")
        _warn_unrepaired(unrepaired)
        sys.exit(1 if has_malformed else 0)

    if mode == "inplace":
        with open(args.file, "w", encoding="utf-8") as fh:
            fh.write(output)
            if not output.endswith("\n"):
                fh.write("\n")
        repaired_count = len(all_fixes) - len(unrepaired)
        if has_malformed:
            print(f"Repaired {repaired_count} issue(s) in {args.file}.")
        else:
            print(f"No issues found in {args.file}.")
        _warn_unrepaired(unrepaired)
        # A leftover unrepairable finding must not read as success: the file
        # still renders wrong and a human has to touch it.
        sys.exit(1 if unrepaired else 0)


if __name__ == "__main__":
    main()
