# 設計メモ(機能ごとの詳細)

CLAUDE.md から切り出した、実装の方針と理由。コードを変えたら、ここも合わせて直す。

## 環境変数
- `APP_ENV`: `development`(未設定も同じ)か `production`。それ以外は起動時にエラー。`production` では `AUTH_ISSUER`・`AUTH_AUDIENCE`・`CORS_ALLOWED_ORIGINS`(オリジンが 1 つ以上)・`DATABASE_URL`(TLS 必須)が必須(未設定なら起動時にエラー)
- `PORT`: 待ち受けポート(既定 8080)
- `AUTH_ISSUER`: JWT の `iss`。Web の `BETTER_AUTH_URL` と同じにする(既定 `http://localhost:3000`)
- `AUTH_AUDIENCE`: JWT の `aud`。Web の `JWT_AUDIENCE` と同じにする(既定 `better-auth-multi-platform-api`)
- `AUTH_JWKS_URL`: 公開鍵の取得先(既定 `<AUTH_ISSUER>/api/auth/jwks`)。`https` のみ可(localhost / host.docker.internal だけ `http` 可)。`APP_ENV=production` では localhost を含めて `https` のみ(起動時にエラー)
- `CORS_ALLOWED_ORIGINS`: ブラウザから Go を直接呼ぶ場合のオリジン。Web は BFF 経由なので現状は使われない(設定は残してある)。カンマ区切り、完全一致(既定 `http://localhost:3000`)。本番では必須
- `DATABASE_URL`: Postgres の接続文字列(パスワードを含む)。既定は `docker-compose.yml` の開発用 DB(ダミーの認証情報、`localhost:5432`)。本番では必須で外部から注入する。本番は `sslmode=require` 以上でないと起動時にエラー(`prefer` や sslmode 省略も平文に落ちるので不可)

## 認証(JWT 検証)
- `internal/auth`: JWT の検証本体(jwx v3)。EdDSA のみ許可し、`iss`・`aud`・`exp`・`sub` を検証する。JWKS はキャッシュし、未知の `kid` のときだけ(15 秒に 1 回まで)再取得する。同時に来たリクエストは 1 回の取得を待って結果を共有する(singleflight)
- `internal/middleware`: `Authorization: Bearer` を取り出して検証し、ユーザー ID を `context` に入れる(`middleware.UserID`)
- 公開鍵を取得できないときは 401 ではなく 503(トークンの問題ではないため)。`auth.ErrInvalidToken` だけが 401
- `internal/middleware/recover.go`: handler の panic を 500 にする(最外側)。ログには panic の値だけを出し、リクエスト(トークンを含みうる)は出さない
- `internal/middleware/timeout.go`: 1 リクエストに 10 秒の期限(`requestTimeout`)。DB のクエリに効く。JWKS の再取得は、別に自前の 3 秒で切れる
- `internal/db`: プールは最大 10 接続、`statement_timeout` は 5 秒(`db.MaxConns`、`db.StatementTimeout`)
- `internal/middleware/cors.go`: 認証ミドルウェアの外側に置く(プリフライトと 401 にも CORS ヘッダーを付けるため)
- 保護するルートは `newMux` の `authn(...)` で包む(例: `GET /me`)

## ユーザーのデータの削除(退会)
- `DELETE /me`(`cmd/server/profile.go` の `deleteMe`): 呼んだ本人のデータを消す。`user_id` はトークンの `sub` だけから取る。冪等で、データがなくても `204 No Content`(Web が失敗後に再送できるように)。5xx のときは中身を返さずログに残す。Web の退会は、この呼び出しの後に Better Auth の `deleteUser` を実行する(Web 側は実装済み: `src/lib/delete-account.ts`)
- 実際に消すのは `profiles` の 1 行だけ(`profile.Store.Delete`)。`profiles` が根で、他のテーブルは `user_id` から `profiles(user_id)` への外部キーで退会時の扱いが決まる
- `profiles.user_id` 自体に外部キーはない(ユーザーは Web の DB にある)
- **全部カスケードにはしない。** 退会後も残したいデータがあるため、`user_id` を持つテーブルは扱いを `internal/userdata` の `Tables` に宣言する(理由も書く)。DB の外部キーは宣言と一致させる:
  - `delete`: 退会で消す。`user_id` は `REFERENCES profiles(user_id) ON DELETE CASCADE`
  - `anonymize`: 残すが投稿者を外す。`user_id` は NULL 可で `ON DELETE SET NULL`(画面では「退会したユーザー」など)
  - `retain`: そのまま残す(保存義務など)。外部キーなし
- `user_id` を持つテーブルを足すマイグレーションは、同じ変更で `Tables` に宣言する。足し忘れや外部キーの食い違いは、`internal/profile/userdata_test.go` の `TestDeclaredPoliciesMatchTheSchema` が落ちて分かる(`TEST_DATABASE_URL` が必要。DB のテスト用ヘルパーが `internal/profile` にあるため、テストもそこにある)
- 退会後も、発行済みの JWT は最大 5 分有効。その間に別の端末から `PUT /me/profile` が来ると `profiles` の行が作り直される(孤児データ)。今は許容している(墓標のテーブルで防ぐ案はあるが、重いので見送り)。Web の退会は、先に Better Auth のセッションを全部失効させる → `DELETE /me` → ユーザー削除の順にして、窓を小さくしている
- `DELETE /me` が消すのは、この API のデータだけ。アカウント本体(Better Auth のユーザー)は Web の DB にあり、Web が消す
- `updated_at` は自動では更新されない。`UPDATE` のたびに `updated_at = now()` を入れる

## テスト
- DB を使うテストは、`TEST_DATABASE_URL` がないと SKIP される。Stop hook は、開発用 DB(`docker compose up -d db`)が起動していれば自動でこの変数を渡す。CI にも Postgres を入れる(`.github/workflows/ci.yml`)
- `cmd/server/integration_test.go`: 結合テスト。HTTP のハンドラ、本物の JWT 検証(テスト用の鍵と JWKS サーバー)、本物のストア、本物の Postgres を通す。確かめること: PUT → GET → DELETE の一本道、DELETE の冪等性、他のユーザーに影響しない、`user_id` はトークンの `sub` だけから取る(クエリやボディで他人を指せない)、不正なトークンでは何も変わらない、bio の規則(1000 文字、NUL)が実 DB で効く
- DB が要るテストは `internal/testdb` の `Pool(t)` を使う(未設定なら SKIP)
