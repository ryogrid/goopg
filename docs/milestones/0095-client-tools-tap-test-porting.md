# Milestone 0095 — Client-Tools TAP Test Porting

**Status:** in-progress
**Depends on:** M0060 (oracle test-port foundation), M0094 (replication E2E/TAP base)
**Reference plan:** `.ralph/fix_plan.md` (M0095 section)

## 運用追記 (2026-05-12)

- blocker の存在や goopg 未対応で途中までしか進められない項目は、blocker 解消までを本マイルストーンの実施範囲に含める。
- blocker 解消により先に進める項目は、解消実装と再検証が完了するまで完了扱いにしない。
- goopg の Go 言語実装制約または設計上の制約で解消不可能な項目のみ、理由明記のうえ完了扱いを維持する。

## Note

- この文書は M0095 の運用方針アンカーとして作成した。
- サブマイルストーン詳細と進捗管理は `.ralph/fix_plan.md` の M0095 節を正とする。
- D-005l (`200_connstr` の LATIN1 経路) は goopg の UTF8-only 設計前提と両立しないため、設計前提を変更しない限り解消不能として扱う。
