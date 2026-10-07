#!/usr/bin/env bash
# PreToolUse: 禁止事項を実行前にブロックする(exit 2 + stderr で Claude に理由を返す)
set -u
input=$(cat)
tool=$(jq -r '.tool_name // ""' <<<"$input")
cmd=$(jq -r '.tool_input.command // ""' <<<"$input")
path=$(jq -r '.tool_input.file_path // .tool_input.path // ""' <<<"$input")
content=$(jq -r '[.tool_input.content, .tool_input.new_string, (.tool_input.edits[]?.new_string)] | map(select(. != null)) | join("\n")' <<<"$input")

block() { echo "BLOCKED by guard-pre.sh: $1" >&2; exit 2; }

rel=${path#"${CLAUDE_PROJECT_DIR:-$PWD}"/}

# --- .env の読み取り(.env.example は許可) ---
if [[ -n "$path" && "$(basename "$path")" == .env* && "$(basename "$path")" != ".env.example" ]]; then
  block ".env 系ファイルは読まない(.env.example は可)"
fi
if [[ "$tool" == "Bash" ]]; then
  stripped=${cmd//.env.example/}
  if grep -Eq "(^|[[:space:]/\"'=])\.env(\.[A-Za-z0-9._-]+)?(\$|[[:space:]\"'|;&)])" <<<"$stripped"; then
    block ".env 系ファイルをコマンドで読まない(.env.example は可)"
  fi
fi

# --- Bash の危険操作 ---
if [[ "$tool" == "Bash" ]]; then
  sep='(^|[;&|][[:space:]]*)'
  grep -Eq "${sep}git[[:space:]]+(-[^[:space:]]+[[:space:]]+)*(commit|push|clean|reset[[:space:]]+--hard)" <<<"$cmd" \
    && block "git commit/push/clean/reset --hard は人が頼むまでしない(変更は未コミットで残す)"
  grep -Eq "${sep}rm[[:space:]]+-[a-zA-Z]*[rR]" <<<"$cmd" \
    && block "rm -r は禁止。必要なら人に確認する"
  grep -Eq "goose.*[[:space:]](up|up-by-one|up-to|down|down-to|reset|redo)([[:space:]]|\$)" <<<"$cmd" \
    && block "goose のマイグレーション実行(up/down/reset/redo)は人が頼んだときだけ。手順を案内するに留める"
fi

# --- 保護対象ファイルの編集禁止 ---
if [[ "$tool" =~ ^(Edit|Write|MultiEdit)$ && -n "$rel" ]]; then
  case "$rel" in
    migrations/*)
      [[ -e "$path" ]] && block "既存のマイグレーションは編集しない。新しいマイグレーションを追加する" ;;
    go.sum) block "go.sum は手編集しない(go mod tidy 等で更新する)" ;;
    .github/workflows/*) block "CI 設定は人が頼んだときだけ変更する" ;;
  esac

  # --- テストを黙らせる回避の禁止 ---
  case "$rel" in
    *_test.go)
      grep -Eq 't\.Skip(Now|f)?\(' <<<"$content" \
        && block "t.Skip の追加は禁止。やむを得ないなら理由を報告して人の確認を待つ"
      ;;
  esac
fi
exit 0
