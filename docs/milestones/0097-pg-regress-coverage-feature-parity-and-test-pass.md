# Milestone 0097 — pg_regress Coverage: Feature Parity & Test Pass

**Status:** in-progress
**Depends on:** M0060 (oracle test-port foundation), M0096 (RC isolation follow-through)
**Reference plan:** `.ralph/fix_plan.md` (M0097 section)

## 運用追記 (2026-05-12)

- blocker の存在や goopg 未対応で途中までしか進められない項目は、blocker 解消までを本マイルストーンの実施範囲に含める。
- blocker 解消により先に進める項目は、解消実装と再検証が完了するまで完了扱いにしない。
- goopg の Go 言語実装制約または設計上の制約で解消不可能な項目のみ、理由明記のうえ完了扱いを維持する。

## Note

- この文書は M0097 の運用方針アンカーとして作成した。
- サブマイルストーン詳細と進捗管理は `.ralph/fix_plan.md` の M0097 節を正とする。
- regress の `excluded` 判定は設計/スコープ方針によるものなので、該当項目は理由明記のうえ完了維持可能。
- `defer` は未達成を意味するため、port 化に必要な整合修正が残る項目は完了扱いにしない。
