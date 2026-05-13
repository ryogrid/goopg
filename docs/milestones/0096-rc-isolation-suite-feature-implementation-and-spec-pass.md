# Milestone 0096 — RC Isolation-Test Suite: Feature Implementation & Spec Pass

**Status:** in-progress
**Depends on:** M0060 (oracle test-port foundation), M0095 (client-tools unblock follow-through)
**Reference plan:** `.ralph/fix_plan.md` (M0096 section)

## 運用追記 (2026-05-12)

- blocker の存在や goopg 未対応で途中までしか進められない項目は、blocker 解消までを本マイルストーンの実施範囲に含める。
- blocker 解消により先に進める項目は、解消実装と再検証が完了するまで完了扱いにしない。
- goopg の Go 言語実装制約または設計上の制約で解消不可能な項目のみ、理由明記のうえ完了扱いを維持する。

## Note

- この文書は M0096 の運用方針アンカーとして作成した。
- サブマイルストーン詳細と進捗管理は `.ralph/fix_plan.md` の M0096 節を正とする。
- 未解消 blocker（例: ON CONFLICT 待機表示整合、RAISE NOTICE 出力整合、RR/Serializable スナップショット整合）は解消まで完了扱いにしない。
