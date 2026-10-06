# wrist-quota — 履歴

決定・変更・検証結果を新しい順に記録する。

## 2026-10-07

### M0 調査（Claude Code）

- 依頼者から受け取った仕様書 v0.1 を `SPEC.md` として配置した。仮称 `quota-ping` は `wrist-quota` に、トピック名の接頭辞の例は `qp-` から `wq-` に置き換えた。それ以外の本文は受け取ったまま。
- Claude Code v2.1.291 で、ステータスラインの入力に `rate_limits` が渡ることを確認した。最初の呼び出しではキーが無く、約 0.4 秒後の呼び出しから入る。
- Codex のセッションログ 5 ファイル・438 件の `token_count` を確認した。4 件は `primary`・`secondary` が `null`（`limit_id` が `premium`）で、ファイルの最後に来る例もあった。
- ntfy の要検証項目を公式ドキュメントで確認した。予約は最短 10 秒・最長 3 日、sequence ID で取り消し・更新が可能、ntfy.sh の上限は 1 日 250 通。
- 実機サンプルを `testdata/` に置いた（セッション ID とパスは置換済み）。
- ntfy.sh への実送信は未実施。
- 詳細と仕様との差分は [M0-FINDINGS.md](M0-FINDINGS.md)。

### 開発準備（Claude Code）

- `kyoujyu88/wrist-quota` を `~/dev/wrist-quota` に clone した（`main` @ `98fd592`、内容は `README.md` のみ）。
- 開発マシンの toolchain を確認した。Go / Node.js は未導入、`gh` は `kyoujyu88` でログイン済み。詳細は [ENVIRONMENT.md](ENVIRONMENT.md)。
- `docs/ENVIRONMENT.md`・`docs/PROGRESS.md`・`docs/HISTORY.md` を作成した。

### リポジトリ名の確定

- 仮称 `quota-ping` に代えて、リポジトリ名を `wrist-quota` とした（ユーザーがリポジトリを作成）。
- 似た名前の既存ツール limitping（CCLimitPing）との混同を避けられる名前になっている。

### 方針の決定と仕様書 v0.1 の作成（claude.ai チャット）

- 表示は通知方式にする。Wear OS と watchOS の両方に 1 つの実装で届き、ウォッチ用ネイティブアプリが不要なため。
- 通知経路は ntfy。Android と iOS の両方にアプリがあり、送信は HTTP POST だけで済むため。
- 集計役は利用者の PC の常駐アプリにする。一般の利用者はサーバーを持たず、スマホ単体ではローカルログが読めないため。
- データは各ツールが PC 上に公式に書き出す情報だけを使う（AI の提案。ユーザーの明示的な承認は未取得）。
- 設定画面はブラウザで開くローカル Web UI（127.0.0.1）。
- 実装言語は Go の単一実行ファイルを推奨（AI の提案。未確定）。
- きっかけ: USB 小型ディスプレイに AI の残トークンを表示する記事 https://x.com/nuits_jp/status/2106686059509883050
