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
- `go build ./cmd/server`: ビルド
- `go test ./...`: テスト

## 設計方針
将来 Echo などへ切り替える可能性がある。次を守る。
- HTTP は標準の `net/http` のみ。外部のルーター/フレームワークを追加しない
- ロジックは `internal/` に置き、`net/http` に依存させない
- ハンドラは薄く保つ(リクエストを読む → ロジックを呼ぶ → レスポンスを返す)
- ミドルウェアは `func(http.Handler) http.Handler` の形で書く
