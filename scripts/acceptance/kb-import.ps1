# Offline acceptance test; all repository writes target a temporary fixture.
$ErrorActionPreference = 'Stop'
$sourceRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('tianwu-kb-import-' + [Guid]::NewGuid().ToString('N'))
$fixture = Join-Path $testRoot 'repository'
$entries = Join-Path $fixture 'internal/kb/assets/entries'
New-Item -ItemType Directory -Path $entries -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $fixture 'go.mod'), 'module github.com/kksty/tianwu')
$old = '{"id":"world-old","title":"old","description":"old","detail":"old","source":"spec"}'
[IO.File]::WriteAllText((Join-Path $entries 'world-old.json'), $old)
[IO.File]::WriteAllText((Join-Path $fixture 'operator.txt'), 'keep')
function New-TestPackage([string]$Name, [string[]]$Deleted) {
    $archivePath = Join-Path $testRoot $Name
    $archive = [IO.Compression.ZipFile]::Open($archivePath, [IO.Compression.ZipArchiveMode]::Create)
    try {
        $content = @{
            'internal/kb/assets/entries/world-new.json' = '{"id":"world-new","title":"new","description":"new","detail":"通用事实","source":"spec"}'
            'kb-export-manifest.json' = (@{format=1;entries=@('world-new');deleted_ids=@($Deleted)} | ConvertTo-Json -Depth 5)
        }
        foreach ($name in $content.Keys) {
            $writer = [IO.StreamWriter]::new($archive.CreateEntry($name).Open(), [Text.UTF8Encoding]::new($false))
            try { $writer.Write($content[$name]) } finally { $writer.Dispose() }
        }
    } finally { $archive.Dispose() }
    return $archivePath
}
$validPackage = New-TestPackage 'valid.zip' @('world-old')
$importer = Join-Path $sourceRoot 'scripts/import-kb.ps1'
& $importer -Archive $validPackage -RepositoryRoot $fixture -WhatIf
if (Test-Path -LiteralPath (Join-Path $entries 'world-new.json')) { throw 'WhatIf wrote content' }
& $importer -Archive $validPackage -RepositoryRoot $fixture
if (-not (Test-Path -LiteralPath (Join-Path $entries 'world-old.json'))) { throw 'Deletion applied without explicit switch' }
if (-not (Test-Path -LiteralPath (Join-Path $entries 'world-new.json'))) { throw 'Knowledge not imported' }
& $importer -Archive $validPackage -RepositoryRoot $fixture -ApplyDeletions
if (Test-Path -LiteralPath (Join-Path $entries 'world-old.json')) { throw 'Explicit deletion not applied' }
if ((Get-Content -LiteralPath (Join-Path $fixture 'operator.txt') -Raw) -ne 'keep') { throw 'Unrelated file changed' }
$before = Get-Content -LiteralPath (Join-Path $entries 'world-new.json') -Raw
$invalidPackage = New-TestPackage 'invalid.zip' @('../outside')
$rejected = $false
try { & $importer -Archive $invalidPackage -RepositoryRoot $fixture -ApplyDeletions } catch { $rejected = $true }
if (-not $rejected) { throw 'Invalid deletion path accepted' }
if ((Get-Content -LiteralPath (Join-Path $entries 'world-new.json') -Raw) -ne $before) { throw 'Invalid package partially applied' }
Write-Host 'PASS: preview, import, explicit deletions, path validation and unrelated-file preservation.'
