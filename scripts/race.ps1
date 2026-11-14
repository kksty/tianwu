# 本机跑 race 检测（需要 cgo / C 工具链）
#
# 本仓库不使用 GitHub Actions；提交前本地跑：
#   powershell -ExecutionPolicy Bypass -File scripts/race.ps1
# 有 make 的环境（git-bash/WSL）也可以 `make race`。
#
# 需要 Go 1.26+ 和 gcc。Windows 可用 winget 安装 MinGW-w64：
#   winget install BrechtSanders.WinLibs.POSIX.UCRT
# 装完重开终端，`gcc --version` 应输出版本。

$ErrorActionPreference = 'Stop'

# 与 Makefile 的 RACE_PKGS 保持一致。
$packages = @(
  './internal/api',
  './internal/lifecycle',
  './internal/verifier',
  './internal/store',
  './internal/decider',
  './internal/board',
  './internal/collab'
)

$cc = (& go env CC).Trim()
if (-not (Get-Command $cc -ErrorAction SilentlyContinue)) {
  Write-Error "race 检测需要 C 工具链（cgo）：找不到 '$cc'。请先安装 gcc/MinGW-w64，或改用 WSL/Docker。"
  exit 1
}

$env:CGO_ENABLED = '1'
& go test -race -count=1 -timeout 30m @packages
exit $LASTEXITCODE
