#!/bin/sh
set -eu

failed=0
files="README.md README_CN.md $(find docs -type f -name '*.md' -print | sort)"

for file in $files; do
  links=$(grep -oE '\[[^]]+\]\([^)]+\.md(#[^)]*)?\)' "$file" | sed -E 's/^.*\]\(([^)#]+\.md)(#[^)]*)?\)$/\1/' || true)
  for link in $links; do
    if ! (cd "$(dirname "$file")" && test -f "$link"); then
      printf '%s: broken Markdown link: %s\n' "$file" "$link" >&2
      failed=1
    fi
  done
done

if [ "$failed" -ne 0 ]; then
  exit 1
fi

printf 'Markdown links verified.\n'
