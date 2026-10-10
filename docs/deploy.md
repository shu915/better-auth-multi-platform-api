# マイグレーションとデプロイ

## マイグレーション(開発)
人が手で実行する。Claude Code では hooks が `up` / `down` などをブロックする。goose を `go tool` で実行する。SQL は `migrations/`。先に `docker compose up -d db`。

接続先は環境変数で渡す(URL をコマンドライン引数に載せると `ps` やシェル履歴に残るため):

```
export GOOSE_DRIVER=postgres GOOSE_DBSTRING="postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
```

(開発用のダミーの値)

- `go tool goose -dir migrations create <name> sql -s`: 雛形を作る(`-s` で連番)
- `go tool goose -dir migrations up`: 未適用分を適用する
- `go tool goose -dir migrations status` / `down`: 状態の確認 / 1 つ戻す(開発だけ)
- スキーマを最初から作り直すときは、`docker compose down -v` で DB のボリュームを消す

## マイグレーション(本番)
- アプリの起動時ではなく、デプロイ手順の中で 1 回だけ実行する(複数タスクの同時起動で衝突させないため)
- `GOOSE_DBSTRING` は `sslmode=require` 以上の URL にする(goose にはアプリの TLS 検証がない)
- **手順(Render):** 本番のイメージ(distroless)に goose と `migrations/` は入っていない。手元(このリポジトリ)から、Render の DB の **外部 URL**(Render の DB の設定で自分の IP を許可する)を `GOOSE_DBSTRING` に入れて、`go tool goose -dir migrations up` を 1 回実行する。`status` で全部 Applied を確認してから、サービスを出す(スキーマがないと `/health` は緑のまま、`/me/profile` が全部 500 になる)
- スキーマを変えるときも、先にこの手順で DB を進めてから新しいイメージを出す(古いイメージが動いている間に壊れない足し方だけにする)
- `down` は使わない(`DROP TABLE` でデータが消える)。前に進める方向だけ。戻したいときは新しいマイグレーションを足す
- マイグレーション用(DDL)とアプリ実行用(SELECT / INSERT / UPDATE / DELETE のみ)の DB ユーザーは分けるのが望ましい(未実施)

## デプロイ先
- API(この Go)とその DB は **Render**。Web は **Vercel + Neon**(Web のリポジトリの CLAUDE.md)
- `AUTH_ISSUER` と `AUTH_JWKS_URL` は、Vercel の本番 URL(https)にする
- Render の DB に `verify-full` で接続できるか(証明書)は未確認。通るなら `DATABASE_URL` を `verify-full` にする
