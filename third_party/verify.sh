#!/bin/sh
set -e
fail=0

while IFS='	' read -r name kind location ref commit _ _; do
  if [ "$name" = "name" ]; then continue; fi
  if [ "$kind" = "git" ]; then
    if [ "$ref" = "master" ]; then
      actual=$(git ls-remote --heads "$location" "$ref" | awk '{print $1}')
    else
      peeled=$(git ls-remote --tags "$location" "$ref^{}" | awk '{print $1}')
      if [ -n "$peeled" ]; then
        actual="$peeled"
      else
        actual=$(git ls-remote --tags "$location" "$ref" | awk '{print $1}')
      fi
    fi
    if [ "$actual" = "$commit" ]; then
      echo "OK $name $kind $ref"
    else
      echo "MISMATCH $name $kind $ref (expected $commit, got $actual)"
      fail=1
    fi
  elif [ "$kind" = "debian" ]; then
    if curl -s "https://sources.debian.org/api/src/${location}/" | grep -q "\"version\":\"$ref\""; then
      echo "OK $name $kind $ref"
    else
      echo "MISSING $name $kind $ref"
      fail=1
    fi
  fi
done < third_party/pins.tsv

if [ "$fail" -eq 1 ]; then
  exit 1
fi
