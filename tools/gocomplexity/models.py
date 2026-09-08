"""Data models for gocomplexity.

All records are plain dataclasses so they serialize cleanly to JSON/CSV and stay
trivially comparable in tests.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass, field


@dataclass(frozen=True)
class FunctionMetric:
    """A single Go function's complexity metrics.

    ``cognitive`` is ``None`` when gocognit did not report the function (should
    not normally happen, but is tolerated rather than fatal).
    """

    cyclomatic: int
    cognitive: int | None
    package: str
    function: str
    file: str
    line: int
    directory: str

    def to_row(self) -> dict[str, object]:
        """Flat dict for CSV/JSON, with a stable column order."""
        return {
            "cyclomatic": self.cyclomatic,
            "cognitive": "" if self.cognitive is None else self.cognitive,
            "package": self.package,
            "function": self.function,
            "file": self.file,
            "line": self.line,
            "directory": self.directory,
        }


@dataclass
class MetricStats:
    """Descriptive statistics for one metric over a set of functions."""

    count: int
    mean: float
    median: float
    maximum: int
    p90: float
    p95: float
    p99: float
    above_thresholds: dict[int, int]  # threshold -> count of functions strictly above

    def to_dict(self) -> dict[str, object]:
        d = asdict(self)
        # JSON object keys must be strings.
        d["above_thresholds"] = {str(k): v for k, v in self.above_thresholds.items()}
        return d


@dataclass
class Aggregate:
    """A package / directory / file roll-up of complexity."""

    key: str
    functions: int
    total_cyclomatic: int
    max_cyclomatic: int
    mean_cyclomatic: float
    total_cognitive: int
    max_cognitive: int
    mean_cognitive: float
    loc: int = 0
    maintainability_index: float = 0.0

    def to_dict(self) -> dict[str, object]:
        return asdict(self)


@dataclass
class ProjectSummary:
    """Top-level report payload."""

    generated_at: str
    roots: list[str]
    num_files: int
    num_packages: int
    num_functions: int
    loc: int
    maintainability_index: float
    duplicate_code_pct: float
    duplicate_code_lines: int
    total_code_lines: int
    halstead_volume: float
    cyclomatic: MetricStats
    cognitive: MetricStats
    thresholds: list[int]
    packages: list[Aggregate] = field(default_factory=list)
    directories: list[Aggregate] = field(default_factory=list)
    files: list[Aggregate] = field(default_factory=list)
    duplicate_window_count: int = 0
    duplicate_multiplicity_histogram: dict[int, int] = field(default_factory=dict)

    def to_dict(self) -> dict[str, object]:
        return {
            "generated_at": self.generated_at,
            "roots": self.roots,
            "num_files": self.num_files,
            "num_packages": self.num_packages,
            "num_functions": self.num_functions,
            "loc": self.loc,
            "maintainability_index": self.maintainability_index,
            "duplicate_code_pct": self.duplicate_code_pct,
            "duplicate_code_lines": self.duplicate_code_lines,
            "total_code_lines": self.total_code_lines,
            "duplicate_window_count": self.duplicate_window_count,
            "duplicate_multiplicity_histogram": {
                str(k): v for k, v in self.duplicate_multiplicity_histogram.items()
            },
            "halstead_volume": self.halstead_volume,
            "thresholds": self.thresholds,
            "cyclomatic": self.cyclomatic.to_dict(),
            "cognitive": self.cognitive.to_dict(),
            "packages": [a.to_dict() for a in self.packages],
            "directories": [a.to_dict() for a in self.directories],
            "files": [a.to_dict() for a in self.files],
        }
