#!/usr/bin/env bash
# Fails unless the pull request description (env PR_BODY) links an issue in
# this repo with a GitHub closing keyword, e.g. "Closes #12" (principle P11).
set -u
body="${PR_BODY:-}"
pattern='(^|[^[:alnum:]_])(close[sd]?|fix(e[sd])?|resolve[sd]?):?[[:space:]]+#[0-9]+'
if match="$(printf '%s\n' "$body" | grep -o -i -E "$pattern" | head -n 1)" && [ -n "$match" ]; then
  echo "ok: links #${match##*#}"
  exit 0
fi
echo 'pull request description must link an issue, e.g. "Closes #12"' >&2
exit 1
