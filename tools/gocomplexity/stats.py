"""Statistics engine: descriptive stats and package/dir/file roll-ups (spec §3).

Complexity comes from the function metrics (gocyclo/gocognit); LOC and the
Maintainability Index come from the source scan (:mod:`sourcemetrics`). The two
are joined here, keyed by file path.
"""

from __future__ import annotations

from collections import defaultdict

import numpy as np

from .config import Config
from .models import Aggregate, FunctionMetric, MetricStats, ProjectSummary
from .sourcemetrics import DuplicationResult, FileSource, maintainability_index


def _metric_stats(values: list[int], thresholds: list[int]) -> MetricStats:
    """Compute descriptive statistics for one metric over ``values``.

    Percentiles use numpy's default linear interpolation for deterministic,
    reproducible output (spec §7). An empty input yields an all-zero record.
    """
    if not values:
        return MetricStats(
            count=0,
            mean=0.0,
            median=0.0,
            maximum=0,
            p90=0.0,
            p95=0.0,
            p99=0.0,
            above_thresholds={t: 0 for t in thresholds},
        )
    arr = np.asarray(values, dtype=float)
    return MetricStats(
        count=len(values),
        mean=round(float(arr.mean()), 4),
        median=round(float(np.median(arr)), 4),
        maximum=int(arr.max()),
        p90=round(float(np.percentile(arr, 90)), 4),
        p95=round(float(np.percentile(arr, 95)), 4),
        p99=round(float(np.percentile(arr, 99)), 4),
        above_thresholds={t: int((arr > t).sum()) for t in thresholds},
    )


def _aggregate(
    metrics: list[FunctionMetric],
    key_fn,
    file_loc: dict[str, int],
    file_mi: dict[str, float],
) -> list[Aggregate]:
    """Group ``metrics`` by ``key_fn`` and roll up complexity, LOC, and MI.

    LOC of a group is the sum of its distinct files' code lines; the group MI is
    the LOC-weighted mean of those files' Maintainability Index.
    """
    buckets: dict[str, list[FunctionMetric]] = defaultdict(list)
    files_in: dict[str, set[str]] = defaultdict(set)
    for m in metrics:
        k = key_fn(m)
        buckets[k].append(m)
        files_in[k].add(m.file)

    result: list[Aggregate] = []
    for key, items in buckets.items():
        cyc = [m.cyclomatic for m in items]
        cog = [m.cognitive for m in items if m.cognitive is not None]
        group_files = files_in[key]
        loc = sum(file_loc.get(f, 0) for f in group_files)
        if loc > 0:
            mi = sum(file_mi.get(f, 100.0) * file_loc.get(f, 0) for f in group_files) / loc
        else:
            mi = 100.0
        result.append(
            Aggregate(
                key=key,
                functions=len(items),
                total_cyclomatic=sum(cyc),
                max_cyclomatic=max(cyc),
                mean_cyclomatic=round(sum(cyc) / len(cyc), 4),
                total_cognitive=sum(cog),
                max_cognitive=max(cog) if cog else 0,
                mean_cognitive=round(sum(cog) / len(cog), 4) if cog else 0.0,
                loc=loc,
                maintainability_index=round(mi, 2),
            )
        )
    # Rank by total cyclomatic (desc), then key (asc) for stable ordering.
    result.sort(key=lambda a: (-a.total_cyclomatic, a.key))
    return result


def build_summary(
    metrics: list[FunctionMetric],
    config: Config,
    generated_at: str,
    num_files: int,
    sources: dict[str, FileSource],
    duplication: DuplicationResult,
) -> ProjectSummary:
    """Assemble a :class:`ProjectSummary` from function metrics + source scan."""
    thresholds = config.thresholds
    cyc_stats = _metric_stats([m.cyclomatic for m in metrics], thresholds)
    cog_stats = _metric_stats(
        [m.cognitive for m in metrics if m.cognitive is not None], thresholds
    )

    # Per-file mean cyclomatic complexity, then per-file Maintainability Index.
    cc_by_file: dict[str, list[int]] = defaultdict(list)
    for m in metrics:
        cc_by_file[m.file].append(m.cyclomatic)

    # The MI constants are calibrated for module-sized units, so we feed
    # per-function averages (avg volume / avg cyclomatic / avg LOC per function
    # in the file) rather than whole-file totals — otherwise large files clamp
    # to 0. Files with no functions are treated as maximally maintainable.
    file_loc: dict[str, int] = {}
    file_mi: dict[str, float] = {}
    for rel, fs in sources.items():
        file_loc[rel] = fs.loc
        ccs = cc_by_file.get(rel, [])
        nfun = len(ccs)
        if nfun == 0:
            file_mi[rel] = 100.0
            continue
        avg_cc = sum(ccs) / nfun
        avg_vol = fs.halstead.volume / nfun
        avg_loc = fs.loc / nfun
        file_mi[rel] = maintainability_index(avg_vol, avg_cc, avg_loc)

    project_loc = sum(fs.loc for fs in sources.values())
    project_volume = sum(fs.halstead.volume for fs in sources.values())
    if project_loc > 0:
        project_mi = sum(file_mi[r] * file_loc[r] for r in sources) / project_loc
    else:
        project_mi = 100.0

    dup_pct = duplication.pct
    dup_lines = duplication.dup_lines
    total_code = duplication.total_lines

    packages = _aggregate(metrics, lambda m: m.package, file_loc, file_mi)
    directories = _aggregate(metrics, lambda m: m.directory, file_loc, file_mi)
    files = _aggregate(metrics, lambda m: m.file, file_loc, file_mi)

    return ProjectSummary(
        generated_at=generated_at,
        roots=list(config.roots),
        num_files=num_files,
        num_packages=len(packages),
        num_functions=len(metrics),
        loc=project_loc,
        maintainability_index=round(project_mi, 2),
        duplicate_code_pct=dup_pct,
        duplicate_code_lines=dup_lines,
        total_code_lines=total_code,
        duplicate_window_count=duplication.unique_windows,
        duplicate_multiplicity_histogram=duplication.multiplicity_histogram,
        halstead_volume=round(project_volume, 2),
        cyclomatic=cyc_stats,
        cognitive=cog_stats,
        thresholds=list(thresholds),
        packages=packages,
        directories=directories,
        files=files,
    )
