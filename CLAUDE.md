# better-auth-multi-platform-api

## 概要
Next.js と Better Auth、Go で認証を実装する。
この API は Go で、Web(別リポジトリ `better-auth-multi-platform-web`)が発行した JWT を検証する。
将来は Tauri(デスクトップ)と Expo(モバイル)からも、同じサービスに組み込む。
認証は Next.js の Better Auth が担当する。クライアント(Web / Tauri / Expo)はそこでログインし、
JWT を取得して Go の API を直接呼ぶ(Next.js はデータの中継をしない)。
ログイン方法はマジックリンクを必須とし、OAuth は任意で有効にできる。パスワードは使わない。

## 使用技術
- Go 1.24(標準の `net/http`)
- Docker(本番は Fargate などのコンテナ基盤を想定)
- Air(開発時のホットリロード)
- Web とは JWT で連携(JWKS で署名を検証。Go はステートレス)

## コマンド
- `air`: 開発サーバー(localhost:8080、保存で自動再起動)
- `docker compose up --build`: Docker で起動
- `go build -o bin/server ./cmd/server`: ビルド
- `go test ./...`: テスト

## 環境変数
- `APP_ENV`: `development`(未設定も同じ)か `production`。それ以外の値は起動時にエラー。`production` にすると、`AUTH_ISSUER`・`AUTH_AUDIENCE`・`CORS_ALLOWED_ORIGINS` が必須になる(未設定なら起動時にエラー)
- `PORT`: 待ち受けポート(既定 8080)
- `AUTH_ISSUER`: JWT の `iss`。Web の `BETTER_AUTH_URL` と同じにする(既定 `http://localhost:3000`)
- `AUTH_AUDIENCE`: JWT の `aud`。Web の `JWT_AUDIENCE` と同じにする(既定 `better-auth-multi-platform-api`)
- `AUTH_JWKS_URL`: 公開鍵の取得先(既定 `<AUTH_ISSUER>/api/auth/jwks`)。`https` のみ可(localhost / host.docker.internal だけ `http` 可)
- `CORS_ALLOWED_ORIGINS`: ブラウザから直接呼ぶ Web のオリジン。カンマ区切り、完全一致(既定 `http://localhost:3000`)。本番(`APP_ENV=production`)では必須

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
