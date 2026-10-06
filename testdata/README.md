# testdata

実機で採取したサンプル。収集部品の単体テストで固定データとして使う。
採取の経緯と読み取れたことは [docs/M0-FINDINGS.md](../docs/M0-FINDINGS.md) を参照。

## claudecode/

Claude Code v2.1.291（Linux）がステータスライン用コマンドの標準入力へ渡した JSON。同じセッションの連続した 3 回分。

| ファイル | 内容 |
|---|---|
| `statusline_before_first_request.json` | 発話の送信直後。`rate_limits` キーが無い |
| `statusline_after_request_start.json` | その約 0.4 秒後。`rate_limits` あり |
| `statusline_after_first_response.json` | 最初の応答の完了後。`rate_limits` あり |

`session_id`・`prompt_id`・`session_name`・`transcript_path` と、ホームディレクトリを含むパスは固定値に置き換えてある。それ以外は採取時のまま。

## codex/

Codex CLI のセッションログ（`rollout-*.jsonl`）から、`token_count` イベントの行だけを抜き出したもの。会話本文の行は含まない。

| 行 | 内容 |
|---|---|
| 1〜2 行目 | 通常のイベント（`limit_id` が `codex`、`primary`・`secondary` あり） |
| 3 行目 | `limit_id` が `premium` で `primary`・`secondary` が `null` のイベント |

行の内容は採取時のまま。
