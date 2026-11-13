# Import an explicitly exported world KB into repository content assets.
# No Git commands, compilation, service control or runtime-data writes.
[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory)][string]$Archive,
    [string]$RepositoryRoot = (Split-Path -Parent $PSScriptRoot),
    [switch]$ApplyDeletions
)

$ErrorActionPreference = 'Stop'
$repositoryPath = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$entryRoot = [IO.Path]::GetFullPath((Join-Path $repositoryPath 'internal/kb/assets/entries'))
if (-not (Test-Path -LiteralPath (Join-Path $repositoryPath 'go.mod') -PathType Leaf) -or
    -not (Test-Path -LiteralPath $entryRoot -PathType Container)) {
    throw '目标不是天吴源码仓库：缺少 go.mod 或 internal/kb/assets/entries。'
}
$archivePath = (Resolve-Path -LiteralPath $Archive).Path
$package = [IO.Compression.ZipFile]::OpenRead($archivePath)
$assets = @{}
$manifest = $null
try {
    foreach ($item in $package.Entries) {
        if ($item.FullName.EndsWith('/')) { continue }
        if ($item.FullName -eq 'KB_IMPORT.md') { continue }
        if ($item.FullName -eq 'kb-export-manifest.json') {
            if ($null -ne $manifest) { throw '导出清单重复。' }
            $reader = [IO.StreamReader]::new($item.Open())
            try { $manifest = $reader.ReadToEnd() | ConvertFrom-Json -AsHashtable } finally { $reader.Dispose() }
            continue
        }
        if ($item.FullName -cnotmatch '^internal/kb/assets/entries/([a-z0-9_-]{1,120})\.json$') {
            throw "导出包包含不允许的路径：$($item.FullName)"
        }
        $entryID = $Matches[1]
        if ($assets.ContainsKey($entryID)) { throw "知识 ID 重复：$entryID" }
        $reader = [IO.StreamReader]::new($item.Open())
        try { $text = $reader.ReadToEnd() } finally { $reader.Dispose() }
        $entry = $text | ConvertFrom-Json -AsHashtable
        if ($entry['id'] -cne $entryID -or $entry['deleted'] -or
            [string]::IsNullOrWhiteSpace($entry['title']) -or
            [string]::IsNullOrWhiteSpace($entry['description']) -or
            [string]::IsNullOrWhiteSpace($entry['detail'])) { throw "无效知识条目：$entryID" }
        $sourceRefs = @()
        if ($null -ne $entry['source_refs']) { $sourceRefs = @($entry['source_refs']) }
        if ([string]::IsNullOrWhiteSpace($entry['source']) -and $sourceRefs.Count -eq 0) { throw "知识缺少来源：$entryID" }
        foreach ($reference in $sourceRefs) { if ([string]::IsNullOrWhiteSpace($reference)) { throw "知识来源引用为空：$entryID" } }
        $assets[$entryID] = $text
    }
} finally { $package.Dispose() }

# Validate the entire package before any repository content is changed.
if ($null -eq $manifest -or $manifest['format'] -ne 1) { throw '导出清单缺失或版本不支持，请使用新版世界知识页重新导出。' }
$declared = @{}
foreach ($entryID in $manifest['entries']) {
    if ($entryID -cnotmatch '^[a-z0-9_-]{1,120}$' -or $declared.ContainsKey($entryID) -or -not $assets.ContainsKey($entryID)) { throw "清单条目无效：$entryID" }
    $declared[$entryID] = $true
}
if ($declared.Count -ne $assets.Count) { throw '条目文件与清单不一致。' }
$deleted = @{}
foreach ($entryID in $manifest['deleted_ids']) {
    if ($entryID -cnotmatch '^[a-z0-9_-]{1,120}$' -or $assets.ContainsKey($entryID) -or $deleted.ContainsKey($entryID)) { throw "删除清单无效：$entryID" }
    $deleted[$entryID] = $true
}

$utf8 = [Text.UTF8Encoding]::new($false)
$written = 0
$removed = 0
foreach ($entryID in ($assets.Keys | Sort-Object)) {
    $target = [IO.Path]::GetFullPath((Join-Path $entryRoot ($entryID + '.json')))
    if ([IO.Path]::GetDirectoryName($target) -ne $entryRoot) { throw '知识写入路径越界。' }
    if ($PSCmdlet.ShouldProcess($target, '写入内置知识 JSON')) {
        $temporary = Join-Path $entryRoot ('.kb-import-' + [Guid]::NewGuid().ToString('N') + '.tmp')
        try {
            [IO.File]::WriteAllText($temporary, $assets[$entryID], $utf8)
            Move-Item -LiteralPath $temporary -Destination $target -Force
        } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
        $written++
    }
}
if ($ApplyDeletions) {
    foreach ($entryID in ($deleted.Keys | Sort-Object)) {
        $target = [IO.Path]::GetFullPath((Join-Path $entryRoot ($entryID + '.json')))
        if ([IO.Path]::GetDirectoryName($target) -ne $entryRoot) { throw '知识删除路径越界。' }
        if ((Test-Path -LiteralPath $target -PathType Leaf) -and $PSCmdlet.ShouldProcess($target, '移除内置知识 JSON')) {
            Remove-Item -LiteralPath $target -Force
            $removed++
        }
    }
} elseif ($deleted.Count -gt 0) {
    Write-Host ('包中另有删除记录，当前未应用：' + (($deleted.Keys | Sort-Object) -join ', ') + '；需要移除仓库条目时加 -ApplyDeletions。')
}
Write-Host "已写入 $written 条，移除 $removed 条。仅修改内置 JSON；请自行检查 git diff、测试、提交和重新构建。"
