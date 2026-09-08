#!/usr/bin/env python3
"""EX0-04 slice classifier: duration-weighted -traces attribution.
Leaf-ward first-match over ordered patterns = the stack-based extraction
the design mandates (single edges cannot separate compareDatum users).
Usage: slice.py cpu.pb.gz [--other-top]
Prints shares + excluded total + null-control line.
"""
import re, subprocess, sys

PREFILTER = ("evalPrefilter",)
FILTEROP = ("filterOp",)
EXPRLEAF = ("evalExprSlot", "evalFastExpr", "evalBinary", "compareDatum",
            "evalTypedStringLit", "parseNumericFastScale", "addTimeInterval")
DECODE = ("decodeRowRange", "decodePhysical", "NumericInt64",
          "pageChecksum", "ParseHeapTuple", "PageGetHeapTuple",
          "decodeScanRow", "Detoast", "detoast")
CLONE = ("cloneRowOwned", "MaterializeArena", "MaterializeForTransfer",
         "acquireRow")
PROBE = ("evalHashKey", "joinPredicateMatch", "buildLoop", "probeLoop",
         "hashBatch", "parallelBuild")
SORTCMP = ("evalSortKeyValue", "lessKeyVals", "lessRows", "sortHeap",
           "tuplesort", "flushChunk", "mergeSort")
SPILL = ("spillWriter", "Spill", "spillFile", "logtape")
EXCLUDED = ("futex", "usleep", "Syscall", "poll", "nanosleep", "select",
            "mutex", "semacquire", "chanrecv", "chansend", "gopark")
# Nested-loop / index-probe path (Q4-class): inner index descent per outer
# row. Unambiguous frames + compare machinery that counts as probe ONLY
# under an NL/index/join context (else it is sort/filter compare).
NL = ("NestLoop", "nestloop", "IndexScan", "indexNext",
      "comparePGIndexTuples", "parsePosting", "btree", "BTree", "nbtree")
NLCMP = ("PGCompareNumeric", "cmpNumeric", "decodeNumericParts")


def classify(stack):
    leaf = stack[0]
    if any(p in leaf for p in EXCLUDED):
        return "excluded"
    # nearest slice-defining frame walking up from the leaf
    for i, f in enumerate(stack):
        above = stack[i:]
        ctx = lambda pats: any(any(p in g for p in pats) for g in above)
        if any(p in f for p in SPILL):
            return "spill-write"
        if any(p in f for p in SORTCMP):
            return "sort-compare"
        if any(p in f for p in PROBE):
            return "join-probe"
        if any(p in f for p in NL):
            return "join-probe"
        if any(p in f for p in NLCMP) and any(
                any(p in g for p in NL + PROBE + FILTEROP) for g in above):
            return "join-probe"
        if any(p in f for p in EXPRLEAF + PREFILTER):
            if ctx(PREFILTER):
                return "prefilter"
            if ctx(FILTEROP):
                return "filterOp-residual"
            if ctx(PROBE):
                return "join-probe"
            if ctx(SORTCMP):
                return "sort-compare"
            return "filterOp-residual"
        if any(p in f for p in DECODE):
            return "scan-decode"
        if any(p in f for p in CLONE):
            return "clone"
    return "other"


def main(pb):
    out = subprocess.run(["go", "tool", "pprof", "-traces", pb],
                         capture_output=True, text=True).stdout
    tot = {"prefilter": 0.0, "filterOp-residual": 0.0, "scan-decode": 0.0,
           "clone": 0.0, "join-probe": 0.0, "sort-compare": 0.0,
           "spill-write": 0.0, "excluded": 0.0, "other": 0.0}
    cur_dur, cur_stack = 0.0, []
    def flush():
        if cur_stack:
            tot[classify(cur_stack)] += cur_dur
    for line in out.splitlines():
        m = re.match(r"\s*(\d+(?:\.\d+)?)ms\s+(\S+)", line)
        if m:
            flush()
            cur_dur, cur_stack = float(m.group(1)), [m.group(2)]
        elif re.match(r"\s+\S", line) and cur_stack is not None and line.strip():
            cur_stack.append(line.strip().split()[0])
        elif line.startswith("-----------+"):
            flush()
            cur_stack = []
    flush()
    total = sum(tot.values())
    for k in ("prefilter", "filterOp-residual", "scan-decode", "clone",
              "join-probe", "sort-compare", "spill-write", "excluded", "other"):
        print(f"{k:16s} {tot[k]/total*100:6.2f}%  ({tot[k]/1000:.2f}s)")
    print(f"TOTAL {total/1000:.2f}s")
    pre, fre = tot["prefilter"], tot["filterOp-residual"]
    print(f"RESIDUAL-RATIO {fre/(pre+fre)*100:.2f}%  "
          f"(filterOp {fre/1000:.2f}s / expr-edge {(pre+fre)/1000:.2f}s)")
    print(f"NULL-CONTROL join={tot['join-probe']/1000:.2f}s "
          f"sort={tot['sort-compare']/1000:.2f}s spill={tot['spill-write']/1000:.2f}s")
    if "--other-top" in sys.argv:
        from collections import Counter
        c = Counter()
        out2 = subprocess.run(["go", "tool", "pprof", "-traces", sys.argv[1]],
                              capture_output=True, text=True).stdout
        cur_dur, cur_stack = 0.0, None
        def flush2():
            if cur_stack and classify(cur_stack) == "other":
                c[cur_stack[0].split(".")[-1][:60]] += cur_dur
        for line in out2.splitlines():
            m = re.match(r"\s*(\d+(?:\.\d+)?)ms\s+(\S+)", line)
            if m:
                flush2()
                cur_dur, cur_stack = float(m.group(1)), [m.group(2)]
            elif re.match(r"\s+\S", line) and cur_stack is not None and line.strip():
                cur_stack.append(line.strip().split()[0])
            elif line.startswith("-----------+"):
                flush2()
                cur_stack = []
        flush2()
        print("OTHER-TOP (leaf frames):")
        for k, v in c.most_common(15):
            print(f"  {k:55s} {v/1000:.2f}s")


if __name__ == "__main__":
    main(sys.argv[1])
