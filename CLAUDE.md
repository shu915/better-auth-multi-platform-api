# better-auth-multi-platform-api

## 概要
Next.js と Better Auth、Go で認証を実装する。
この API は Go で、Web(別リポジトリ `better-auth-multi-platform-web`)が発行した JWT を検証する。
将来は Tauri(デスクトップ)と Expo(モバイル)からも、同じ Go のサービスを使う予定(おいおい実装する。今は Web のみ)。
認証は Next.js の Better Auth が担当する。Web は Next.js のサーバー(BFF)経由で Go を呼ぶ。
Web 側のサーバーが、呼ぶたびに JWT を発行して付ける(Web リポジトリの `src/lib/api-server.ts` の `callApi`)。ブラウザには JWT を出さず、フォームも Server Action 経由にする。
Go は JWT を検証するだけで、呼び出し元を区別しない。そのため、Tauri / Expo は、Better Auth でログインして JWT を取得し、Go を直接呼ぶ形で足せる(Go は変えずに済む)。
ログイン方法はマジックリンクを必須とし、OAuth は任意で有効にできる。パスワードは使わない。

## 使用技術
- Go 1.27(標準の `net/http`。`go.mod` と Dockerfile のイメージに合わせる)
- Postgres(接続は pgx。開発は `docker-compose.yml` の `db`)
- Docker(本番は Fargate などのコンテナ基盤を想定)
- Air(開発時のホットリロード)
- Web とは JWT で連携(JWKS で署名を検証。Go はステートレス)

## コマンド
- `air`: 開発サーバー(localhost:8080、保存で自動再起動)
- `docker compose up --build`: Docker で起動
- `go build -o bin/server ./cmd/server`: ビルド
- `gofmt -l .` / `go vet ./...` / `go test ./...`: 整形の確認 / 静的解析 / テスト(Claude Code では hooks が自動実行する)
- マイグレーション(人が手で実行する。Claude Code では hooks が `up` / `down` などをブロックする。goose を `go tool` で実行。SQL は `migrations/`。先に `docker compose up -d db`):
  - 接続先は環境変数で渡す(URL をコマンドライン引数に載せると、`ps` やシェル履歴に残るため)。
    `export GOOSE_DRIVER=postgres GOOSE_DBSTRING="postgres://postgres:postgres@localhost:5432/app?sslmode=disable"`(開発用のダミーの値)
  - `go tool goose -dir migrations create <name> sql -s`: 新しいマイグレーションの雛形を作る(`-s` で連番)
  - `go tool goose -dir migrations up`: 未適用分を適用する
  - `go tool goose -dir migrations status` / `down`: 状態の確認 / 1 つ戻す(開発だけ)
  - スキーマを最初から作り直すときは、`docker compose down -v` で DB のボリュームを消す
  - **本番**:
    - アプリの起動時ではなく、デプロイ手順の中で 1 回だけ実行する(複数タスクの同時起動で衝突させないため)
    - `GOOSE_DBSTRING` は、`sslmode=require` 以上の URL を渡す(goose にはアプリの TLS 検証がない)
    - `down` は使わない(`DROP TABLE` でデータが消える。前に進める方向だけ。戻したいときは、新しいマイグレーションを足す)
    - マイグレーション用(DDL)とアプリ実行用(SELECT / INSERT / UPDATE / DELETE のみ)の DB ユーザーは分けるのが望ましい(次のステップで検討)
  - `updated_at` は自動では更新されない。`UPDATE` のたびに `updated_at = now()` を入れる
  - `profiles.user_id` 自体に外部キーはない(ユーザーは Web の DB にある)。ユーザー削除との同期は、`DELETE /me` と下の「ユーザーのデータの削除(退会)」のとおり。他のテーブルの `user_id` は、`profiles(user_id)` への外部キーで扱いを決める

