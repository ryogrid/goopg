#!/usr/bin/env python3
"""CLI entry point for gocomplexity.

Collect Go sources, measure cyclomatic (gocyclo) and cognitive (gocognit)
complexity, compute statistics, and emit a timestamped report bundle.
"""

from __future__ import annotations

import argparse
import os
import sys
from datetime import datetime

from . import __version__
from .collector import collect_go_files
from .config import Config, load_config
from .reports import print_console_summary, write_all
from .runner import ToolNotFoundError, analyze
from .sourcemetrics import duplication_pct, scan_sources
from .stats import build_summary

# Default parent directory for timestamped report dirs (beside the tool).
_DEFAULT_REPORTS_ROOT = os.path.join("tools", "gocomplexity", "reports")


def build_parser() -> argparse.ArgumentParser:
    """Construct the argument parser."""
    parser = argparse.ArgumentParser(
        prog="gocomplexity",
        description="Measure Go cyclomatic/cognitive complexity and report on "
        "codebase health.",
    )
    parser.add_argument(
        "paths",
        nargs="*",
        metavar="ROOT",
        help="Root paths to analyze (default: from config, else 'internal cmd').",
    )
    parser.add_argument("--config", metavar="PATH", help="YAML config file.")
    parser.add_argument(
        "--output-dir",
        metavar="DIR",
        help="Parent dir for the timestamped report dir "
        f"(default: {_DEFAULT_REPORTS_ROOT}).",
    )
    parser.add_argument(
        "--base",
        metavar="DIR",
        default=".",
        help="Repository root the ROOT paths are relative to (default: '.').",
    )
    parser.add_argument(
        "--exclude-dir",
        action="append",
        default=[],
        metavar="NAME",
        help="Additional directory name to exclude (repeatable).",
    )
    parser.add_argument(
        "--threshold",
        action="append",
        type=int,
        default=[],
        metavar="N",
        help="Complexity threshold to count functions above (repeatable).",
    )
    parser.add_argument("--top-functions", type=int, metavar="N")
    parser.add_argument("--top-packages", type=int, metavar="N")
    parser.add_argument("--top-files", type=int, metavar="N")
    parser.add_argument(
        "--fail-over",
        type=int,
        metavar="N",
        help="Exit 1 if any function's cyclomatic complexity exceeds N (CI gate).",
    )
    parser.add_argument(
        "--dup-min-lines",
        type=int,
        metavar="N",
        help="Minimum consecutive code lines for a duplicate block (default 6).",
    )
    parser.add_argument(
        "--no-duplication",
        action="store_true",
        help="Skip duplicate-code detection (the most expensive pass).",
    )
    parser.add_argument(
        "--quiet", action="store_true", help="Suppress the console summary."
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    return parser


def _resolve_config(args: argparse.Namespace) -> Config:
    """Merge CLI flags over the loaded/default config (CLI wins)."""
    cfg = load_config(args.config)
    if args.paths:
        cfg.roots = list(args.paths)
    if args.exclude_dir:
        cfg.exclude_dirs = cfg.exclude_dirs + list(args.exclude_dir)
    if args.threshold:
        cfg.thresholds = list(args.threshold)
    if args.top_functions is not None:
        cfg.top_functions = args.top_functions
    if args.top_packages is not None:
        cfg.top_packages = args.top_packages
    if args.top_files is not None:
        cfg.top_files = args.top_files
    if args.dup_min_lines is not None:
        cfg.duplication_min_lines = args.dup_min_lines
    return cfg.normalized()


def main(argv: list[str] | None = None) -> None:
    """Run the gocomplexity CLI."""
    args = build_parser().parse_args(argv)

    try:
        config = _resolve_config(args)
    except (FileNotFoundError, ValueError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(2)

    files = collect_go_files(config, base=args.base)
    if not files:
        print(
            f"Error: no Go files found under {config.roots} (base={args.base}).",
            file=sys.stderr,
        )
        sys.exit(1)

    try:
        metrics = analyze(files, cwd=args.base)
    except ToolNotFoundError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(3)
    except RuntimeError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(3)

    # Source-level metrics (LOC / Halstead / Maintainability Index / duplication)
    # over the same production-only file set.
    sources = scan_sources(files, base=args.base)
    if args.no_duplication:
        total_code = sum(fs.loc for fs in sources.values())
        duplication = (0.0, 0, total_code)
    else:
        duplication = duplication_pct(sources, config.duplication_min_lines)

    generated_at = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    stamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    summary = build_summary(
        metrics,
        config,
        generated_at,
        num_files=len(files),
        sources=sources,
        duplication=duplication,
    )

    reports_root = args.output_dir or _DEFAULT_REPORTS_ROOT
    out_dir = os.path.join(reports_root, f"report_{stamp}")
    write_all(
        out_dir,
        summary,
        metrics,
        top_functions=config.top_functions,
        top_packages=config.top_packages,
        top_files=config.top_files,
    )

    if not args.quiet:
        print_console_summary(summary, metrics)
        print()
    print(f"Reports written to {out_dir}/")

    if args.fail_over is not None:
        over = [m for m in metrics if m.cyclomatic > args.fail_over]
        if over:
            print(
                f"FAIL: {len(over)} function(s) exceed cyclomatic complexity "
                f"{args.fail_over}.",
                file=sys.stderr,
            )
            sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
