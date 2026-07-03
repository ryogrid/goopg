"""Phase 8: merge agent code-audit verdicts into unimplemented_feat.json.

Verdicts arrive per batch in agent_verdict_NN.json; items in agent_batch_NN.json
are in the same order. Keys (short commit shas) can repeat within a batch, so
match by key + order of occurrence.
"""
import json
from collections import Counter, defaultdict

from common import HERE, REPO

doc = json.loads((REPO / "unimplemented_feat.json").read_text())
feats = doc["unimplemented_features"]

# map feature -> verdict via batch/verdict file pairing
verdict_by_feature = {}
batches = sorted(HERE.glob("agent_batch_*.json"))
audited_batches = 0
for bf in batches:
    nn = bf.stem.split("_")[-1]
    vf = HERE / f"agent_verdict_{nn}.json"
    if not vf.exists():
        continue
    items = json.loads(bf.read_text())
    verdicts = json.loads(vf.read_text())["verdicts"]
    audited_batches += 1
    # order-of-occurrence matching per key
    by_key = defaultdict(list)
    for v in verdicts:
        by_key[v["key"]].append(v)
    seen = Counter()
    for it in items:
        k = it["key"]
        idx = seen[k]
        seen[k] += 1
        vlist = by_key.get(k, [])
        v = vlist[idx] if idx < len(vlist) else (vlist[-1] if vlist else None)
        if v:
            verdict_by_feature[it["feature"]] = v

kept, dropped = [], []
tally = Counter()
for f in feats:
    rc = f["resolution_check"]
    if rc["later_commits"].startswith("confirmed-open"):
        tally["already-confirmed-open"] += 1
        kept.append(f)
        continue
    v = verdict_by_feature.get(f["feature"])
    if v is None:
        tally["not-audited"] += 1
        f["code_audit"] = "not-audited"
        kept.append(f)
        continue
    tally[v["verdict"]] += 1
    if v["verdict"] == "implemented":
        f["resolution_check"]["later_commits"] = "implemented-code-audit: " + v["evidence"][:160]
        dropped.append(f)
    elif v["verdict"] == "open":
        f["code_audit"] = "confirmed-open: " + v["evidence"][:160]
        f["confidence"] = "high"
        kept.append(f)
    else:
        f["code_audit"] = "unclear: " + v["evidence"][:160]
        f["confidence"] = "medium"
        kept.append(f)

print(f"audited batches: {audited_batches}/{len(batches)}")
print("tally:", dict(tally))

prior = json.loads((HERE / "resolved_dropped.json").read_text())
(HERE / "resolved_dropped.json").write_text(
    json.dumps(prior + dropped, ensure_ascii=False, indent=1))

kept.sort(key=lambda x: (x["confidence"] != "high", x["deferred_date"]))
doc["unimplemented_features"] = kept
doc["counts"]["dropped_as_resolved"] += len(dropped)
doc["counts"]["unimplemented"] = len(kept)
if "code-audit" not in doc["method"]:
    doc["method"] += " + per-item agent code audit (haiku, grep+read)"
(REPO / "unimplemented_feat.json").write_text(json.dumps(doc, ensure_ascii=False, indent=1))
print(f"FINAL: {len(kept)} items (dropped {len(dropped)} via code audit)")
