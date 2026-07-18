"""Statistics engine: descriptive stats and package/dir/file roll-ups (spec §3)."""

from __future__ import annotations

from collections import defaultdict

import numpy as np

from .config import Config
from .models import Aggregate, FunctionMetric, MetricStats, ProjectSummary


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


def _aggregate(metrics: list[FunctionMetric], key_fn) -> list[Aggregate]:
    """Group ``metrics`` by ``key_fn`` and roll up cyclomatic/cognitive totals."""
    buckets: dict[str, list[FunctionMetric]] = defaultdict(list)
    for m in metrics:
        buckets[key_fn(m)].append(m)

    result: list[Aggregate] = []
    for key, items in buckets.items():
        cyc = [m.cyclomatic for m in items]
        cog = [m.cognitive for m in items if m.cognitive is not None]
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
) -> ProjectSummary:
    """Assemble a :class:`ProjectSummary` from per-function metrics."""
    thresholds = config.thresholds
    cyc_stats = _metric_stats([m.cyclomatic for m in metrics], thresholds)
    cog_stats = _metric_stats(
        [m.cognitive for m in metrics if m.cognitive is not None], thresholds
    )

    packages = _aggregate(metrics, lambda m: m.package)
    directories = _aggregate(metrics, lambda m: m.directory)
    files = _aggregate(metrics, lambda m: m.file)

    return ProjectSummary(
        generated_at=generated_at,
        roots=list(config.roots),
        num_files=num_files,
        num_packages=len(packages),
        num_functions=len(metrics),
        cyclomatic=cyc_stats,
        cognitive=cog_stats,
        thresholds=list(thresholds),
        packages=packages,
        directories=directories,
        files=files,
    )
