#Requires -Version 5.1
# Build the Windows CLI with its embedded one-shot statusline renderer.
[CmdletBinding()]
param([string]$Output = (Join-Path $PSScriptRoot '..\submon.exe'))
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$Output = [IO.Path]::GetFullPath($Output)
Push-Location $root
try {
    if ((& go env GOOS) -ne 'windows') { throw 'This build script targets Windows.' }
    & go build -buildvcs=false -trimpath -ldflags '-s -w -H windowsgui' -o internal/statuslinebundle/assets/submon.exe ./apps/statusline
    if ($LASTEXITCODE -ne 0) { throw 'Standalone statusline build failed.' }
    & go build -buildvcs=false -trimpath -ldflags '-s -w' -o $Output .
    if ($LASTEXITCODE -ne 0) { throw 'CLI build failed.' }
    Write-Host "Built: $Output"
} finally { Pop-Location }
