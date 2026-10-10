# better-auth-multi-platform-api

## 概要
Next.js と Better Auth、Go で認証を実装する。この API は Go で、Web(別リポジトリ `better-auth-multi-platform-web`)が発行した JWT を検証する。将来は Tauri(デスクトップ)と Expo(モバイル)からも同じ Go を使う予定(今は Web のみ)。
- 認証は Next.js の Better Auth が担当する。Web は Next.js のサーバー(BFF)経由で Go を呼ぶ。Web 側のサーバーが呼ぶたびに JWT を発行して付ける(Web の `src/lib/api-server.ts` の `callApi`)。ブラウザには JWT を出さない
- Go は JWT を検証するだけで、呼び出し元を区別しない。だから Tauri / Expo は、Better Auth でログインして JWT を取り、Go を直接呼ぶ形で足せる(Go は変えずに済む)
- ログインはマジックリンクが必須、OAuth は任意。パスワードは使わない

## 使用技術
Go 1.27(標準の `net/http`。`go.mod` と Dockerfile のイメージに合わせる)/ Postgres(pgx。開発は `docker-compose.yml` の `db`)/ Docker(本番は Render)/ Air(開発のホットリロード)。Web とは JWT で連携(JWKS で署名を検証。Go はステートレス)

## 詳細ドキュメント(必要なときに読む)
- [docs/design.md](docs/design.md): 環境変数、JWT 検証、退会時のデータ削除(`internal/userdata`)、テスト
- [docs/deploy.md](docs/deploy.md): マイグレーションの手順(開発と本番)、デプロイ先
- 該当の機能を触るときは、先に読む。コードを変えたら、その節も直す

## 守るルール(特に壊しやすいもの)
- `user_id` はトークンの `sub` だけから取る(クエリやボディで他人を指せない)
- 公開鍵を取れないときは 401 ではなく 503
- `user_id` を持つテーブルを足すマイグレーションは、同じ変更で `internal/userdata` の `Tables` に扱い(delete / anonymize / retain)を宣言する
- `UPDATE` のたびに `updated_at = now()` を入れる(自動では更新されない)
- 本番は `APP_ENV=production` で、`DATABASE_URL` は `sslmode=require` 以上

## コマンド
- `air`: 開発サーバー(localhost:8080、保存で自動再起動)
- `docker compose up --build`: Docker で起動
- `go build -o bin/server ./cmd/server`: ビルド
- `gofmt -l .` / `go vet ./...` / `go test ./...`: 整形の確認 / 静的解析 / テスト(Claude Code では hooks が自動実行する)
- マイグレーション(goose)は人が手で実行する。手順は docs/deploy.md

## 設計方針
将来 Echo などへ切り替える可能性がある。次を守る。
- HTTP は標準の `net/http` のみ。外部のルーター/フレームワークを追加しない
- ロジックは `internal/` に置き、`net/http` に依存させない
- ハンドラは薄く保つ(リクエストを読む → ロジックを呼ぶ → レスポンスを返す)
- ミドルウェアは `func(http.Handler) http.Handler` の形で書く

## 実装後の流れ(生成と評価のループ)
実装は自分、評価は `evaluator` エージェント(`.claude/agents/evaluator.md`)。
1. 実装する。`gofmt -l .`・`go vet ./...`・`go build ./...`・`go test ./...` は hooks(`.claude/settings.json`)が自動で実行する(編集ごとに gofmt、応答終了時に全体)。失敗したら差し戻されるので直す
2. 挙動を変える変更のときは `evaluator` を呼ぶ(ドキュメントやコメントだけの変更では呼ばない)
3. 「要修正」なら「高」「中」の指摘を直して 2 に戻る。最大 5 周で打ち切る
4. 「合格」で終了し、結果を報告する。5 周で合格しなければ、止めて残りの指摘をそのまま報告する(無理に通さない)。同じ指摘が再発したら、上限を待たずに止める
5. 報告には、SKIP されたテスト(`TEST_DATABASE_URL` 未設定の DB テストなど)と未検証の項目、人間向けの確認手順を必ず書く。人間の返事は待たない

人間の確認(curl での動作確認、DB を使った手動テスト、最終レビュー)はループの外。人間が問題を見つけたら、その内容を新しい goal として回す。goal を書くときは、完了条件(機械で確認できるもの)と、ループの外(人間の作業)を分ける。

### テストを通すための改ざんは禁止
やむを得ず変えるときは、理由を報告に書き、人間の確認を待つ。
- 失敗するテストの削除、`t.Skip` の追加、`-run` や build タグで外すこと
- 期待値を実装の出力に合わせて弱めること(仕様が変わった場合を除く)
- 上限値、タイムアウト、許可リストなどを、通るように緩めること
- `evaluator` の指摘を、直さずにコメントや設定で黙らせること

テストが落ちたら、まず実装が間違っていると考える。テストの側が間違っていると判断するときも、根拠を書く。

## コミットと hooks
- 人が頼むまでコミットしない(未コミットで残し、先に読んでもらう)。push は hook が止めるので人が実行する。`.github/workflows/` の編集は hook では止めない。CI を緩める変更(テストの削除、SKIP を許す、など)は改ざんとして扱う
- `goose up` などのマイグレーションは実行しない(手順を案内するだけ)
- PreToolUse でブロック(`.claude/hooks/`): `.env` 系の読み取り(`.env.example` は可)、`git push/clean/reset --hard`、`rm -r`、`goose up/down/reset/redo`、既存 `migrations/` の編集、`go.sum` の編集、`_test.go` への `t.Skip` の追加
- ブロックされたら回避せず、理由を報告して人の指示を待つ

## 進捗ファイル
- `claude-progress.txt` に、完了・実行中・これからのタスクを書く。新しいセッションは、最初にこのファイルと CLAUDE.md を読む
- goal が終わるたびに、終了時の報告と一緒に更新する
- PR を作るとき(頼まれたとき)は、PR の前に書き直す: 完了を移し、「実行中」を現状に合わせ、「これから」の先頭を次の作業にする。同じ PR に入れる
