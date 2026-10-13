#!/usr/bin/env bash
# Tests for check-pr-links-issue.sh. Run from anywhere.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
check="$here/check-pr-links-issue.sh"
fail=0

expect() { # expect <want-exit> <name> <body>
  local want="$1" name="$2" body="$3" got
  PR_BODY="$body" "$check" >/dev/null 2>&1
  got=$?
  if [ "$got" -ne "$want" ]; then
    echo "FAIL $name: want exit $want, got $got"
    fail=1
  else
    echo "ok   $name"
  fi
}

expect 0 closes            'Closes #12'
expect 0 lowercase         'closes #4'
expect 0 fixes             'Fixes #12'
expect 0 resolves-colon    'resolves: #3'
expect 0 closed-past       'Closed #7'
expect 0 in-paragraph      $'Adds the parser.\n\nCloses #12\n\nTested with make.'
expect 1 empty             ''
expect 1 mention-only      'see #12'
expect 1 no-number         'Closes #'
expect 1 other-repo        'Closes owner/other#12'
expect 1 keyword-in-word   'encloses #12'
expect 1 no-space          'Closes#12'

# Shell metacharacters in the body are data, never executed.
marker="$(mktemp -u)"
expect 1 metachar-no-link  "\$(touch $marker) \`touch $marker\`"
expect 0 metachar-link     "\$(touch $marker) Closes #5"
if [ -e "$marker" ]; then
  echo "FAIL metachar: body was executed"
  rm -f "$marker"
  fail=1
fi

# An unset PR_BODY fails cleanly with the readable message.
msg="$(env -u PR_BODY "$check" 2>&1 >/dev/null)"
case "$msg" in
  *'must link an issue'*) echo "ok   unset-body" ;;
  *) echo "FAIL unset-body: got '$msg'"; fail=1 ;;
esac

exit "$fail"
