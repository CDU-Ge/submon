#Requires -Version 5.1
# Run with powershell/pwsh -NoProfile -File scripts/tests/statusline-stress-path.tests.ps1
$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path $PSScriptRoot '..\statusline-stress.ps1'
$tokens = $null; $errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
# Exercise the actual script's resolution assignment without its Windows-only loop.
$assignment = $ast.Find({ param($node)
    $node -is [System.Management.Automation.Language.AssignmentStatementAst] -and $node.Left.Extent.Text -eq '$binary'
}, $true)
if ($null -eq $assignment) { throw 'Missing executable resolution assignment.' }
$root = Join-Path ([IO.Path]::GetTempPath()) ('submon path test ' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path (Join-Path $root 'scripts') -Force
$first = Join-Path $root 'submon.exe'
$second = Join-Path $root 'another submon.exe'
[IO.File]::WriteAllText($first, 'fixture')
[IO.File]::WriteAllText($second, 'fixture')
$script:candidates = @([pscustomobject]@{ Source = $first; Path = $first }, [pscustomobject]@{ Source = $second; Path = $second })
function Get-Command { param($Name, $CommandType, $ErrorAction) $script:candidates }
Push-Location $root
try {
    foreach ($inputPath in @((Join-Path $root 'scripts\..\submon.exe'), $first, '.\submon.exe', 'submon-path-fixture')) {
        $Submon = $inputPath
        $binary = $null
        . ([scriptblock]::Create($assignment.Extent.Text))
        if ($binary -isnot [string] -or $binary -ne (Get-Item -LiteralPath $first).FullName) {
            throw "Expected one canonical executable path; got: $binary"
        }
    }
    'Default, relative, explicit/space-containing and duplicate PATH candidate cases passed.'
} finally {
    Pop-Location
    Remove-Item Function:\Get-Command -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $root -Recurse -Force
}