## 環境変数
- `APP_ENV`: `development`(未設定も同じ)か `production`。それ以外の値は起動時にエラー。`production` にすると、`AUTH_ISSUER`・`AUTH_AUDIENCE`・`CORS_ALLOWED_ORIGINS`・`DATABASE_URL` が必須になる(`CORS_ALLOWED_ORIGINS` はオリジンが 1 つ以上、`DATABASE_URL` は TLS 必須)(未設定なら起動時にエラー)
- `PORT`: 待ち受けポート(既定 8080)
- `AUTH_ISSUER`: JWT の `iss`。Web の `BETTER_AUTH_URL` と同じにする(既定 `http://localhost:3000`)
- `AUTH_AUDIENCE`: JWT の `aud`。Web の `JWT_AUDIENCE` と同じにする(既定 `better-auth-multi-platform-api`)
- `AUTH_JWKS_URL`: 公開鍵の取得先(既定 `<AUTH_ISSUER>/api/auth/jwks`)。`https` のみ可(localhost / host.docker.internal だけ `http` 可)
- `CORS_ALLOWED_ORIGINS`: ブラウザから Go を直接呼ぶ場合のオリジン。Web は BFF 経由なので、現状は使われない(設定は残してある)。カンマ区切り、完全一致(既定 `http://localhost:3000`)。本番(`APP_ENV=production`)では必須
- `DATABASE_URL`: Postgres の接続文字列(パスワードを含む)。既定は `docker-compose.yml` の開発用 DB(ダミーの認証情報、`localhost:5432`)。本番では必須で、外部から注入する。本番では `sslmode=require` 以上でないと起動時にエラー(`prefer` や sslmode 省略も平文に落ちるので不可)

## 認証(JWT 検証)
- `internal/auth`: JWT の検証本体(jwx v3)。EdDSA のみ許可し、`iss`・`aud`・`exp`・`sub` を検証する。JWKS はキャッシュし、未知の `kid` のときだけ(15 秒に 1 回まで)再取得する。同時に来たリクエストは 1 回の取得を待って結果を共有する(singleflight)
- `internal/middleware`: `Authorization: Bearer` を取り出して検証し、ユーザー ID を `context` に入れる(`middleware.UserID`)
- 公開鍵を取得できないときは 401 ではなく 503 を返す(トークンの問題ではないため)。`auth.ErrInvalidToken` だけが 401
- `internal/middleware/cors.go`: 認証ミドルウェアの外側に置く(プリフライトと 401 にも CORS ヘッダーを付けるため)
- 保護するルートは `newMux` の `authn(...)` で包む(例: `GET /me`)

## ユーザーのデータの削除(退会)
- `DELETE /me`(`cmd/server/profile.go` の `deleteMe`): 呼んだ本人のデータを消す。`user_id` はトークンの `sub` だけから取る。冪等で、データがなくても `204 No Content`(Web が失敗後に再送できるように)。5xx のときは、中身を返さずログに残す。Web の退会は、この呼び出しの後に Better Auth の `deleteUser` を実行する想定(Web 側は実装済み: `src/lib/delete-account.ts`)
- 実際に消すのは `profiles` の1行だけ(`profile.Store.Delete`)。`profiles` が根で、他のテーブルは `user_id` から `profiles(user_id)` への外部キーで、退会時の扱いが決まる
- **全部カスケードにはしない。** コメントのように、退会後も残したいデータがあるため、`user_id` を持つテーブルは、扱いを `internal/userdata` の `Tables` に宣言する(理由も書く)。DB の外部キーは、宣言と一致させる:
  - `delete`: 退会で消す。`user_id` は `REFERENCES profiles(user_id) ON DELETE CASCADE`
  - `anonymize`: 残すが、投稿者を外す。`user_id` は NULL 可で `ON DELETE SET NULL`(画面では「退会したユーザー」など)
  - `retain`: そのまま残す(保存義務など)。外部キーなし
- `user_id` を持つテーブルを足すマイグレーションは、同じ変更で `Tables` に宣言する。足し忘れや、外部キーの食い違いは、`internal/profile/userdata_test.go` の `TestDeclaredPoliciesMatchTheSchema` が落ちて分かる(`TEST_DATABASE_URL` が必要)。宣言のテストが `internal/profile` にあるのは、DB のテスト用ヘルパー(未設定なら SKIP)がそこにあるため
- 退会後も、発行済みの JWT は最大 5 分有効。その間に別の端末から `PUT /me/profile` が来ると、`profiles` の行が作り直される(孤児データ)。今は許容している(墓標のテーブルで防ぐ案はあるが、重いので見送り)
  - Web の退会フローを作るときの順番: 先に Better Auth のセッションを全部失効させる(新しい JWT が出なくなる)→ `DELETE /me` → Better Auth のユーザーを削除する。これで、復活する窓はほぼ、すでに発行済みの JWT の残り(最大 5 分)だけになる
