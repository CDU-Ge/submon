#Requires -Version 5.1
[CmdletBinding()]
param([Parameter(Mandatory=$true)][string]$Submon, [string]$LogDirectory)
$ErrorActionPreference = 'Stop'
$Submon = (Get-Item -LiteralPath $Submon).FullName
if (-not $LogDirectory) { $LogDirectory = Join-Path (Get-Location).Path ('statusline-logs\install-test-' + [Guid]::NewGuid().ToString('N')) }
$null = New-Item -ItemType Directory -Path $LogDirectory -Force
$logRoot = (Get-Item -LiteralPath $LogDirectory).FullName
$utf8 = [Text.UTF8Encoding]::new($false)
$names = @('USERPROFILE','AppData','LocalAppData','SUBMON_LAYOUT','SUBMON_STATUSLINE_TIMING','ANTHROPIC_BASE_URL','NO_COLOR','TERM')
$saved = @{}
foreach ($name in $names) { $saved[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
$records = [IO.StreamWriter]::new((Join-Path $logRoot 'invocations.jsonl'),$false,$utf8)
$records.AutoFlush = $true
function Invoke-Captured([string]$Binary,[string]$Arguments,[string]$InputData) {
    $p = [Diagnostics.Process]::new()
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName=$Binary; $info.Arguments=$Arguments
    $info.UseShellExecute=$false; $info.CreateNoWindow=$true
    $info.RedirectStandardInput=$true; $info.RedirectStandardOutput=$true; $info.RedirectStandardError=$true
    $info.StandardOutputEncoding=$utf8; $info.StandardErrorEncoding=$utf8
    $p.StartInfo=$info
    try {
        $null=$p.Start()
        $out=$p.StandardOutput.ReadToEndAsync();$err=$p.StandardError.ReadToEndAsync()
        $data=$utf8.GetBytes($InputData)
        $p.StandardInput.BaseStream.Write($data,0,$data.Length);$p.StandardInput.Close()
        if(-not $p.WaitForExit(5000)){$p.Kill();$p.WaitForExit();throw 'Test process timed out.'}
        if(-not $out.Wait(2000) -or -not $err.Wait(2000)){throw 'Output pipe remained open.'}
        $result=[pscustomobject]@{binary=$Binary;arguments=$Arguments;exit_code=$p.ExitCode;stdout=$out.Result;stderr=$err.Result}
        $records.WriteLine(($result|ConvertTo-Json -Compress))
        return $result
    } finally { $p.Dispose() }
}
try {
    $env:USERPROFILE=Join-Path $logRoot 'user home'
    $env:AppData=Join-Path $env:USERPROFILE 'AppData\Roaming'
    $env:LocalAppData=Join-Path $env:USERPROFILE 'AppData\Local'
    $env:SUBMON_STATUSLINE_TIMING='0';$env:ANTHROPIC_BASE_URL='';$env:NO_COLOR='';$env:TERM='';$env:SUBMON_LAYOUT=''
    $settingsPath=Join-Path $env:USERPROFILE '.claude\settings.json'
    $helper=Join-Path $env:LocalAppData 'submon\statusline\submon.exe'
    $null=New-Item -ItemType Directory -Path (Split-Path $settingsPath) -Force
    $settings=@{statusLine=@{type='command';command='"'+$Submon.Replace('\','/')+'" statusline --provider kimi --show-provider --no-color';padding=4;refreshInterval=45};custom='preserve me'}
    [IO.File]::WriteAllText($settingsPath,($settings|ConvertTo-Json -Depth 6),$utf8)
    $before=(Get-FileHash $settingsPath).Hash
    $preview=Invoke-Captured $Submon 'install --print-claude' ''
    if($preview.exit_code -ne 0 -or (Test-Path $helper) -or (Get-FileHash $settingsPath).Hash -ne $before){throw 'Preview mutated data or failed.'}
    $install=Invoke-Captured $Submon 'install --claude-only' ''
    if($install.exit_code -ne 0 -or -not(Test-Path $helper)){throw "Install failed: $($install.stderr)"}
    $actual=[IO.File]::ReadAllText($settingsPath)|ConvertFrom-Json
    $command=$actual.statusLine.command
    if(-not $command.Contains($helper.Replace('\','/')) -or $command.Contains('--provider') -or $command.Contains('--show-provider') -or -not $command.Contains('--no-color')){throw "Migration failed: $command"}
    if($actual.custom -ne 'preserve me' -or $actual.statusLine.padding -ne 4 -or $actual.statusLine.refreshInterval -ne 45){throw 'Install lost custom settings.'}
    $helperTime=(Get-Item $helper).LastWriteTimeUtc
    $settingsTime=(Get-Item $settingsPath).LastWriteTimeUtc
    $again=Invoke-Captured $Submon 'install --claude-only' ''
    if($again.exit_code -ne 0 -or (Get-Item $helper).LastWriteTimeUtc -ne $helperTime -or (Get-Item $settingsPath).LastWriteTimeUtc -ne $settingsTime){throw 'Repeated install rewrote unchanged data.'}
    $payload='{"session_id":"standalone-parity","model":{"display_name":"Claude Sonnet"},"context_window":{"context_window_size":200000,"used_percentage":42},"fast_mode":true,"effort":{"level":"high"}}'
    foreach($layout in @('a','b','c')) {
        $env:SUBMON_LAYOUT=$layout
        foreach($arguments in @('statusline','statusline --no-color')) {
            $main=Invoke-Captured $Submon $arguments $payload
            $lite=Invoke-Captured $helper $arguments $payload
            if($main.exit_code -ne 0 -or $lite.exit_code -ne 0 -or $main.stdout -ne $lite.stdout){throw "Output mismatch: $layout / $arguments"}
        }
    }
    $rejected=Invoke-Captured $helper 'statusline --provider kimi' $payload
    if($rejected.exit_code -eq 0){throw 'Removed provider option accepted.'}
    $foreign='echo custom-statusline'
    $settings=@{statusLine=@{command=$foreign};custom='preserve me'}
    [IO.File]::WriteAllText($settingsPath,($settings|ConvertTo-Json -Depth 6),$utf8)
    $hash=(Get-FileHash $settingsPath).Hash
    $result=Invoke-Captured $Submon 'install --claude-only' ''
    if($result.exit_code -ne 0 -or (Get-FileHash $settingsPath).Hash -ne $hash){throw 'Foreign statusline overwritten.'}
    [IO.File]::WriteAllText((Join-Path $logRoot 'summary.json'),(@{passed=$true;helper=$helper;helper_bytes=(Get-Item $helper).Length;helper_sha256=(Get-FileHash $helper).Hash}|ConvertTo-Json),$utf8)
    Write-Host "Install, preview, migration, idempotence, layout/color parity and foreign settings passed. Logs: $logRoot"
} finally {
    $records.Dispose()
    foreach($name in $names){[Environment]::SetEnvironmentVariable($name,$saved[$name],'Process')}
}
