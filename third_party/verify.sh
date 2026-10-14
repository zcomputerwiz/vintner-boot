#!/bin/sh
# Check every pin in pins.tsv against its upstream. Prints OK, MISMATCH or
# MISSING per row and exits non-zero on any failure.
set -e
here=$(cd "$(dirname "$0")" && pwd)
fail=0

while IFS='	' read -r name kind location ref commit _ role; do
  if [ "$name" = "name" ]; then continue; fi
  if [ "$kind" = "git" ]; then
    if [ "$role" = "branch snapshot" ]; then
      # The branch head moves; the pin holds as long as the recorded commit
      # still exists upstream. tree:0 fetches the commit object only.
      tmp=$(mktemp -d)
      if git -C "$tmp" init -q && git -C "$tmp" fetch -q --depth=1 --filter=tree:0 "$location" "$commit" 2>/dev/null; then
        echo "OK $name $kind $ref@$commit"
      else
        echo "MISSING $name $kind $ref@$commit"
        fail=1
      fi
      rm -rf "$tmp"
      continue
    fi
    peeled=$(git ls-remote --tags "$location" "$ref^{}" | awk '{print $1}')
    if [ -n "$peeled" ]; then
      actual="$peeled"
    else
      actual=$(git ls-remote --tags "$location" "$ref" | awk '{print $1}')
    fi
    if [ "$actual" = "$commit" ]; then
      echo "OK $name $kind $ref"
    else
      echo "MISMATCH $name $kind $ref (expected $commit, got $actual)"
      fail=1
    fi
  elif [ "$kind" = "debian" ]; then
    if curl -fsS "https://sources.debian.org/api/src/${location}/" | grep -qF "\"version\":\"$ref\""; then
      echo "OK $name $kind $ref"
    else
      echo "MISSING $name $kind $ref"
      fail=1
    fi
  fi
done < "$here/pins.tsv"

if [ "$fail" -eq 1 ]; then
  exit 1
fi