- `DELETE /me` が消すのは、この API のデータだけ。アカウント本体(Better Auth のユーザー)は Web の DB にあり、Web が消す

## 設計方針
将来 Echo などへ切り替える可能性がある。次を守る。
- HTTP は標準の `net/http` のみ。外部のルーター/フレームワークを追加しない
- ロジックは `internal/` に置き、`net/http` に依存させない
- ハンドラは薄く保つ(リクエストを読む → ロジックを呼ぶ → レスポンスを返す)
- ミドルウェアは `func(http.Handler) http.Handler` の形で書く

## 実装後の流れ(生成と評価のループ)
実装は自分(ジェネレーター)、評価は `evaluator` エージェント(`.claude/agents/evaluator.md`)が行う。
1. 実装する。`gofmt -l .`・`go vet ./...`・`go build ./...`・`go test ./...` は hooks(`.claude/settings.json`)が自動で実行する(編集ごとに gofmt、応答終了時に全体)。失敗したら差し戻されるので直す
2. `evaluator` を呼ぶ(レビューと改ざん確認担当。挙動を変える変更のとき。ドキュメントやコメントだけの変更では呼ばない)
3. 判定が「要修正」なら、「高」「中」の指摘を直して 2 に戻る。ただし最大 5 周まで(これは打ち切りの上限で、普通はもっと少ない周回で終わる)
4. 「合格」になったら終了し、結果を報告する。5 周で合格しなければ、止めて残っている指摘をそのまま報告する(無理に通さない)。同じ指摘が再発したときは、上限を待たずに止めて報告する
5. 報告には、SKIP されたテスト(例: `TEST_DATABASE_URL` 未設定の DB テスト)と、未検証の項目を必ず書く

### 人間の確認はループの外
- 実際の API の動作確認(curl での確認など)、DB を使った手動テスト、最終レビューは人間の作業。ループの終了条件に含めない
- ループが終わる条件は「検証がすべて通り、evaluator が合格」(または最大 5 周で打ち切り)だけ
- 終了時の報告に、人間向けの確認手順と、AI が検証していない範囲(SKIP された DB テストなど)を書く。人間の返事は待たない
- 人間が問題を見つけたら、その内容を起点に新しい goal として回す
- goal を書くときは、完了条件(AI がループ内で満たす。機械で確認できるものだけ)と、ループの外(人間の作業)の節を分ける

### テストを通すための改ざんは禁止
合格させるために、次をしてはいけない。やむを得ず変えるときは、理由を報告に書き、人間の確認を待つ。
- 失敗するテストの削除、`t.Skip` の追加、`-run` や build タグで外すこと
- 期待値やアサーションを、実装の出力に合わせて弱める・書き換えること(仕様が変わった場合を除く)
- 検証用の値(上限値、タイムアウト、許可リストなど)を、通るように緩めること
- `evaluator` の指摘を、直さずにコメントや設定で黙らせること
テストが落ちたら、まず実装が間違っていると考える。テストの側が間違っていると判断するときも、その根拠を書く。

### コミットとマイグレーション
- 人が頼むまでコミットしない(変更は未コミットで残し、先に読んでもらう)
- `goose up` などマイグレーションは実行しない(手順を案内するだけにする)

### hooks が強制していること(`.claude/hooks/`)
- PreToolUse でブロック: `.env` 系の読み取り(`.env.example` は可)、`git commit/push/clean/reset --hard`、`rm -r`、`goose up/down/reset/redo`、既存 `migrations/` の編集、`go.sum`・`.github/workflows/` の編集、`_test.go` への `t.Skip` の追加
- ブロックされたら回避せず、理由を報告して人の指示を待つ

## 進捗ファイル
- `claude-progress.txt` に、完了・実行中・これからのタスクを書く。新しいセッションは、最初にこのファイルと CLAUDE.md を読む
- goal が終わるたびに(ループの終了時の報告と一緒に)更新する。長いセッションで文脈が劣化しても、続きから始められるようにするため
- PR を作るとき(頼まれたとき)は、PR の前に `claude-progress.txt` を書き直す: 完了したものを「完了したタスク」に移し、「実行中」を現状に合わせ(「コミット直前」などの古い記述を消す)、「これから」の先頭を次の作業にする。進捗ファイルの変更も同じ PR に入れる
