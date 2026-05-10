#!/bin/bash
# Generate analysis report from pgbench comparison results

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
RESULTS_DIR="$REPO_ROOT/bench/pgbench-compare/results"
ANALYSIS_DIR="$REPO_ROOT/analysis"

TIMESTAMP=${1:-$(ls -t "$RESULTS_DIR" | grep -o '^[0-9]\{8\}_[0-9]\{6\}' | head -1)}

if [ -z "$TIMESTAMP" ]; then
    echo "ERROR: No results found. Please run the comparison first." >&2
    echo "Usage: $0 [TIMESTAMP]" >&2
    exit 1
fi

echo "Generating report for timestamp: $TIMESTAMP"

mkdir -p "$ANALYSIS_DIR"
REPORT_FILE="$ANALYSIS_DIR/pgbench_comparison_${TIMESTAMP}.md"

# Function to extract metrics from pgbench output
extract_metrics() {
    local file=$1

    if [ ! -f "$file" ]; then
        echo "N/A"
        return
    fi

    # Extract TPS (excluding connections)
    local tps=$(grep "^tps = " "$file" | awk '{print $3}')

    # Extract latency average
    local lat_avg=$(grep "^latency average" "$file" | awk '{print $4}')

    # Extract latency stddev
    local lat_stddev=$(grep "^latency stddev" "$file" | awk '{print $4}')

    # Extract initial connection time
    local conn_time=$(grep "^initial connection time" "$file" | awk '{print $5}')

    echo "${tps}|${lat_avg}|${lat_stddev}|${conn_time}"
}

# Function to format comparison table
format_comparison() {
    local workload=$1
    local goopg_file="$RESULTS_DIR/${TIMESTAMP}_goopg_${workload}.txt"
    local postgres_file="$RESULTS_DIR/${TIMESTAMP}_postgres_${workload}.txt"

    local goopg_metrics=$(extract_metrics "$goopg_file")
    local postgres_metrics=$(extract_metrics "$postgres_file")

    local goopg_tps=$(echo "$goopg_metrics" | cut -d'|' -f1)
    local goopg_lat_avg=$(echo "$goopg_metrics" | cut -d'|' -f2)
    local goopg_lat_stddev=$(echo "$goopg_metrics" | cut -d'|' -f3)
    local goopg_conn=$(echo "$goopg_metrics" | cut -d'|' -f4)

    local pg_tps=$(echo "$postgres_metrics" | cut -d'|' -f1)
    local pg_lat_avg=$(echo "$postgres_metrics" | cut -d'|' -f2)
    local pg_lat_stddev=$(echo "$postgres_metrics" | cut -d'|' -f3)
    local pg_conn=$(echo "$postgres_metrics" | cut -d'|' -f4)

    # Calculate ratio
    local ratio="N/A"
    if [ -n "$goopg_tps" ] && [ -n "$pg_tps" ] && [ "$pg_tps" != "0" ]; then
        ratio=$(echo "scale=2; $goopg_tps / $pg_tps * 100" | bc)"%"
    fi

    cat <<EOF
| Metric | goopg | PostgreSQL | goopg/PostgreSQL |
|--------|-------|------------|------------------|
| TPS (excl. connections) | ${goopg_tps:-N/A} | ${pg_tps:-N/A} | ${ratio} |
| Latency Average (ms) | ${goopg_lat_avg:-N/A} | ${pg_lat_avg:-N/A} | - |
| Latency Stddev (ms) | ${goopg_lat_stddev:-N/A} | ${pg_lat_stddev:-N/A} | - |
| Initial Connection Time (ms) | ${goopg_conn:-N/A} | ${pg_conn:-N/A} | - |

EOF
}

# Generate report
cat > "$REPORT_FILE" <<EOF
# pgbench Performance Comparison: goopg vs PostgreSQL

**Date**: $(date -d @${TIMESTAMP:0:8} +%Y-%m-%d) ${TIMESTAMP:9:2}:${TIMESTAMP:11:2}:${TIMESTAMP:13:2}

## Executive Summary

This report presents a performance comparison between goopg (a from-scratch Go implementation of PostgreSQL) and the official PostgreSQL 18.3 server using pgbench as the benchmark tool.

## Test Configuration

### System Configuration
- **goopg Port**: 5433
- **PostgreSQL Port**: 5434
- **Data Directory**: Separate directories for each system

### Database Configuration
Both systems were configured with identical parameters to ensure fair comparison:

- **shared_buffers**: 2.5 GB (2560 MB)
- **wal_buffers**: 100 MB
- **checkpoint_timeout**: 24 hours
- **max_wal_size**: 1 TB (1024 GB)

These settings were chosen to prevent checkpoints from occurring during the benchmark runs, eliminating checkpoint overhead as a confounding variable.

### pgbench Configuration
- **Scale Factor**: 100 (approximately 1.5 GB database)
- **Clients**: 100
- **Threads**: 100
- **Duration**: 180 seconds (3 minutes) per workload
- **Progress Reporting**: Every 10 seconds

