# wrist-quota — M0 調査結果

> 実施: 2026-10-07（JST）、開発マシン（Ubuntu 24.04.5 LTS）
> 対象: [SPEC.md](../SPEC.md) 13 章 M0 の 3 項目

## 結論

- Claude Code・Codex ともに、仕様どおりの場所から利用枠の数値を取得できた。
- ntfy の「要検証」4 項目は公式ドキュメントで確認できた。予約済みメッセージは取り消し・更新ができる。
- 仕様との差分は 4 点（末尾の「仕様との差分」）。いずれも方針の変更ではなく、実装方法の具体化。
- ntfy.sh への実送信はまだ行っていない（外部への送信になるため、依頼者の了承後に実施）。

## 1. Claude Code（v2.1.291、Linux）

確認方法: 一時的な `--settings` でステータスラインにダンプ用コマンドを指定した対話セッションを起動し、短い発話を 1 回送った。`~/.claude/settings.json` は変更していない（`statusLine` は未設定のまま）。

| 呼び出し | タイミング | `rate_limits` |
|---|---|---|
| 1 回目 | 発話の送信直後 | キー自体が無い |
| 2 回目 | 1 回目の約 0.4 秒後（応答の完了前） | あり |
| 3 回目 | 最初の応答の完了後 | あり |

```json
"rate_limits": {
  "five_hour": { "used_percentage": 26, "resets_at": 1791339000 },
  "seven_day": { "used_percentage": 11, "resets_at": 1791878400 }
}
```

- フィールド名と構造は仕様 6.1 と一致。
- `used_percentage` は整数で渡ってきた（仕様の例は小数）。`resets_at` は Unix 秒。
- 欠落時は `null` ではなくキーが存在しない。
- セッションを起動しただけ（発話前）ではステータスラインのコマンドは呼ばれなかった。
- ステータスラインの入力には `session_id`・`cwd`・`transcript_path`・`workspace` など利用枠以外の情報も含まれる。保存するのは `rate_limits` だけにする。

サンプル: `testdata/claudecode/`（3 回分。セッション ID とパスは置換済み）

## 2. Codex

確認方法: `~/.codex/sessions/` 配下の 5 ファイルから `token_count` イベントだけを抽出した（計 438 件）。会話本文の行は読み飛ばしている。

```json
"rate_limits": {
  "limit_id": "codex",
  "limit_name": null,
  "primary":   { "used_percent": 23.0, "window_minutes": 300,   "resets_at": 1790860996 },
  "secondary": { "used_percent": 21.0, "window_minutes": 10080, "resets_at": 1791407845 },
  "credits": { "has_credits": false, "unlimited": false, "balance": "0" },
  "individual_limit": null,
  "spend_control_reached": null,
  "plan_type": "plus",
  "rate_limit_reached_type": null
}
```

- 場所・対象行・`primary`／`secondary` の構造は仕様 6.2 と一致。
- 仕様に無いフィールド（`limit_id`・`plan_type`・`credits` など）がある。
- 438 件中 4 件は `limit_id` が `premium` で、`primary`・`secondary` がともに `null` だった。このイベントは `limit_id` が `codex` の通常イベントの間に単発で挟まり、**ファイルの最後のイベントになっている例もあった**。
- 観測した範囲では `primary` が 300 分、`secondary` が 10080 分で固定だった（位置が入れ替わる例は見つからなかった）。
- ファイルは最大 15 MB・2,675 行あった。末尾側から読む方針は必要。
- イベントの最上位には `timestamp`（ISO 8601）がある。`observed_at` に使える。

サンプル: `testdata/codex/token_count_events.jsonl`（通常 2 件、`null` 1 件）

## 3. ntfy

確認方法: 公式ドキュメントの原文（[publish.md](https://github.com/binwiederhier/ntfy/blob/main/docs/publish.md)、公開版は https://docs.ntfy.sh/publish/ ）を読んだ。実送信は未実施。

| 要検証項目 | 結果 |
|---|---|
| 予約配信の範囲 | 最短 10 秒、最長 3 日。サーバー側の `message-delay-limit` で変更可能 |
| 予約の取り消し・更新 | 可能。sequence ID を付けて予約し、`DELETE /<topic>/<sequence_id>` で取り消し、同じ sequence ID で再送すると予約が置き換わる |
| JSON 送信のフィールド名 | `topic`（必須）・`title`・`message`・`priority`・`tags`・`delay`・`sequence_id` ほか。サーバーのルート URL へ POST |
| ntfy.sh の送信回数制限 | 1 日 250 通。リクエストは 60 回まで連続可、以後 5 秒に 1 回分ずつ回復 |

補足:

- `delay` は文字列。Unix 秒・期間（`30m`）・自然言語（`tomorrow, 3pm`）を受け付ける。
- `priority` は 1〜5 の整数、`tags` は文字列の配列。
- メッセージ本文は 4,096 バイトまで。タイトルは 1 KB、タグは合計 512 バイトまで（超えると HTTP 400）。
- 予約メッセージは配信の 12 時間後までサーバーのキャッシュに残る。
- ヘッダーの UTF-8 は ntfy 側では対応しているが、HTTP ライブラリによっては化ける。JSON 送信ならこの問題を避けられる。
- 配信済みの通知の更新・削除は、ドキュメント上 Android と Firefox のみ対応とされている。予約の取り消しはサーバー側の処理なので受信端末に依存しない。
- 制限を繰り返し超えると IP が一時的に遮断されることがある。

## 仕様との差分

1. **Codex の `null` イベントの扱い（6.2）** — 仕様は「`null` の場合は未取得として扱う」。実機では `null` のイベントが通常イベントの間に挟まり、ファイルの最後に来ることもある。最新のイベントが `null` なら読み飛ばして、値のある直近のイベントまで遡る必要がある。「未取得」とするのは値のあるイベントが 1 件も無い時だけにする。
2. **リセット通知の予約（7.4）** — 取り消し・更新ができると確認できた。予約には窓ごとに決まる sequence ID を付け、`resets_at` が変わったら同じ ID で予約し直す。「できない場合の対処」は不要になる。
3. **JSON 送信のフィールド（8 章）** — 仕様の 6 フィールドに `sequence_id` を加える。
4. **送信回数の上限（8 章）** — ntfy.sh は 1 日 250 通。しきい値 3 段階 × 窓 4 つ ＋ リセット通知では到底届かない量だが、再試行と定時まとめを含めて上限を超えない設計にする。

## 未確認

- ntfy.sh への実送信（日本語の表示、予約配信、sequence ID による取り消しの実動作、ウォッチでの見え方）。
- Windows・macOS での Claude Code／Codex の挙動。
- Claude Code の `resets_at` が ISO 8601 文字列になる条件（今回は Unix 秒のみ観測）。
- Codex の `limit_id` が `premium` のイベントが何を表すか。
