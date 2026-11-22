# A persistent local reader. The JS controller sends only its own direct child
# PID on stdin. The OS identifies the controller independently of that input.
$ErrorActionPreference = 'Stop'
$target = $null
try {
    $requested = [int]::Parse([Console]::ReadLine())
    if ($requested -le 0 -or $requested -eq $PID) { throw 'invalid child' }
    $parent = (Get-CimInstance Win32_Process -Filter "ProcessId=$PID").ParentProcessId
    $target = [Diagnostics.Process]::GetProcessById($requested)
    # Pin the process object before verifying the relationship. The handle
    # prevents a recycled numeric PID from changing our sampling target.
    $null = $target.Handle
    $record = Get-CimInstance Win32_Process -Filter "ProcessId=$requested"
    if (!$record -or !$parent -or $record.ParentProcessId -ne $parent -or $target.HasExited) {
        throw 'not a live sibling of this reader'
    }
    [Console]::WriteLine('ready')
    while ([Console]::ReadLine() -ceq 'sample') {
        if ($target.HasExited) {
            [Console]::WriteLine('exited')
            break
        }
        $target.Refresh()
        $cpu = $target.TotalProcessorTime.TotalMilliseconds
        $workingSet = $target.WorkingSet64
        $handles = $target.HandleCount
        $tick = [Diagnostics.Stopwatch]::GetTimestamp() * 1000.0 / [Diagnostics.Stopwatch]::Frequency
        if ($target.HasExited) { [Console]::WriteLine('exited'); break }
        $format = [Globalization.CultureInfo]::InvariantCulture
        [Console]::WriteLine([string]::Format($format, '{0:R} {1:R} {2} {3}', [object[]]@([double]$tick, [double]$cpu, $workingSet, $handles)))
    }
} catch {
    [Console]::WriteLine('unavailable')
} finally {
    if ($null -ne $target) { $target.Dispose() }
}
