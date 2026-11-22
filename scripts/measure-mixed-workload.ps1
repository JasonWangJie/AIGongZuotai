[CmdletBinding()]
param(
    [ValidateRange(1, 10)]
    [int]$Runs = 3
)

$ErrorActionPreference = 'Stop'
if ([Environment]::OSVersion.Platform -ne 'Win32NT') {
    throw 'The working-set sampler requires Windows. Run TestPostgresMixedWorkspaceWorkload directly for portable Go/PG measurements.'
}
$database = $null
if (-not [Uri]::TryCreate($env:TEST_DATABASE_URL, [UriKind]::Absolute, [ref]$database) -or
    $database.Scheme -notin @('postgres', 'postgresql') -or -not $database.IsLoopback) {
    throw 'Set TEST_DATABASE_URL to an isolated loopback PostgreSQL test database.'
}
$repo = Split-Path -Parent $PSScriptRoot
$out = Join-Path $repo ('.runtime/mixed-workload-' + [Guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($out)
$binary = Join-Path $out 'studio-mixed.test.exe'
$previousOptIn = $env:MIXED_WORKLOAD_TEST
$previousReport = $env:MIXED_WORKLOAD_REPORT
Push-Location $repo
try {
    & go test -c -o $binary ./internal/studio
    if ($LASTEXITCODE -ne 0) { throw 'Could not compile the workload fixture.' }
    $digest = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
    $env:MIXED_WORKLOAD_TEST = '1'
    $results = foreach ($i in 1..$Runs) {
        $env:MIXED_WORKLOAD_REPORT = Join-Path $out "trial-$i.json"
        $runArgs = @{
            FilePath = $binary
            ArgumentList = @('-test.run=^TestPostgresMixedWorkspaceWorkload$', '-test.v', '-test.timeout=2m')
            WorkingDirectory = (Join-Path $repo 'internal/studio')
            RedirectStandardOutput = (Join-Path $out "trial-$i.log")
            RedirectStandardError = (Join-Path $out "trial-$i.err")
            PassThru = $true
            WindowStyle = 'Hidden'
        }
        $process = Start-Process @runArgs
        [long]$peak = 0
        $samples = 0
        try {
            # Windows PowerShell 5.1 needs a process handle opened while the
            # child is alive to report its exit code after the last refresh.
            [void]$process.Handle
            while (-not $process.HasExited) {
                $process.Refresh()
                $peak = [Math]::Max($peak, $process.PeakWorkingSet64)
                $samples++
                [void]$process.WaitForExit(20)
            }
            $process.WaitForExit()
            if ($process.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $env:MIXED_WORKLOAD_REPORT)) {
                throw "Workload trial $i failed. See $out/trial-$i.log and trial-$i.err."
            }
        } finally {
            $process.Dispose()
        }
        if ($peak -le 0) { throw "Windows did not expose a working set for trial $i." }
        [ordered]@{
            trial = $i
            peak_working_set_bytes_observed = $peak
            working_set_samples = $samples
            workload = (Get-Content -LiteralPath $env:MIXED_WORKLOAD_REPORT -Raw -Encoding UTF8 | ConvertFrom-Json)
        }
    }
    $report = [ordered]@{
        schema_version = 1
        measured_at = [DateTimeOffset]::UtcNow.ToString('o')
        cpu = (Get-CimInstance Win32_Processor | Select-Object -First 1 -ExpandProperty Name).Trim()
        machine_memory_bytes = (Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory
        windows_version = [Environment]::OSVersion.Version.ToString()
        test_binary_sha256 = $digest
        working_set_scope = 'Maximum sampled Windows PeakWorkingSet64 of each test process, sampled about every 20 ms. Includes App, HTTP fixtures, client, Go runtime and tests. Excludes PostgreSQL and the sampler.'
        trials = @($results)
    }
    $destination = Join-Path $out 'report.json'
    [IO.File]::WriteAllText($destination, (($report | ConvertTo-Json -Depth 30) + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
    Write-Output "Measured $Runs trial(s). Report: $destination"
} finally {
    $env:MIXED_WORKLOAD_TEST = $previousOptIn
    $env:MIXED_WORKLOAD_REPORT = $previousReport
    Pop-Location
}
