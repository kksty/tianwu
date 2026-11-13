# 从仓库外的 harness 源码构建执行器
#
# 天吴本仓库不内置 cyber-harness 源码。先单独 clone/fetch
# `kksty/cyber-harness` 的 `tianwu/fixes` 分支，再用 -HarnessDir 指过来；
# 不传时默认 .build/harness-src（已被 .gitignore 忽略）。
#
# 只构建 agent 需要的部分：full edition + sqlite 标签，带 emptytemplates/noembed
# （模板与指纹资源不嵌进二进制）。天吴从不启动 harness 的 web 前端，所以这里也
# 不需要先构建前端。
#
# 需要 Go 1.26+；首次构建会下载依赖（约 350 个模块）。
#
# 用法：
#   pwsh -File scripts/build-harness.ps1 -HarnessDir ..\cyber-harness
#   pwsh -File scripts/build-harness.ps1 -HarnessDir .build\harness-src -Out .\bin\aiscan-full.exe

param(
    [string]$HarnessDir = "",
    [string]$Out = "./bin/aiscan-full.exe",
    [string]$Proxy = "https://goproxy.cn,direct"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) { throw "找不到 go：请安装 Go 1.26+ 并确保在 PATH 中（新开一个终端再试）。" }

if (-not $HarnessDir) { $HarnessDir = Join-Path $root ".build/harness-src" }
elseif (-not [System.IO.Path]::IsPathRooted($HarnessDir)) { $HarnessDir = Join-Path $root $HarnessDir }

$harnessPath = $HarnessDir
if (-not (Test-Path -LiteralPath $harnessPath)) {
    throw "找不到 harness 源码：$HarnessDir`n本仓库不内置 harness/。先单独 clone：`n  git clone --branch tianwu/fixes https://github.com/kksty/cyber-harness.git <目录>`n再用 -HarnessDir 指向该目录。"
}
$harnessPath = (Resolve-Path -LiteralPath $harnessPath).Path
if (-not (Test-Path (Join-Path $harnessPath "go.mod"))) {
    throw "$harnessPath 不像 harness 源码（没有 go.mod）。请 checkout kksty/cyber-harness 的 tianwu/fixes 分支。"
}

$env:GOPROXY = $Proxy
if (-not [System.IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $root $Out }
New-Item -ItemType Directory -Path (Split-Path -Parent $Out) -Force | Out-Null

$tags = "forceposix emptytemplates noembed osusergo full sqlite"
# Windows' native resolver does not require CGO. Forcing netgo bypasses the
# system DNS cache/NRPT and can fail while ordinary Windows clients work.
$targetOS = $env:GOOS
if (-not $targetOS) { $targetOS = (& go env GOOS).Trim() }
if ($targetOS -ne "windows") { $tags += " netgo" }
Write-Host "building aiscan-full from $harnessPath"
Push-Location $harnessPath
try {
    go build -tags $tags -o $Out ./cmd/aiscan
} finally {
    Pop-Location
}
Write-Host "ok: $Out"
& $Out --version
