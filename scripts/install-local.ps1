# Install a locally built pair after the operator stops the console and host.
# No services are started. Runtime data and configuration are never touched.
[CmdletBinding(SupportsShouldProcess)]
param([string]$From = '.build/release')

$ErrorActionPreference = 'Stop'
$installRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
if (-not [IO.Path]::IsPathRooted($From)) { $From = Join-Path $installRoot $From }
$installSource = (Resolve-Path -LiteralPath $From).Path
$installBin = Join-Path $installRoot 'bin'
$installNames = @('tw.exe', 'aiscan-full.exe')
$installTargets = @($installNames | ForEach-Object { Join-Path $installBin $_ })
$installRunning = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -in $installTargets })
if ($installRunning.Count) {
    throw ('旧程序仍在运行，请在原终端 Ctrl+C 后重试。PID: ' + ($installRunning.ProcessId -join ', '))
}
foreach ($installName in $installNames) {
    if (-not (Test-Path -LiteralPath (Join-Path $installSource $installName) -PathType Leaf)) {
        throw "缺少候选程序：$installName"
    }
}
if ($installSource -eq $installBin) { throw '候选目录不能是安装目录 bin' }
if (-not $PSCmdlet.ShouldProcess($installBin, '替换 tw.exe 和 aiscan-full.exe，清理旧构建副本')) { return }

New-Item -ItemType Directory -Path $installBin -Force | Out-Null
foreach ($installName in $installNames) {
    $installCandidate = Join-Path $installSource $installName
    $installPending = Join-Path $installBin ($installName + '.new')
    Copy-Item -LiteralPath $installCandidate -Destination $installPending -Force
    if ((Get-FileHash -LiteralPath $installCandidate).Hash -ne (Get-FileHash -LiteralPath $installPending).Hash) {
        throw "候选程序校验失败：$installName"
    }
}
foreach ($installName in $installNames) {
    Move-Item -LiteralPath (Join-Path $installBin ($installName + '.new')) -Destination (Join-Path $installBin $installName) -Force
}
foreach ($installName in $installNames) {
    if ((Get-FileHash -LiteralPath (Join-Path $installSource $installName)).Hash -ne (Get-FileHash -LiteralPath (Join-Path $installBin $installName)).Hash) {
        throw "安装校验失败：$installName"
    }
}

# Only top-level application build files are eligible, never directories,
# toolchains, project data, or the release pair used for this installation.
$installBuild = Join-Path $installRoot '.build'
$installOld = @(Get-ChildItem -LiteralPath $installBuild -File | Where-Object { $_.Name -like 'tw*.exe' -or $_.Name -like 'aiscan-full*.exe' })
foreach ($installFile in $installOld) {
    if ([IO.Path]::GetFullPath($installFile.DirectoryName) -ne $installBuild) { throw '旧构建路径越界' }
    if ($installFile.DirectoryName -eq $installSource) { continue }
    Remove-Item -LiteralPath $installFile.FullName -Force
}
foreach ($installOldPath in @((Join-Path $installBin 'aiscan-full.exe.bak'), (Join-Path $installBuild 'harness/aiscan-full.exe'))) {
    if (Test-Path -LiteralPath $installOldPath -PathType Leaf) { Remove-Item -LiteralPath $installOldPath -Force }
}
Write-Host "已安装：$installBin（未启动服务）"
