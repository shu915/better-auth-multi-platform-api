# better-auth-multi-platform-api

## 概要
Next.js と Better Auth、Go で認証を実装する。
この API は Go で、Web(別リポジトリ `better-auth-multi-platform-web`)が発行した JWT を検証する。
将来は Tauri(デスクトップ)と Expo(モバイル)からも、同じ Go のサービスを使う予定(おいおい実装する。今は Web のみ)。
認証は Next.js の Better Auth が担当する。Web は Next.js のサーバー(BFF)経由で Go を呼ぶ。
サーバーが、呼ぶたびに JWT を発行して付ける(`src/lib/api-server.ts` の `callApi`)。ブラウザには JWT を出さず、フォームも Server Action 経由にする。
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
- `go test ./...`: テスト
- マイグレーション(goose を `go tool` で実行。SQL は `migrations/`。先に `docker compose up -d db`):
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
  - 別 DB のユーザーを指す列(`user_id`)に外部キーはない。ユーザー削除との同期は、いずれ決める

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

## 設計方針
将来 Echo などへ切り替える可能性がある。次を守る。
- HTTP は標準の `net/http` のみ。外部のルーター/フレームワークを追加しない
- ロジックは `internal/` に置き、`net/http` に依存させない
- ハンドラは薄く保つ(リクエストを読む → ロジックを呼ぶ → レスポンスを返す)
- ミドルウェアは `func(http.Handler) http.Handler` の形で書く