### Workloads Tested
1. **Standard (TPC-B like)**: Mixed read/write workload with SELECT, UPDATE, and INSERT
2. **Simple Update**: UPDATE-only workload (\`-N\` flag)
3. **Select Only**: Read-only SELECT workload (\`-S\` flag)

### Test Methodology
- Tests were run alternating between goopg and PostgreSQL for each workload
- Both servers remained running throughout all tests
- Measurement order: Standard→Simple Update→Select Only for each system

## Results

### 1. Standard (TPC-B like) Workload

$(format_comparison "standard")

### 2. Simple Update Workload

$(format_comparison "simple-update")

### 3. Select Only Workload

$(format_comparison "select-only")

## Analysis

### Performance Overview

EOF

# Add analysis based on actual results
for workload in standard simple-update select-only; do
    goopg_file="$RESULTS_DIR/${TIMESTAMP}_goopg_${workload}.txt"
    postgres_file="$RESULTS_DIR/${TIMESTAMP}_postgres_${workload}.txt"

    if [ -f "$goopg_file" ] && [ -f "$postgres_file" ]; then
        goopg_tps=$(grep "^tps = " "$goopg_file" | awk '{print $3}')
        pg_tps=$(grep "^tps = " "$postgres_file" | awk '{print $3}')

        if [ -n "$goopg_tps" ] && [ -n "$pg_tps" ]; then
            ratio=$(echo "scale=1; $goopg_tps / $pg_tps * 100" | bc)

            case $workload in
                standard)
                    workload_name="Standard (TPC-B like)"
                    ;;
                simple-update)
                    workload_name="Simple Update"
                    ;;
                select-only)
                    workload_name="Select Only"
                    ;;
            esac

            cat >> "$REPORT_FILE" <<EOF
- **${workload_name}**: goopg achieved ${ratio}% of PostgreSQL's throughput (${goopg_tps} vs ${pg_tps} TPS)
EOF
        fi
    fi
done

cat >> "$REPORT_FILE" <<'EOF'

### Key Findings

1. **Implementation Maturity**: As a from-scratch implementation, goopg's performance relative to PostgreSQL varies across workload types, reflecting different maturity levels in executor, storage, and concurrency subsystems.

2. **Workload Sensitivity**: Performance ratios differ across workload types:
   - **Select-only**: Tests read path optimization and buffer management
   - **Simple Update**: Tests write path and WAL efficiency
   - **Standard (TPC-B)**: Tests overall system balance with mixed operations

3. **Scalability**: With 100 clients and 100 threads, this test stresses concurrent execution paths and lock contention handling in both systems.

### Observed Differences

The performance gap between goopg and PostgreSQL can be attributed to several factors:

1. **Optimization History**: PostgreSQL has decades of optimization across multiple hardware generations, workload patterns, and use cases.

2. **Executor Efficiency**: Query execution in goopg may have different code paths and optimizations compared to PostgreSQL's highly-tuned C implementation.

3. **Buffer Management**: Cache hit rates, eviction policies, and pin/unpin overhead may differ between the implementations.

4. **Lock Granularity**: Concurrency control mechanisms and lock contention handling impact high-client-count scenarios differently.

5. **Memory Allocation**: Go's garbage collector vs C's manual memory management creates different allocation patterns and overhead profiles.

## Detailed Results

### Raw pgbench Output

Full pgbench output for all tests is available in the results directory:

EOF

for workload in standard simple-update select-only; do
    for system in goopg postgres; do
        file="${TIMESTAMP}_${system}_${workload}.txt"
        if [ -f "$RESULTS_DIR/$file" ]; then
            echo "- \`bench/pgbench-compare/results/$file\`" >> "$REPORT_FILE"
        fi
    done
done

cat >> "$REPORT_FILE" <<EOF

## Conclusions

This benchmark provides a snapshot of goopg's performance relative to PostgreSQL 18.3 under controlled pgbench workloads. The results demonstrate:

1. **Functional Completeness**: goopg successfully handles all three standard pgbench workload types with 100 concurrent clients.

2. **Performance Baseline**: Establishes quantitative performance metrics for tracking optimization progress over time.

3. **Optimization Opportunities**: Performance gaps highlight specific subsystems (executor, buffer manager, WAL) where targeted optimization efforts can yield the highest returns.

## Future Work

To further improve goopg's performance:

1. **Profile-Guided Optimization**: Use pprof to identify CPU and memory hotspots during pgbench runs.
2. **Buffer Manager Tuning**: Analyze buffer hit rates and eviction patterns.
3. **Executor Optimization**: Review executor node implementations for unnecessary allocations and inefficient algorithms.
4. **Concurrency Primitives**: Evaluate lock contention patterns under high client counts.
5. **Benchmark Expansion**: Test with additional workloads (TPC-C, TPC-H) to identify workload-specific optimization opportunities.

## Reproducibility

To reproduce these results:

\`\`\`bash
# Run the full comparison
make pgbench-compare

# Or run manually
./bench/pgbench-compare/run_comparison.sh
\`\`\`

The script automatically:
- Initializes separate data directories for goopg and PostgreSQL
- Configures identical database parameters
- Runs all three workloads alternating between systems
- Generates result files with timestamps

---

*Generated by: \`bench/pgbench-compare/generate_report.sh\`*
*Timestamp: $TIMESTAMP*
EOF

echo ""
echo "Report generated: $REPORT_FILE"
echo ""
cat "$REPORT_FILE"
