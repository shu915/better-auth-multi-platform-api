# better-auth-multi-platform (api)

ひとつのログインを、複数のクライアントで共有するためのサンプルの、Go 側の API です。Web アプリ
(Better Auth)が発行した JWT を検証して、ログイン中のユーザーのプロフィールを読み書きします。
Go は JWT を検証するだけで、呼び出し元を区別しません。そのため、将来のデスクトップ(Tauri)や
モバイル(Expo)からも、Go を変えずに呼べます(今は Web のみ)。

Web 側(Next.js、Better Auth)は
[better-auth-multi-platform-web](https://github.com/shu915/better-auth-multi-platform-web) にあります。

## 全体像

```
ブラウザ ──▶ Next.js (Vercel) ──▶ Go API (Render)  ◀── この API
               │  JWT を発行          │  Web の公開鍵 (JWKS) で署名を検証
               ▼                     ▼
           Postgres (Neon)      Postgres (Render)
           ユーザー、セッション     profiles (bio)
```

- **認証は Web が担当します。** Go は、`Authorization: Bearer` の JWT を、Web の
  `/api/auth/jwks` にある公開鍵で検証します。許可するのは EdDSA だけで、`iss`、`aud`、`exp`、
  `sub` を検証します。公開鍵はキャッシュし、未知の `kid` のときだけ(15 秒に 1 回まで)取り直します。
- **ユーザー ID は、トークンの `sub` だけから取ります。** クエリやボディで他人を指すことは
  できません。
- **公開鍵が取れないときは、401 ではなく 503 を返します**(トークンの問題ではないため)。

## エンドポイント

| メソッド | パス | 認証 | 内容 |
|---|---|---|---|
| GET | `/health` | 不要 | 生存確認(DB は見ない)。 |
| GET | `/me` | 必要 | トークンのユーザー ID を返す。 |
| GET | `/me/profile` | 必要 | プロフィール(bio)を返す。行がなければ空のプロフィール。 |
| PUT | `/me/profile` | 必要 | bio を保存する。1000 文字まで。NUL と不正な UTF-8 は 422、本体は 16KB まで(超えると 413)。 |
| DELETE | `/me` | 必要 | 呼んだ本人のデータを消す。冪等で、なくても 204。Web の退会が呼ぶ。 |

保護するルートの応答は、`Cache-Control: no-store` です。

## 技術スタック

| 分類 | 技術 | バージョン |
|---|---|---|
| 言語 | Go | 1.27 |
| HTTP | 標準の `net/http` | - |
| DB ドライバ | pgx | 5.11 |
| JWT / JWKS | lestrrat-go/jwx | 3.3 |
| マイグレーション | goose | 3.28 |
| DB | Postgres(本番は Render、開発と CI は 17) | - |
| コンテナ | Docker(distroless の最終イメージ) | - |
| 開発時のホットリロード | Air | - |
| ホスティング | Render(Web は Vercel と Neon) | - |

フレームワークは使わず、標準の `net/http` だけです。将来、別のルーター(Echo など)へ替えやすい
ように、ロジックは `internal/` に置き、ハンドラは薄く保っています。

## 手元で動かす

必要なもの: Go、Docker、動いている Web アプリ(JWT の発行元)。

```bash
docker compose up -d db        # 開発用の Postgres
export GOOSE_DRIVER=postgres GOOSE_DBSTRING="postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
go tool goose -dir migrations up
air                            # http://localhost:8080 (保存で自動再起動)
```

`docker compose up --build` で、API も Docker で起動できます。

### 環境変数

開発では、すべて既定値で動きます。

| 変数 | 説明 |
|---|---|
| `APP_ENV` | `development`(未設定も同じ)か `production`。それ以外は起動時にエラー。`production` では、下の必須の値がないと起動しません。 |
| `PORT` | 待ち受けポート。既定は 8080。 |
| `AUTH_ISSUER` | JWT の `iss`。Web の `BETTER_AUTH_URL` と同じにする(末尾に `/` を付けない。付けると起動時にエラー)。本番では必須。 |
| `AUTH_AUDIENCE` | JWT の `aud`。Web の `JWT_AUDIENCE` と同じにする。本番では必須。 |
| `AUTH_JWKS_URL` | 公開鍵の取得先。既定は `<AUTH_ISSUER>/api/auth/jwks`。本番では https のみ。 |
| `CORS_ALLOWED_ORIGINS` | ブラウザから直接呼ぶ場合のオリジン(カンマ区切り、完全一致)。Web は Next.js のサーバー経由なので、今は使われない。本番では必須。 |
| `DATABASE_URL` | Postgres の接続文字列。本番では必須で、`sslmode=require` 以上でないと起動時にエラー。 |

## コマンド

| コマンド | 内容 |
|---|---|
| `air` | 開発サーバー。 |
| `go build -o bin/server ./cmd/server` | ビルド。 |
| `gofmt -l .` / `go vet ./...` / `go test ./...` | 整形の確認、静的解析、テスト。 |
| `go tool goose -dir migrations up` / `status` | マイグレーションの適用と状態の確認。 |

## デプロイ

本番は Render です(Docker、DB も Render)。

1. Render で Postgres を作り、サービスと**同じリージョン**に置く。
2. 手元から、DB の外部 URL(`sslmode=require` 以上)を `GOOSE_DBSTRING` に入れて、
   `go tool goose -dir migrations up` を 1 回実行する。`status` で全部 Applied を確認する。
   本番のイメージには goose も `migrations/` も入っていません。スキーマがないと、`/health` は緑の
   まま、`/me/profile` が全部 500 になります。
3. サービスを作り、環境変数(`APP_ENV=production`、`DATABASE_URL`(内部 URL)、`AUTH_ISSUER`、
   `AUTH_AUDIENCE`、`CORS_ALLOWED_ORIGINS`)を設定する。Health Check のパスは `/health`。
4. Web を、Go のあとにデプロイする(Web の退会が `DELETE /me` を呼ぶため)。

## 退会時のデータ

`DELETE /me` が消すのは、`profiles` の 1 行だけです。他のテーブルが `user_id` を持つときは、
退会時の扱い(消す、匿名にして残す、そのまま残す)を `internal/userdata` に、理由つきで宣言し
ます。宣言と DB の外部キーが食い違うと、テスト(`TestDeclaredPoliciesMatchTheSchema`)が落ちます。

## テスト

- **単体**: JWT の検証(`none` や HS 系の混同、期限、`iss`/`aud`)、ミドルウェア(認証、CORS、
  panic の回復、期限)、起動時の設定の検証、DB の接続設定。
- **結合**(`cmd/server/integration_test.go`): HTTP のハンドラ、本物の JWT 検証(テスト用の鍵と
  JWKS サーバー)、本物のストア、本物の Postgres を通す。PUT、GET、DELETE の一本道、冪等性、
  他のユーザーに影響しないこと、`user_id` をトークンの `sub` だけから取ること、不正なトークンで
  何も変わらないこと、bio の規則が実 DB で効くことを確かめる。
- DB を使うテストは、`TEST_DATABASE_URL` がないと SKIP されます。専用のスキーマを使うので、
  本物のデータがない DB を指してください。
- CI(GitHub Actions)は、`gofmt`、`go vet`、ビルド、テストを、Postgres を立てて実行し、DB を使う
  テストが SKIP されたら失敗にします。

設計の判断など、開発者向けの詳しい記述は、`CLAUDE.md` と `claude-progress.txt` にあります。
