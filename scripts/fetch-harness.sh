#!/usr/bin/env sh
# 下载官方打包好的 aiscan-full，放进 ./bin。
#
#   ./scripts/fetch-harness.sh <zip-url> [sha256]
#
# 资产命名以上游 release 为准；这里不猜 URL，只负责下载、校验、解包。
set -eu

[ $# -ge 1 ] || { echo "用法: $0 <zip-url> [sha256]" >&2; exit 1; }
url="$1"
sha="${2:-}"
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out="$root/bin/aiscan-full"
mkdir -p "$(dirname "$out")"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
zip="$tmp/harness.zip"

echo "downloading $url"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$zip"
else
  wget -q "$url" -O "$zip"
fi

if [ -n "$sha" ]; then
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$zip" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$zip" | awk '{print $1}')
  fi
  [ "$actual" = "$sha" ] || { echo "sha256 不匹配：期望 $sha，实际 $actual" >&2; exit 1; }
  echo "sha256 ok"
else
  echo "注意：没有给 sha256，下载内容未校验" >&2
fi

unzip -q "$zip" -d "$tmp"
bin=$(find "$tmp" -type f -name 'aiscan-full*' | head -n 1)
[ -n "$bin" ] || { echo "压缩包里没有 aiscan-full" >&2; exit 1; }
cp "$bin" "$out"
chmod +x "$out"
echo "ok: $out"
"$out" --version
