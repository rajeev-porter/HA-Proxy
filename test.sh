#!/usr/bin/env bash
# CLI companion to the web console. Covers the annotations a browser can't drive
# cleanly: slow-client timeouts, large request headers (buffer sizing), CORS
# preflight, and source-IP allow/deny from your real address.
#
# Usage:
#   ./test.sh https://your-app.host           # run all
#   ./test.sh https://your-app.host bodysize  # run one section
#
# Sections: ping timeout_server timeout_client bodysize ratelimit headers
#           cors allowlist bigheader
set -uo pipefail

BASE="${1:-http://localhost:8080}"; BASE="${BASE%/}"
ONLY="${2:-all}"
CURL=(curl -sk --max-time 60)

c_pass=$'\033[32m'; c_fail=$'\033[31m'; c_info=$'\033[33m'; c_dim=$'\033[90m'; c_off=$'\033[0m'
say(){ printf '%s\n' "$*"; }
hdr(){ printf '\n%s== %s ==%s\n' "$c_dim" "$1" "$c_off"; }
code(){ "${CURL[@]}" -o /dev/null -w '%{http_code}' "$@"; }

want(){ [ "$ONLY" = all ] || [ "$ONLY" = "$1" ]; }

want ping && { hdr "ping (connectivity + request id)"
  out=$("${CURL[@]}" -D - -o /dev/null "$BASE/api/ping" 2>/dev/null)
  st=$(printf '%s' "$out" | head -1)
  rid=$(printf '%s' "$out" | grep -i '^x-request-id:' | tr -d '\r')
  say "  $st"
  if [ -n "$rid" ]; then say "  ${c_pass}PASS${c_off} $rid"; else say "  ${c_info}INFO${c_off} no X-Request-ID (add config-frontend snippet)"; fi
}

want timeout_server && { hdr "timeout-server (read timeout)  annotation: haproxy-ingress.github.io/timeout-server"
  st=$(code "$BASE/api/delay?ms=8000")
  if [ "$st" = 504 ] || [ "$st" = 502 ]; then say "  ${c_pass}PASS${c_off} HAProxy returned $st before the 8s backend reply"
  else say "  ${c_info}INFO${c_off} got $st — set timeout-server below 8s, or it isn't applied"; fi
}

want timeout_client && { hdr "timeout-client (slow client)  annotation: haproxy-ingress.github.io/timeout-client"
  # Drip-feed an upload slower than the client timeout using --limit-rate.
  st=$(head -c 200000 /dev/zero | "${CURL[@]}" --limit-rate 2k -o /dev/null -w '%{http_code}' \
        -H 'Content-Type: application/octet-stream' --data-binary @- "$BASE/api/echo")
  if [ "$st" = 408 ] || [ "$st" = 400 ] || [ -z "$st" ]; then say "  ${c_pass}PASS${c_off} slow upload cut (status '${st:-connection closed}')"
  else say "  ${c_info}INFO${c_off} upload completed (status $st) — lower timeout-client or raise payload"; fi
}

want bodysize && { hdr "proxy-body-size  annotation: haproxy-ingress.github.io/proxy-body-size"
  st=$(head -c 2097152 /dev/zero | "${CURL[@]}" -o /dev/null -w '%{http_code}' \
        -H 'Content-Type: application/octet-stream' --data-binary @- "$BASE/api/echo")
  if [ "$st" = 413 ]; then say "  ${c_pass}PASS${c_off} 2 MiB POST rejected with 413"
  else say "  ${c_info}INFO${c_off} got $st — set proxy-body-size under 2m"; fi
}

want ratelimit && { hdr "limit-rps  annotation: haproxy-ingress.github.io/limit-rps"
  denied=0; ok=0
  for i in $(seq 1 40); do
    st=$(code "$BASE/api/ping" &)
  done >/dev/null 2>&1
  # fire 40 in parallel, collect codes
  codes=$(for i in $(seq 1 40); do code "$BASE/api/ping" & done; wait)
  for s in $codes; do case "$s" in 200) ok=$((ok+1));; 403|429|503) denied=$((denied+1));; esac; done
  say "  200:$ok  denied:$denied"
  if [ "$denied" -gt 0 ]; then say "  ${c_pass}PASS${c_off} $denied/40 denied — rate limit active"
  else say "  ${c_info}INFO${c_off} no denials — lower limit-rps or raise burst"; fi
}

want headers && { hdr "headers (custom response header)  annotation: haproxy-ingress.github.io/headers"
  val=$("${CURL[@]}" -D - -o /dev/null "$BASE/api/ping" | grep -i '^x-custom-header:' | tr -d '\r')
  if [ -n "$val" ]; then say "  ${c_pass}PASS${c_off} $val"; else say "  ${c_info}INFO${c_off} X-Custom-Header absent (apply headers key)"; fi
}

want cors && { hdr "cors-enable  annotation: haproxy-ingress.github.io/cors-enable"
  acao=$("${CURL[@]}" -D - -o /dev/null -X OPTIONS \
        -H 'Origin: https://example.com' \
        -H 'Access-Control-Request-Method: GET' \
        "$BASE/api/ping" | grep -i '^access-control-allow-origin:' | tr -d '\r')
  if [ -n "$acao" ]; then say "  ${c_pass}PASS${c_off} preflight returned $acao"
  else say "  ${c_info}INFO${c_off} no Access-Control-Allow-Origin (apply cors-enable + cors-allow-origin)"; fi
}

want allowlist && { hdr "allowlist-source-range  annotation: haproxy-ingress.github.io/allowlist-source-range"
  st=$(code "$BASE/api/ping")
  say "  from your source IP: $st"
  say "  ${c_dim}403 = your IP is outside the range; 200 = inside (or not applied)${c_off}"
}

want bigheader && { hdr "buffer-size (large request header)  global: buffer-size / tune.bufsize"
  big=$(head -c 12000 /dev/zero | tr '\0' 'a')
  st=$(code -H "X-Big: $big" "$BASE/api/ping")
  if [ "$st" = 400 ] || [ "$st" = 431 ] || [ "$st" = 502 ]; then say "  ${c_pass}PASS${c_off} oversized header rejected ($st)"
  else say "  ${c_info}INFO${c_off} 12 KB header accepted ($st) — within current buffer; shrink buffer-size to see rejection"; fi
}

say ""
