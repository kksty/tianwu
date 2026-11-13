#!/usr/bin/env sh
# 从仓库外的 harness 源码构建执行器（Linux/macOS）。
#
# 天吴本仓库不内置 cyber-harness 源码。先单独 clone/fetch
# `kksty/cyber-harness` 的 `tianwu/fixes`，用 HARNESS_DIR 指过来；
# 不传时默认 .build/harness-src（已被 .gitignore 忽略）。
#
# 只构建 agent 需要的部分：full edition + sqlite，带 emptytemplates/noembed
# （模板与指纹资源不嵌进二进制），不做前端构建——天吴从不启动 harness 的 web。
#
# 用法：
#   HARNESS_DIR=../cyber-harness ./scripts/build-harness.sh
#   HARNESS_DIR=.build/harness-src ./scripts/build-harness.sh ./bin/aiscan-full
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
harness="${HARNESS_DIR:-$root/.build/harness-src}"

command -v go >/dev/null 2>&1 || {
  echo "找不到 go：请安装 Go 1.26+ 并确保在 PATH 中。" >&2
  exit 1
}

[ -f "$harness/go.mod" ] || {
  echo "找不到 $harness/go.mod：本仓库不内置 harness/" >&2
  echo "先单独 clone kksty/cyber-harness 的 tianwu/fixes，再用 HARNESS_DIR 指过去：" >&2
  echo "  git clone --branch tianwu/fixes https://github.com/kksty/cyber-harness.git .build/harness-src" >&2
  echo "  HARNESS_DIR=.build/harness-src ./scripts/build-harness.sh" >&2
  exit 1
}

out="${1:-$root/bin/aiscan-full}"
mkdir -p "$(dirname "$out")"

cd "$harness"
tags="forceposix emptytemplates noembed osusergo full sqlite"
# Cross-compiling for Windows must retain its native system DNS resolver too.
target_os="${GOOS:-$(go env GOOS)}"
if [ "$target_os" != "windows" ]; then tags="$tags netgo"; fi
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
  go build -tags "$tags" -o "$out" ./cmd/aiscan
echo "ok: $out"
"$out" --version
