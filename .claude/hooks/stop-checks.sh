#!/usr/bin/env bash
# Stop: 応答終了時に gofmt / vet / build / test を実行。失敗したら Claude に差し戻す(exit 2)
set -u
input=$(cat)
active=$(jq -r '.stop_hook_active // false' <<<"$input")
cd "${CLAUDE_PROJECT_DIR:-$PWD}" || exit 0
git rev-parse --git-dir >/dev/null 2>&1 || exit 0

# 前回成功時から変更がなければスキップ
state="$(git rev-parse --git-dir)/claude-stop-check"
hash=$({ git status --porcelain; git diff HEAD; git ls-files -o --exclude-standard | xargs shasum 2>/dev/null; } | shasum | cut -d' ' -f1)
[[ -f "$state" && "$(cat "$state")" == "$hash" ]] && exit 0

fails=""
skips=0
run() { # run <name> <cmd...>
  local name=$1; shift
  local out
  out=$("$@" 2>&1); local rc=$?
  if [[ $name == fmt && -n $out ]]; then
    fails+="### gofmt: 整形が必要なファイル(gofmt -w で直す)"$'\n'"$out"$'\n\n'
  elif [[ $name != fmt && $rc -ne 0 ]]; then
    fails+="### $name failed ($*)"$'\n'"$(tail -n 50 <<<"$out")"$'\n\n'
  fi
  [[ $name == test ]] && skips=$(grep -c -- '--- SKIP' <<<"$out")
}

run fmt gofmt -l .
run vet go vet ./...
run build go build ./...
run test go test -v ./...

if [[ -z "$fails" ]]; then
  echo "$hash" >"$state"
  if (( skips > 0 )); then
    jq -n --arg m "Stop hook: 検証OK。ただしテスト SKIP が ${skips} 件あります(例: TEST_DATABASE_URL 未設定)。未検証として報告してください。" '{systemMessage:$m}'
  fi
  exit 0
fi
if [[ "$active" == "true" ]]; then
  jq -n --arg m "Stop hook: 差し戻し後も検証が失敗しています。人の確認が必要です。" '{systemMessage:$m}'
  exit 0
fi
{ echo "静的チェックが失敗しました。直してください(テストや値を弱めて通さない)。"; echo; echo "$fails"; } >&2
exit 2
