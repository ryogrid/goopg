"""Report generation: console, CSV, JSON, HTML, and an SVG histogram (spec §4-5)."""

from __future__ import annotations

import html
import json
import os
from pathlib import Path

import matplotlib

matplotlib.use("Agg")  # headless; no display required.
import matplotlib.pyplot as plt  # noqa: E402
import pandas as pd  # noqa: E402

from .models import Aggregate, FunctionMetric, ProjectSummary

OUTPUT_FILES = (
    "report.html",
    "summary.json",
    "functions.csv",
    "packages.csv",
    "directories.csv",
    "files.csv",
    "histogram.svg",
)


def write_all(
    out_dir: str,
    summary: ProjectSummary,
    metrics: list[FunctionMetric],
    top_functions: int,
    top_packages: int,
    top_files: int,
) -> None:
    """Write every report artifact into ``out_dir`` (created if needed)."""
    os.makedirs(out_dir, exist_ok=True)

    _write_json(out_dir, summary)
    _write_functions_csv(out_dir, metrics)
    _write_aggregate_csv(out_dir, "packages.csv", summary.packages)
    _write_aggregate_csv(out_dir, "directories.csv", summary.directories)
    _write_aggregate_csv(out_dir, "files.csv", summary.files)
    _write_histogram(out_dir, metrics)
    _write_html(out_dir, summary, metrics, top_functions, top_packages, top_files)


# --------------------------------------------------------------------------- #
# JSON / CSV
# --------------------------------------------------------------------------- #
def _write_json(out_dir: str, summary: ProjectSummary) -> None:
    path = os.path.join(out_dir, "summary.json")
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(summary.to_dict(), fh, indent=2, sort_keys=False)
        fh.write("\n")


def _write_functions_csv(out_dir: str, metrics: list[FunctionMetric]) -> None:
    rows = [m.to_row() for m in metrics]
    columns = ["cyclomatic", "cognitive", "package", "function", "file", "line", "directory"]
    df = pd.DataFrame(rows, columns=columns)
    df.to_csv(os.path.join(out_dir, "functions.csv"), index=False)


def _write_aggregate_csv(out_dir: str, name: str, aggs: list[Aggregate]) -> None:
    columns = [
        "key",
        "functions",
        "total_cyclomatic",
        "max_cyclomatic",
        "mean_cyclomatic",
        "total_cognitive",
        "max_cognitive",
        "mean_cognitive",
    ]
    df = pd.DataFrame([a.to_dict() for a in aggs], columns=columns)
    df.to_csv(os.path.join(out_dir, name), index=False)


# --------------------------------------------------------------------------- #
# Histogram
# --------------------------------------------------------------------------- #
# Cyclomatic complexity is heavily right-skewed; clip the tail into a single
# overflow bin so the histogram stays readable (and the SVG stays small).
_HISTOGRAM_CAP = 50


def _write_histogram(out_dir: str, metrics: list[FunctionMetric]) -> None:
    values = [m.cyclomatic for m in metrics]
    fig, ax = plt.subplots(figsize=(8, 4.5))
    if values:
        cap = _HISTOGRAM_CAP
        clipped = [min(v, cap) for v in values]
        bins = range(1, cap + 2)
        ax.hist(clipped, bins=bins, color="#4C78A8", edgecolor="white", linewidth=0.4)
        n_over = sum(1 for v in values if v > cap)
        ax.set_xlabel(f"Cyclomatic complexity (last bin = {cap}+, {n_over} functions)")
    else:
        ax.set_xlabel("Cyclomatic complexity")
    ax.set_title("Cyclomatic complexity distribution")
    ax.set_ylabel("Number of functions")
    ax.grid(axis="y", alpha=0.3)
    fig.tight_layout()
    fig.savefig(os.path.join(out_dir, "histogram.svg"), format="svg")
    plt.close(fig)


# --------------------------------------------------------------------------- #
# HTML
# --------------------------------------------------------------------------- #
def _esc(value: object) -> str:
    return html.escape(str(value))


def _stats_table(title: str, stats) -> str:
    rows = [
        ("Functions", stats.count),
        ("Mean", stats.mean),
        ("Median", stats.median),
        ("Max", stats.maximum),
        ("P90", stats.p90),
        ("P95", stats.p95),
        ("P99", stats.p99),
    ]
    body = "".join(
        f"<tr><th>{_esc(k)}</th><td>{_esc(v)}</td></tr>" for k, v in rows
    )
    thr = "".join(
        f"<tr><th>&gt; {_esc(t)}</th><td>{_esc(n)}</td></tr>"
        for t, n in stats.above_thresholds.items()
    )
    return (
        f"<div class='card'><h3>{_esc(title)}</h3>"
        f"<table class='kv'>{body}</table>"
        f"<h4>Functions above threshold</h4>"
        f"<table class='kv'>{thr}</table></div>"
    )


def _functions_table(metrics: list[FunctionMetric], limit: int) -> str:
    ranked = sorted(metrics, key=lambda m: (-m.cyclomatic, m.file, m.line))[:limit]
    head = (
        "<tr><th>#</th><th>Cyclomatic</th><th>Cognitive</th><th>Function</th>"
        "<th>Package</th><th>File:Line</th></tr>"
    )
    rows = []
    for i, m in enumerate(ranked, 1):
        cog = "" if m.cognitive is None else m.cognitive
        rows.append(
            f"<tr><td>{i}</td><td class='num'>{_esc(m.cyclomatic)}</td>"
            f"<td class='num'>{_esc(cog)}</td><td><code>{_esc(m.function)}</code></td>"
            f"<td>{_esc(m.package)}</td><td>{_esc(m.file)}:{_esc(m.line)}</td></tr>"
        )
    return f"<table class='rank'>{head}{''.join(rows)}</table>"


