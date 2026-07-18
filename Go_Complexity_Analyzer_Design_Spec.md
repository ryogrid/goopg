# Go Codebase quolity Analyzer - Design Specification

## 1. Overview

**Go Source Quolity Analyzer** is a Python-based static analysis tool that
measures and reports cyclomatic complexity etc for Go projects. It is
intended for large codebases such as RDBMS servers and focuses on
production code while excluding tests and auxiliary utilities.

## 2. Goals

-   Measure cyclomatic complexity etc using command line tools such as `gocyclo`.
-   Exclude test files (`*_test.go`) and configurable directories.
-   Produce project, package, directory, file, and function level
    metrics.
-   Generate machine-readable and human-readable reports.

## 3. Architecture

    Filesystem
        │
        ▼
    File Collector
        │
        ▼
    gocyclo Runner
        │
        ▼
    Result Parser
        │
        ├── Statistics Engine
        ├── Package Analyzer
        ├── Directory Analyzer
        ├── File Analyzer
        └── Function Analyzer
                │
                ▼
    Report Generation

## 4. Functional Requirements

### File Collection

Include: - `*.go`

Exclude by default: - `*_test.go` - `vendor/` - `third_party/` -
`testutil/` - `testport/` - `tmp/` - `tools/` - `examples/`

Additional exclusions shall be configurable.

### Complexity Analysis

Invoke `gocyclo` on the collected files.

Collect: - Cyclomatic complexity - Function name - Receiver (if
applicable) - Source file - Line number - Package - Directory

### Statistics

Compute: - Number of Go files - Number of packages - Number of
functions - Mean - Median - Maximum - P90/P95/P99 - Counts above
configurable thresholds

### Reports

Generate: - Console summary - Package ranking - Directory summary - File
summary - Top-N functions - CSV - JSON - HTML - Histogram (SVG)

## 5. Output Files

    report/
        report.html
        summary.json
        functions.csv
        packages.csv
        files.csv
        histogram.svg

## 6. Configuration

Example:

``` yaml
exclude_dirs:
  - vendor
  - third_party
  - testutil
  - testport
  - tmp

exclude_patterns:
  - "*_test.go"

top_functions: 100
top_packages: 20
top_files: 50
```

## 7. Non-functional Requirements

-   Python 3.x.x
-   Cross-platform
-   Configurable
-   Deterministic output

## 8. Other Features

-   Cognitive Complexity
-   Maintainability Index
-   Halstead Metrics
-   Fan-in / Fan-out
