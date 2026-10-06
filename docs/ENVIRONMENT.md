# wrist-quota — 環境情報

> 対象範囲: AI ツール（Claude Code / Codex）の利用枠残量を PC で集計し、ntfy 経由でスマートフォン・スマートウォッチへ通知する常駐ツール。
> 最終確認: 2026-10-07（JST）

## 技術スタック

| レイヤー | 技術 | 備考 |
|---|---|---|
| 言語 | Go 1.27.1 | 単一実行ファイル。外部ライブラリは使っていない（標準ライブラリのみ） |
| 設定画面 | 素の HTML・CSS・JavaScript | ビルド工程なし。`embed` で実行ファイルに同梱 |
| 通知経路 | ntfy（既定 `https://ntfy.sh`） | JSON 送信。ドキュメント確認済み・実送信は未実施 |
| 保存 | JSON ファイル | 設定ディレクトリ配下。データベースなし |
| 対応 OS | Windows / macOS / Linux | Linux で動作確認。Windows・macOS はビルドのみ確認 |

## ビルドとテスト

```bash
go build ./cmd/wrist-quota
go test ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./cmd/wrist-quota   # クロスビルド
```

## 実行時のファイルとポート

| 項目 | 値 |
|---|---|
| 設定ディレクトリ | `os.UserConfigDir()/wrist-quota`（Linux: `~/.config/wrist-quota`）。`WRIST_QUOTA_HOME` で変更可 |
| `config.json` | 通知先・ルール・プロバイダの設定、ステータスライン登録の記録 |
| `state.json` | 最新の観測値、通知済みの記録、送信履歴 |
| `claudecode.json` | `statusline` サブコマンドが書く Claude Code の観測値 |
| `claude-settings.backup.json` | ステータスライン登録前の Claude Code 設定の退避 |
| 設定画面 | `127.0.0.1:47821` と `[::1]:47821`（`config.json` の `listen` で変更可。ループバック以外は拒否） |

ファイルは本人だけが読み書きできる権限（ディレクトリ 0700、ファイル 0600）で作る。

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
| Go | 1.27.1（`~/.local/go` にユーザー単位で導入。`~/.profile` で PATH に追加） |
| Docker | 29.8.2 |
| Claude Code CLI | 2.1.291（`~/.local/bin/claude`） |
| tmux | 導入済み（M0 のサンプル採取に使用） |

## 未導入（必要になった時点で導入）

- Node.js（npm, pnpm）/ uv / cargo — PATH 上に無い。このプロジェクトでは不要。
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