def _aggregate_table(aggs: list[Aggregate], limit: int, key_label: str) -> str:
    head = (
        f"<tr><th>#</th><th>{_esc(key_label)}</th><th>Functions</th>"
        "<th>Total CC</th><th>Max CC</th><th>Mean CC</th></tr>"
    )
    rows = []
    for i, a in enumerate(aggs[:limit], 1):
        rows.append(
            f"<tr><td>{i}</td><td>{_esc(a.key)}</td><td class='num'>{_esc(a.functions)}</td>"
            f"<td class='num'>{_esc(a.total_cyclomatic)}</td>"
            f"<td class='num'>{_esc(a.max_cyclomatic)}</td>"
            f"<td class='num'>{_esc(a.mean_cyclomatic)}</td></tr>"
        )
    return f"<table class='rank'>{head}{''.join(rows)}</table>"


_CSS = """
:root { color-scheme: light dark; }
body { font-family: -apple-system, Segoe UI, Roboto, sans-serif; margin: 2rem;
       line-height: 1.45; }
h1 { margin-bottom: 0.2rem; }
.meta { color: #888; margin-bottom: 1.5rem; }
.cards { display: flex; gap: 1rem; flex-wrap: wrap; margin-bottom: 1.5rem; }
.card { border: 1px solid #8884; border-radius: 8px; padding: 1rem 1.25rem;
        min-width: 220px; }
table { border-collapse: collapse; margin: 0.5rem 0 1.5rem; width: 100%; }
table.kv { width: auto; }
th, td { text-align: left; padding: 0.25rem 0.7rem; border-bottom: 1px solid #8883; }
td.num { text-align: right; font-variant-numeric: tabular-nums; }
table.rank th { background: #8881; position: sticky; top: 0; }
code { font-family: ui-monospace, Menlo, monospace; }
figure { margin: 0 0 1.5rem; }
.svgwrap { max-width: 720px; overflow-x: auto; }
"""


def _write_html(
    out_dir: str,
    summary: ProjectSummary,
    metrics: list[FunctionMetric],
    top_functions: int,
    top_packages: int,
    top_files: int,
) -> None:
    svg = Path(os.path.join(out_dir, "histogram.svg")).read_text(encoding="utf-8")
    # Strip the XML prolog so the SVG can be inlined inside the HTML body.
    svg_inline = svg[svg.find("<svg") :] if "<svg" in svg else svg

    doc = f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Go Complexity Report</title>
<style>{_CSS}</style>
</head>
<body>
<h1>Go Codebase Complexity Report</h1>
<p class="meta">Generated {_esc(summary.generated_at)} &middot;
roots: {_esc(", ".join(summary.roots))} &middot;
{_esc(summary.num_files)} files &middot;
{_esc(summary.num_packages)} packages &middot;
{_esc(summary.num_functions)} functions</p>

<div class="cards">
  {_stats_table("Cyclomatic complexity", summary.cyclomatic)}
  {_stats_table("Cognitive complexity", summary.cognitive)}
</div>

<figure><figcaption><strong>Distribution</strong></figcaption>
<div class="svgwrap">{svg_inline}</div></figure>

<h2>Top {min(top_functions, len(metrics))} functions by cyclomatic complexity</h2>
{_functions_table(metrics, top_functions)}

<h2>Top {min(top_packages, len(summary.packages))} packages</h2>
{_aggregate_table(summary.packages, top_packages, "Package")}

<h2>Top {min(top_files, len(summary.files))} files</h2>
{_aggregate_table(summary.files, top_files, "File")}

</body>
</html>
"""
    with open(os.path.join(out_dir, "report.html"), "w", encoding="utf-8") as fh:
        fh.write(doc)


# --------------------------------------------------------------------------- #
# Console
# --------------------------------------------------------------------------- #
def print_console_summary(
    summary: ProjectSummary, metrics: list[FunctionMetric], top_n: int = 15
) -> None:
    """Print a compact human-readable summary to stdout."""
    s = summary
    print(f"Go Codebase Complexity — {s.num_functions} functions across "
          f"{s.num_files} files, {s.num_packages} packages")
    print(f"  roots: {', '.join(s.roots)}")
    print()
    _print_metric_line("Cyclomatic", s.cyclomatic)
    _print_metric_line("Cognitive ", s.cognitive)
    print()
    print(f"Top {min(top_n, len(metrics))} functions by cyclomatic complexity:")
    ranked = sorted(metrics, key=lambda m: (-m.cyclomatic, m.file, m.line))[:top_n]
    for m in ranked:
        cog = "-" if m.cognitive is None else str(m.cognitive)
        print(f"  cc={m.cyclomatic:>3}  cog={cog:>3}  {m.function}  "
              f"({m.file}:{m.line})")


def _print_metric_line(label: str, stats) -> None:
    thr = "  ".join(f">{t}:{n}" for t, n in stats.above_thresholds.items())
    print(f"  {label}: mean={stats.mean}  median={stats.median}  "
          f"max={stats.maximum}  p90={stats.p90}  p95={stats.p95}  "
          f"p99={stats.p99}   [{thr}]")
