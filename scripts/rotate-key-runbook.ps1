# rotate-key-runbook.ps1 - operator runbook for the one-off credential cleanup.
#
# Chains the four safe steps around `cmd/redact-logs` and keeps the destructive
# one gated:
#
#   1. dry-run                     (report what would be redacted)
#   2. --apply --backup            (rewrite journals, keep the originals as .bak)
#   3. rescan                      (must report changed=0)
#   4. rotate/revoke the provider key  <-- human action; redaction is NOT rotation
#   5. --apply --delete-backups    (only allowed when step 3 saw 0 hits)
#
# DEFAULT IS READ-ONLY: without -Execute the script only prints the steps and
# the exact commands; nothing runs. With -Execute it runs steps 1-5 and stops at
# the first failure. -SkipDelete runs only 1-4, so an operator can inspect and
# delete the .bak copies later, after the provider-side rotation.
#
# Usage:
#   .\scripts\rotate-key-runbook.ps1 -Data .build/r1/data-e2 `
#       -HarnessConfig .build/r1/cfg-e/cyber.yaml
#   .\scripts\rotate-key-runbook.ps1 -Data .build/r1/data-e2 `
#       -HarnessConfig .build/r1/cfg-e/cyber.yaml -Tool .build/r1/redact-logs.exe -Execute
#
# The tool lives in cmd/redact-logs; when -Tool is omitted the runbook uses
# `go run ./cmd/redact-logs` from the repository root.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Data,

    [string]$HarnessConfig = $env:TW_HARNESS_CONFIG,

    [string[]]$Secret = @(),

    [string]$Tool = "",

    [switch]$Execute,

    [switch]$SkipDelete
)

$ErrorActionPreference = "Stop"

$repo = Split-Path -Parent $PSScriptRoot
$toolArgs = @()
if ($Tool -ne "") {
    $toolArgs += $Tool
} else {
    $toolArgs += "go"
    $toolArgs += "run"
    $toolArgs += "./cmd/redact-logs"
}

$baseArgs = @("--data", $Data)
if ($HarnessConfig -ne "" -and $null -ne $HarnessConfig) {
    $baseArgs += @("--harness-config", $HarnessConfig)
}
foreach ($value in $Secret) {
    if ($value -ne "") {
        $baseArgs += @("--secret", $value)
    }
}

function Format-Command {
    param([string[]]$CommandArgs)
    $quoted = @()
    foreach ($arg in $CommandArgs) {
        if ($arg -match "[\s`"']") {
            $quoted += "'" + ($arg -replace "'", "''") + "'"
        } else {
            $quoted += $arg
        }
    }
    return ($quoted -join " ")
}

function Show-Step {
    param(
        [int]$Number,
        [string]$Title,
        [string]$Why,
        [string[]]$CommandArgs
    )
    Write-Host ""
    Write-Host ("[{0}/5] {1}" -f $Number, $Title) -ForegroundColor Cyan
    Write-Host ("       {0}" -f $Why) -ForegroundColor DarkGray
    Write-Host ("       " + (Format-Command $CommandArgs))
}

function Invoke-Step {
    param(
        [int]$Number,
        [string]$Title,
        [string[]]$CommandArgs
    )
    Write-Host ""
    Write-Host ("[{0}/5] {1}" -f $Number, $Title) -ForegroundColor Cyan
    Write-Host ("       " + (Format-Command $CommandArgs))
    Push-Location $repo
    try {
        & $CommandArgs[0] $CommandArgs[1..($CommandArgs.Length - 1)]
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($code -ne 0) {
        Write-Host ("step {0} failed with exit code {1}: stopping." -f $Number, $code) -ForegroundColor Red
        exit $code
    }
}

$dryRunArgs = $toolArgs + $baseArgs
$applyArgs = $toolArgs + $baseArgs + @("--apply", "--backup")
$deleteArgs = $toolArgs + $baseArgs + @("--apply", "--delete-backups")

Write-Host "rotate-key runbook"
Write-Host ("  data    : {0}" -f $Data)
Write-Host ("  harness : {0}" -f $HarnessConfig)
Write-Host ("  tool    : {0}" -f (Format-Command $toolArgs))
if (-not $Execute) {
    Write-Host ""
    Write-Host "READ-ONLY: no command is executed (default). Re-run with -Execute (and -SkipDelete if you want to inspect first)." -ForegroundColor Yellow
}

Show-Step -Number 1 -Title "dry-run" `
    -Why "report per-file replacements; writes nothing" `
    -CommandArgs $dryRunArgs

Show-Step -Number 2 -Title "apply + backup" `
    -Why "rewrite the journals; the originals stay as <file>.bak (never overwritten)" `
    -CommandArgs $applyArgs

Show-Step -Number 3 -Title "rescan (must be 0 hits)" `
    -Why "a clean re-scan is the precondition for touching .bak; 'changed=0' means journals are clean" `
    -CommandArgs $dryRunArgs

Write-Host ""
Write-Host "[4/5] rotate / revoke the provider key (human action)" -ForegroundColor Cyan
Write-Host "       Redaction removes copies of the credential from logs; it does NOT revoke it." -ForegroundColor DarkGray
Write-Host "       Provider console: rotate/revoke the leaked key, then update the profile's api_key / api_keys." -ForegroundColor DarkGray
Write-Host "       Do this BEFORE deleting .bak; the backups still contain the original value." -ForegroundColor DarkGray

if ($SkipDelete) {
    Write-Host ""
    Write-Host "[5/5] skipped (-SkipDelete): .bak kept for inspection/rollback." -ForegroundColor Yellow
    Write-Host "      Delete them later with: " -NoNewline
    Write-Host (Format-Command $deleteArgs)
} else {
    Show-Step -Number 5 -Title "delete .bak (refused unless step 3 saw 0 hits)" `
        -Why "the tool re-verifies *.jsonl itself; residual hits => non-zero exit and no deletion" `
        -CommandArgs $deleteArgs
}

if (-not $Execute) {
    Write-Host ""
    Write-Host "Nothing was executed. Add -Execute to run steps 1-3 (+5 unless -SkipDelete)." -ForegroundColor Yellow
    exit 0
}

Write-Host ""
Write-Host "EXECUTING runbook (-Execute)" -ForegroundColor Yellow
Invoke-Step -Number 1 -Title "dry-run" -CommandArgs $dryRunArgs
Invoke-Step -Number 2 -Title "apply + backup" -CommandArgs $applyArgs
Invoke-Step -Number 3 -Title "rescan" -CommandArgs $dryRunArgs
Write-Host ""
Write-Host "[4/5] ROTATE THE KEY NOW if you have not already: the provider credential is still valid." -ForegroundColor Yellow
if ($SkipDelete) {
    Write-Host "[5/5] skipped (-SkipDelete): .bak kept." -ForegroundColor Yellow
    exit 0
}
Write-Host "[5/5] deleting .bak (tool verifies 0 hits first; residual => non-zero exit)" -ForegroundColor Yellow
Invoke-Step -Number 5 -Title "delete .bak" -CommandArgs $deleteArgs
Write-Host ""
Write-Host "runbook done." -ForegroundColor Green
