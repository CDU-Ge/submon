#Requires -Version 5.1
<#
Repeatedly pipe mock Claude Code JSON into submon statusline and retain logs.
Compatible with Windows PowerShell 5.1 and PowerShell 7. No admin rights needed.
The observer samples independently, so hung calls remain observable.
#>
[CmdletBinding()]
param(
    [string]$Submon = (Join-Path $PSScriptRoot '..\submon.exe'),
    [string]$Workspace = (Get-Location).Path,
    [string]$MockDataFile,
    [string]$LogDirectory,
    [ValidateRange(0, 2147483647)][int]$Iterations = 10000,
    [ValidateRange(1, 86400)][int]$DurationSeconds = 600,
    [ValidateRange(0, 60000)][int]$DelayMilliseconds = 10,
    [ValidateRange(20, 10000)][int]$SampleMilliseconds = 100,
    [ValidateRange(100, 60000)][int]$CallTimeoutMilliseconds = 3000,
    [ValidateRange(1, 65536)][int]$MaxGrowthMB = 512,
    [ValidateRange(1, 10000)][int]$MaxExtraProcesses = 64,
    [switch]$NoProcessSampling,
    [switch]$CaptureInvocationMemory,
    [switch]$TraceStages
)

$ErrorActionPreference = 'Stop'
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw '此脚本需要 Windows。'
}
# Resolve literal files first. Get-Command can yield multiple application
# candidates; passing their Source array to FileName joins paths with spaces.
$binary = if (Test-Path -LiteralPath $Submon -PathType Leaf) {
    (Get-Item -LiteralPath $Submon -Force -ErrorAction Stop).FullName
} else {
    $application = Get-Command -Name $Submon -CommandType Application -ErrorAction Stop | Select-Object -First 1
    (Get-Item -LiteralPath $application.Path -Force -ErrorAction Stop).FullName
}
$workingDirectory = (Resolve-Path -LiteralPath $Workspace).Path
if (-not $LogDirectory) {
    $LogDirectory = Join-Path (Get-Location).Path ('statusline-logs\' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
}
$null = New-Item -ItemType Directory -Path $LogDirectory -Force
$logRoot = (Resolve-Path -LiteralPath $LogDirectory).Path
$utf8 = New-Object System.Text.UTF8Encoding($false)
if ($MockDataFile) {
    $payload = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $MockDataFile).Path)
    $null = $payload | ConvertFrom-Json # Fail before starting if JSON is malformed.
} else {
    $payload = @{
        session_id = 'submon-windows-stress'
        workspace = @{ current_dir = $workingDirectory }
        model = @{ id = 'claude-sonnet'; display_name = 'Claude Sonnet' }
        context_window = @{ context_window_size = 200000; used_percentage = 42; total_input_tokens = 84000 }
    } | ConvertTo-Json -Depth 5 -Compress
}
[IO.File]::WriteAllText((Join-Path $logRoot 'mock-data.json'), $payload, $utf8)
$names = @([IO.Path]::GetFileNameWithoutExtension($binary), 'git', 'OpenConsole', 'conhost', 'powershell', 'pwsh') | Select-Object -Unique
$metadata = @{
    started_at_utc = [DateTime]::UtcNow.ToString('o'); binary = $binary
    binary_sha256 = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash
    workspace = $workingDirectory; powershell = $PSVersionTable.PSVersion.ToString()
    os = [Environment]::OSVersion.VersionString; observer_host_pid = $PID
    iterations = $Iterations; duration_seconds = $DurationSeconds
    delay_ms = $DelayMilliseconds; sample_ms = $SampleMilliseconds
    call_timeout_ms = $CallTimeoutMilliseconds; max_growth_mb = $MaxGrowthMB
    max_extra_processes = $MaxExtraProcesses; tracked_names = @($names)
    process_sampling = -not $NoProcessSampling; capture_invocation_memory = [bool]$CaptureInvocationMemory
    trace_stages = [bool]$TraceStages; timing_schema = 2
    # Log only routing information, never tokens or the full environment.
    anthropic_base_url = $env:ANTHROPIC_BASE_URL
    measurement = 'Global named-process samples; counts include existing unrelated processes. Sub-100ms processes can be missed. Private bytes and working sets are distinct; summed working sets include shared pages.'
}
[IO.File]::WriteAllText((Join-Path $logRoot 'metadata.json'), ($metadata | ConvertTo-Json -Depth 5), $utf8)
$state = [hashtable]::Synchronized(@{ Stop = $false; Ready = $false; Guard = ''; Completed = 0; Error = ''; Samples = 0 })
$observer = $null
$async = $null
if ($NoProcessSampling) {
    $state.Ready = $true
} else {
$observer = [PowerShell]::Create()
$null = $observer.AddScript({
    param($Shared, $Names, $Root, $Interval, $GrowthMB, $ExtraProcesses)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    $writer = New-Object IO.StreamWriter((Join-Path $Root 'process-samples.jsonl'), $false, $encoding)
    $writer.AutoFlush = $true
    $baselineBytes = $null
    $baselineCount = 0
    $baselineGroups = @{}
    $peakBytes = 0L
    $peakCount = 0
    $peakGroups = @{}
    $lastGroups = @{}
    try {
        while (-not $Shared.Stop) {
            $items = @()
            # Enumerate through .NET instead of Get-Process -Name: absent names
            # would accumulate ErrorRecords in the runspace during long tests.
            foreach ($process in [Diagnostics.Process]::GetProcesses()) {
                try {
                    if ($Names -notcontains $process.ProcessName) { continue }
                    $items += [pscustomobject]@{
                        pid = $process.Id; name = $process.ProcessName
                        started_at_utc = $process.StartTime.ToUniversalTime().ToString('o')
                        private_bytes = $process.PrivateMemorySize64
                        working_set_bytes = $process.WorkingSet64
                        handles = $process.HandleCount; cpu_seconds = $process.TotalProcessorTime.TotalSeconds
                    }
                } catch { } finally { $process.Dispose() } # Processes may exit between enumeration and read.
            }
            $groups = @{}
            foreach ($name in $Names) {
                $matching = @($items | Where-Object { $_.name -eq $name })
                $private = 0L; $working = 0L
                foreach ($item in $matching) { $private += $item.private_bytes; $working += $item.working_set_bytes }
                $groups[$name] = @{ count = $matching.Count; private_bytes = $private; working_set_bytes = $working }
                if (-not $peakGroups.ContainsKey($name)) { $peakGroups[$name] = @{ count = 0; private_bytes = 0L } }
                $peakGroups[$name].count = [Math]::Max($peakGroups[$name].count, $matching.Count)
                $peakGroups[$name].private_bytes = [Math]::Max($peakGroups[$name].private_bytes, $private)
            }
            $total = 0L
            foreach ($item in $items) { $total += $item.private_bytes }
            if ($null -eq $baselineBytes) { $baselineBytes = $total; $baselineCount = $items.Count; $baselineGroups = $groups }
            $peakBytes = [Math]::Max($peakBytes, $total)
            $peakCount = [Math]::Max($peakCount, $items.Count)
            $lastGroups = $groups
            $record = @{ timestamp_utc = [DateTime]::UtcNow.ToString('o'); completed = $Shared.Completed; total_private_bytes = $total; count = $items.Count; groups = $groups; processes = $items }
            $writer.WriteLine(($record | ConvertTo-Json -Depth 7 -Compress))
            $Shared.Samples++
            $Shared.Ready = $true
            if (($total - $baselineBytes) -gt ($GrowthMB * 1MB)) { $Shared.Guard = '独占内存增长达到限制。' }
            if (($items.Count - $baselineCount) -gt $ExtraProcesses) { $Shared.Guard = '进程数量增长达到限制。' }
            Start-Sleep -Milliseconds $Interval
        }
    } catch {
        $Shared.Error = $_.ToString()
        [IO.File]::AppendAllText((Join-Path $Root 'observer-errors.log'), ($_.ToString() + [Environment]::NewLine), $encoding)
    } finally {
        $writer.Dispose()
        $summary = @{ samples = $Shared.Samples; baseline_private_bytes = $baselineBytes; peak_private_bytes = $peakBytes; baseline_count = $baselineCount; peak_count = $peakCount; baseline_groups = $baselineGroups; peak_groups = $peakGroups; final_groups = $lastGroups; guard = $Shared.Guard; observer_error = $Shared.Error }
        [IO.File]::WriteAllText((Join-Path $Root 'process-summary.json'), ($summary | ConvertTo-Json -Depth 6), $encoding)
    }
}).AddArgument($state).AddArgument(@($names)).AddArgument($logRoot).AddArgument($SampleMilliseconds).AddArgument($MaxGrowthMB).AddArgument($MaxExtraProcesses)
$async = $observer.BeginInvoke()
}
$invocations = New-Object IO.StreamWriter((Join-Path $logRoot 'invocations.jsonl'), $false, $utf8)
$invocations.AutoFlush = $true
$clock = [Diagnostics.Stopwatch]::StartNew()
$reason = '已完成'
$failed = 0
$active = $null
$loopClock = [Diagnostics.Stopwatch]::new()
$invocationTotal = 0.0; $metricsTotal = 0.0; $delayTotal = 0.0
$stageTotals = @{ run_ms = 0.0; config_ms = 0.0; stdin_ms = 0.0; render_ms = 0.0; outside_run_ms = 0.0 }
$stageCount = 0
$durations = New-Object double[] 10000 # Bounded ring, even for continuous runs.
Write-Host "日志目录: $logRoot"
try {
    while (-not $state.Ready -and -not $state.Error -and $clock.Elapsed.TotalSeconds -lt 5) { Start-Sleep -Milliseconds 20 }
    if (-not $state.Ready) { throw "采样器启动失败: $($state.Error)" }
    $setupSeconds = $clock.Elapsed.TotalSeconds
    $loopClock.Start()
    while (($Iterations -eq 0 -or $state.Completed -lt $Iterations) -and $clock.Elapsed.TotalSeconds -lt $DurationSeconds) {
        if ($state.Error) { throw "采样器已停止: $($state.Error)" }
        if ($state.Guard) { $reason = $state.Guard; break }
        # Equivalent to echo mock JSON | submon statusline. Direct pipes allow
        # a timeout and CreateNoWindow without creating a shell each iteration.
        $info = New-Object Diagnostics.ProcessStartInfo
        $info.FileName = $binary
        $info.Arguments = 'statusline'
        $info.WorkingDirectory = $workingDirectory
        $info.UseShellExecute = $false
        $info.CreateNoWindow = $true
        $info.RedirectStandardInput = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        $info.StandardOutputEncoding = $utf8
        $info.StandardErrorEncoding = $utf8
        if ($TraceStages) { $info.EnvironmentVariables['SUBMON_STATUSLINE_TIMING'] = '1' }
        $active = New-Object Diagnostics.Process
        $active.StartInfo = $info
        $timer = [Diagnostics.Stopwatch]::StartNew()
        $null = $active.Start()
        $processStartMS = $timer.Elapsed.TotalMilliseconds
        $childId = $active.Id
        # Drain both streams concurrently to avoid pipe deadlocks.
        $stdout = $active.StandardOutput.ReadToEndAsync()
        $stderr = $active.StandardError.ReadToEndAsync()
        # Write UTF-8 bytes directly; Windows PowerShell's StreamWriter may
        # otherwise use the local code page for paths containing Chinese.
        $inputBytes = $utf8.GetBytes($payload + [Environment]::NewLine)
        $active.StandardInput.BaseStream.Write($inputBytes, 0, $inputBytes.Length)
        $active.StandardInput.BaseStream.Flush()
        $active.StandardInput.Close()
        $inputEndMS = $timer.Elapsed.TotalMilliseconds
        $timedOut = -not $active.WaitForExit($CallTimeoutMilliseconds)
        if ($timedOut) { $active.Kill() }
        if (-not $active.WaitForExit(2000)) { throw "无法停止本次子进程 $childId" }
        $exitMS = $timer.Elapsed.TotalMilliseconds
        $null = $stdout.Wait(2000)
        $null = $stderr.Wait(2000)
        if (-not $stdout.IsCompleted -or -not $stderr.IsCompleted) { throw '子进程退出后输出管道仍未关闭。' }
        $timer.Stop() # End-to-end invocation excludes post-exit metrics/logging.
        $durationMS = $timer.Elapsed.TotalMilliseconds
        $metricsClock = [Diagnostics.Stopwatch]::StartNew()
        $peakWorking = $null; $peakPaged = $null
        if ($CaptureInvocationMemory) {
            try { $peakWorking = $active.PeakWorkingSet64; $peakPaged = $active.PeakPagedMemorySize64 } catch { }
        }
        $metricsClock.Stop()
        $metricsMS = $metricsClock.Elapsed.TotalMilliseconds
        $applicationStages = $null
        if ($TraceStages) {
            $match = [regex]::Match($stderr.Result, '(?m)^SUBMON_STATUSLINE_TIMING (.+)$')
            if ($match.Success) {
                $applicationStages = $match.Groups[1].Value | ConvertFrom-Json
                foreach ($name in @('run_ms', 'config_ms', 'stdin_ms', 'render_ms')) { $stageTotals[$name] += $applicationStages.$name }
                $stageTotals.outside_run_ms += $durationMS - $applicationStages.run_ms
                $stageCount++
            }
        }
        $record = @{
            application_stages = $applicationStages
            timestamp_utc = [DateTime]::UtcNow.ToString('o'); iteration = $state.Completed + 1
            pid = $childId; duration_ms = $durationMS
            process_start_ms = $processStartMS; input_ms = $inputEndMS - $processStartMS
            wait_exit_ms = $exitMS - $inputEndMS; drain_output_ms = $durationMS - $exitMS
            metrics_ms = $metricsMS
            exit_code = $active.ExitCode; timed_out = $timedOut
            peak_working_set_bytes = $peakWorking; peak_paged_bytes = $peakPaged
            stdout = $stdout.Result; stderr = $stderr.Result
        }
        $invocations.WriteLine(($record | ConvertTo-Json -Depth 4 -Compress))
        $durations[$state.Completed % $durations.Length] = $durationMS
        $invocationTotal += $durationMS; $metricsTotal += $metricsMS
        $state.Completed++
        if ($record.exit_code -ne 0 -or $timedOut) { $failed++ }
        $active.Dispose(); $active = $null
        if ($timedOut) { $reason = '本次调用超时,记录日志后停止。'; break }
        if ($failed -gt 0) { $reason = '本次调用失败,记录日志后停止。'; break }
        if ($DelayMilliseconds -gt 0) {
            $delayClock = [Diagnostics.Stopwatch]::StartNew()
            Start-Sleep -Milliseconds $DelayMilliseconds
            $delayTotal += $delayClock.Elapsed.TotalMilliseconds
        }
    }
    if ($clock.Elapsed.TotalSeconds -ge $DurationSeconds) { $reason = '已达到测试时长限制。' }
} catch {
    $reason = $_.ToString()
    [IO.File]::AppendAllText((Join-Path $logRoot 'errors.log'), ($reason + [Environment]::NewLine), $utf8)
    throw
} finally {
    $loopClock.Stop()
    if ($null -ne $active) {
        try { if (-not $active.HasExited) { $active.Kill(); $null = $active.WaitForExit(2000) } } catch { }
        $active.Dispose()
    }
    $invocations.Dispose()
    # Keep observation tail separate from invocation/loop latency.
    $tailClock = [Diagnostics.Stopwatch]::StartNew()
    if ($null -ne $observer) {
        Start-Sleep -Seconds 3
        $state.Stop = $true
        try { $null = $observer.EndInvoke($async) } finally { $observer.Dispose() }
    }
    $tailClock.Stop()
    $latency = $null
    if ($state.Completed -gt 0) {
        $count = [Math]::Min($state.Completed, $durations.Length)
        $values = @($durations[0..($count-1)] | Sort-Object)
        $latency = @{ samples = $count; population = 'last up to 10000 invocations'; mean_all = $invocationTotal / $state.Completed; p50 = $values[[Math]::Ceiling($count * 0.5)-1]; p95 = $values[[Math]::Ceiling($count * 0.95)-1]; max = $values[-1] }
    }
    $stageMeans = $null
    if ($stageCount -gt 0) {
        $stageMeans = @{}
        foreach ($name in $stageTotals.Keys) { $stageMeans[$name] = $stageTotals[$name] / $stageCount }
    }
    $summary = @{ application_stage_mean_ms = $stageMeans; traced_invocations = $stageCount
        setup_seconds = $setupSeconds; loop_seconds = $loopClock.Elapsed.TotalSeconds
        observation_tail_seconds = $tailClock.Elapsed.TotalSeconds
        invocation_total_ms = $invocationTotal; metrics_total_ms = $metricsTotal; delay_total_ms = $delayTotal
        latency_ms = $latency; process_sampling = -not $NoProcessSampling; ended_at_utc = [DateTime]::UtcNow.ToString('o'); completed = $state.Completed; failed = $failed; elapsed_seconds = $clock.Elapsed.TotalSeconds; reason = $reason; guard = $state.Guard; observer_error = $state.Error }
    [IO.File]::WriteAllText((Join-Path $logRoot 'run-summary.json'), ($summary | ConvertTo-Json), $utf8)
    Write-Host "已完成 $($state.Completed) 次,失败 $failed 次。停止原因: $reason"
    Write-Host ("调用循环 {0:N2}s,收尾采样 {1:N2}s" -f $loopClock.Elapsed.TotalSeconds, $tailClock.Elapsed.TotalSeconds)
    if ($null -ne $latency) { Write-Host ("单次调用 P50 {0:N2}ms, P95 {1:N2}ms" -f $latency.p50, $latency.p95) }
    if ($null -ne $stageMeans) {
        Write-Host ("程序内平均: 配置 {0:N2}ms, stdin {1:N2}ms,渲染 {2:N2}ms; RunE 之外 {3:N2}ms" -f $stageMeans.config_ms, $stageMeans.stdin_ms, $stageMeans.render_ms, $stageMeans.outside_run_ms)
    }
    Write-Host "日志已保留: $logRoot"
}
if ($failed -gt 0 -or $state.Guard -or $state.Error) { exit 1 }
