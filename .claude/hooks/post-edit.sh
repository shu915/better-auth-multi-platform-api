#!/usr/bin/env bash
# PostToolUse(Edit|Write|MultiEdit): 編集した .go ファイルの gofmt 差分を指摘する(自動整形はしない)
set -u
input=$(cat)
path=$(jq -r '.tool_input.file_path // ""' <<<"$input")
[[ "$path" == *.go && -f "$path" ]] || exit 0
out=$(gofmt -l "$path" 2>&1)
[[ -z "$out" ]] && exit 0
{ echo "gofmt が必要です: $path"; echo "gofmt -w $path を実行してください。"; } >&2
exit 2
