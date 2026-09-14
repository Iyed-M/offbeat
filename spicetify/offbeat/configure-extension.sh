#!/bin/sh
set -eu

usage() {
  printf '%s\n' "usage: $0 --endpoint ws://127.0.0.1:16352/v1/adapter --credential ADAPTER_CREDENTIAL --output /path/to/offbeat.js" >&2
  exit 2
}

endpoint=
credential=
output=

while [ "$#" -gt 0 ]; do
  case "$1" in
    --endpoint) endpoint=${2-}; shift 2 ;;
    --credential) credential=${2-}; shift 2 ;;
    --output) output=${2-}; shift 2 ;;
    *) usage ;;
  esac
done

[ -n "$endpoint" ] && [ -n "$credential" ] && [ -n "$output" ] || usage

case "$endpoint" in
  *[!A-Za-z0-9:/._\[\]-]* | '') printf '%s\n' "endpoint contains unsupported characters" >&2; exit 2 ;;
esac
case "$credential" in
  *[!A-Za-z0-9+/=_-]* | '') printf '%s\n' "credential must be base64-like text without whitespace" >&2; exit 2 ;;
esac

umask 077
mkdir -p "$(dirname "$output")"
{
  printf 'globalThis.OffbeatM2Config = Object.freeze({ endpoint: "%s", credential: "%s" });\n' "$endpoint" "$credential"
  cat "$(dirname "$0")/offbeat.js"
} > "$output"
chmod 600 "$output"
