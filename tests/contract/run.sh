#!/usr/bin/env bash
# Hits every path in paths.txt against a running stack. Fails if anything 404s.
# Usage: ./tests/contract/run.sh [base_url] [cookie]
#   base_url defaults to http://localhost:8080
#   cookie is optional; without it we still pass as long as the backend
#   replies 401 / 400 (route registered, just unauthenticated / bad input).

set -u

BASE_URL=${1:-http://localhost:8080}
COOKIE=${2:-}

root=$(cd "$(dirname "$0")" && pwd)
paths_file="$root/paths.txt"

if [[ ! -f "$paths_file" ]]; then
  echo "paths.txt not found at $paths_file" >&2
  exit 2
fi

placeholder_uuid="00000000-0000-0000-0000-000000000000"
fail=0
pass=0

printf "%-6s  %-3s  %s\n" "METHOD" "ST" "PATH"
printf "%.80s\n" "--------------------------------------------------------------------------------"

while IFS= read -r line; do
  # Trim comments and whitespace.
  trimmed=$(printf '%s' "$line" | sed 's/#.*//' | awk '{$1=$1;print}')
  [[ -z "$trimmed" ]] && continue

  method=$(printf '%s' "$trimmed" | awk '{print $1}')
  path=$(printf '%s' "$trimmed" | awk '{print $2}')
  # Substitute every {uuid} with the placeholder.
  resolved=${path//\{uuid\}/$placeholder_uuid}

  cookie_flag=()
  if [[ -n "$COOKIE" ]]; then
    cookie_flag=(-H "Cookie: $COOKIE")
  fi

  status=$(curl -s -o /dev/null -w "%{http_code}" \
    -X "$method" "$BASE_URL$resolved" \
    -H 'Content-Type: application/json' \
    --data '{}' \
    "${cookie_flag[@]}" || echo 000)

  if [[ "$status" == "404" ]]; then
    mark="FAIL"
    fail=$((fail + 1))
  else
    mark="OK"
    pass=$((pass + 1))
  fi
  printf "%-6s  %-3s  %s  [%s]\n" "$method" "$status" "$resolved" "$mark"
done < "$paths_file"

echo
echo "pass=$pass fail=$fail"
exit $fail
