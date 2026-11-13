# 下载官方打包好的 aiscan-full，放进 ./bin（不解压整个源码、不需要 Go 工具链）。
#
# 用法：
#   pwsh -File scripts/fetch-harness.ps1 -Url "https://github.com/chainreactors/cyber-harness/releases/download/vX/aiscan-full_windows_amd64.zip"
#   pwsh -File scripts/fetch-harness.ps1 -Url "...zip" -Sha256 "<64 位十六进制>"
#
# 资产命名以上游 release 为准；这里不猜 URL，只负责下载、校验、解包、放到 bin/。

param(
    [Parameter(Mandatory = $true)][string]$Url,
    [string]$Sha256 = "",
    [string]$Out = "./bin/aiscan-full.exe"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$out = Join-Path $root $Out
New-Item -ItemType Directory -Path (Split-Path -Parent $out) -Force | Out-Null

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("harness-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
    $zip = Join-Path $tmp "harness.zip"
    Write-Host "downloading $Url"
    Invoke-WebRequest -Uri $Url -OutFile $zip -UseBasicParsing

    if ($Sha256) {
        $actual = (Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLower()
        if ($actual -ne $Sha256.ToLower()) {
            throw "sha256 不匹配：期望 $Sha256，实际 $actual"
        }
        Write-Host "sha256 ok"
    } else {
        Write-Warning "没有给 -Sha256：下载内容未校验，生产环境请务必提供"
    }

    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $bin = Get-ChildItem -Path $tmp -Recurse -File -Filter "aiscan-full*" |
        Where-Object { $_.Extension -eq ".exe" -or $_.Extension -eq "" } |
        Select-Object -First 1
    if (-not $bin) { throw "压缩包里没有 aiscan-full 可执行文件" }

    Copy-Item $bin.FullName $out -Force
    Write-Host "ok: $out"
    & $out --version
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
