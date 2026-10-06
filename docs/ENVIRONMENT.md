# wrist-quota — 環境情報

> 対象範囲: AI ツール（Claude Code / Codex）の利用枠残量を PC で集計し、ntfy 経由でスマートフォン・スマートウォッチへ通知する常駐ツール。
> 最終確認: 2026-10-07（JST）

## 技術スタック

実装言語は未確定（Go の単一実行ファイルが候補。ユーザーの承認は未取得）。確定したらこの表を更新する。

| レイヤー | 技術 | 状態 |
|---|---|---|
| 集計・常駐アプリ | 未確定（候補: Go） | 未着手 |
| 設定画面 | ローカル Web UI（127.0.0.1） | 未着手 |
| 通知経路 | ntfy（既定は公開サーバー、自前サーバーも指定可） | ドキュメント確認済み・実送信は未実施 |
| 対応 OS | Windows / macOS / Linux | — |

## 開発マシン

| 項目 | 値 |
|---|---|
| OS | Ubuntu 24.04.5 LTS（kernel 6.8.0-146-generic, x86_64） |
| 作業ディレクトリ | `~/dev/wrist-quota` |

## インストール済みツール

| ツール | バージョン |
|---|---|
| git | 2.43.0 |
| gh | 2.45.0（`kyoujyu88` でログイン済み） |
| Python | 3.12.3 |
| Docker | 29.8.2 |
| Claude Code CLI | 2.1.291（`~/.local/bin/claude`） |
| tmux | 導入済み（M0 のサンプル採取に使用） |

## 未導入（必要になった時点で導入）

- Go / Node.js（npm, pnpm）/ uv / cargo — いずれも PATH 上に無い。実装言語の確定後に導入する。
- ntfy CLI — 未導入。送信は HTTP POST だけで足りるため必須ではない。
- Codex CLI — PATH 上に無い。ただし `~/.codex/sessions/2026/` にセッションログは存在する（M0 のサンプル採取に利用可）。

## データソース（集計対象）

各ツールが PC 上に公式に書き出す情報だけを使う。非公式の利用状況エンドポイントは使わない。

- Claude Code: ステータスライン用コマンドへ渡る JSON の `rate_limits`（`five_hour` / `seven_day` の `used_percentage`・`resets_at`）
- Codex: `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` の `token_count` イベントに含まれる `rate_limits`

どちらもこの開発マシンで取得できることを確認済み（2026-10-07、[M0-FINDINGS.md](M0-FINDINGS.md)）。Claude Code の `statusLine` は `~/.claude/settings.json` に未設定。

## リポジトリ

- Remote: https://github.com/kyoujyu88/wrist-quota
- 既定ブランチ: `main`
