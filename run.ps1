#!/usr/bin/env pwsh
# Run Tianwu from source on Windows (no build step required).
#   ./run.ps1                      # default addr/data
#   ./run.ps1 -Aiscan "C:\path\aiscan-full.exe"
#   ./run.ps1 -Project acme-web    # open one project
param(
  [string]$Addr = "127.0.0.1:8080",
  [string]$Data = "./data",
  [string]$Project = "",
  [string]$Aiscan = ""
)

$ErrorActionPreference = "Stop"
$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
  $guess = "C:\Program Files\Go\bin"
  if (Test-Path (Join-Path $guess "go.exe")) { $env:Path = "$guess;$env:Path" }
  else { throw "go not found. Install Go 1.22+ (winget install GoLang.Go)." }
}

$args = @("run", "./cmd/tw", "--addr", $Addr, "--data", $Data)
if ($Project -ne "") { $args += @("--project", $Project) }
if ($Aiscan -ne "") { $args += @("--aiscan", $Aiscan) }

Write-Host "tianwu -> http://$Addr" -ForegroundColor Red
& go @args
