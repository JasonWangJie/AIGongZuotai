param([ValidateSet('setup','start','stop','status')][string]$Action = 'status')
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$runtimeRoot = Join-Path $projectRoot '.runtime'
$identityHasher = [Security.Cryptography.SHA256]::Create()
$workspaceKey = ([BitConverter]::ToString($identityHasher.ComputeHash([Text.Encoding]::UTF8.GetBytes($projectRoot.ToLowerInvariant())))).Replace('-', '').Substring(0,16).ToLowerInvariant()
$identityHasher.Dispose()
$developmentRoot = $env:POSTGRES_DEV_ROOT
if (!$developmentRoot) { $developmentRoot = Join-Path $env:LOCALAPPDATA 'AIWorkbenchPostgres' }
$instanceRoot = [IO.Path]::GetFullPath((Join-Path $developmentRoot $workspaceKey))
if ($instanceRoot -match '[^\x00-\x7F]') { throw 'PostgreSQL Windows tools need an ASCII path. Set POSTGRES_DEV_ROOT to an ASCII development directory.' }
$toolRoot = Join-Path $instanceRoot 'postgres'
$binaryRoot = Join-Path $toolRoot 'pgsql\bin'
$dataRoot = Join-Path $instanceRoot 'pgdata'
$environmentPath = Join-Path $runtimeRoot 'postgres.env'
$archivePath = Join-Path $runtimeRoot 'postgresql-18.7.zip'
$archiveUri = 'https://get.enterprisedb.com/postgresql/postgresql-18.7-1-windows-x64-binaries.zip'
$port = '55432'

function Run-Pg([string]$Name, [string[]]$Arguments) {
  & (Join-Path $binaryRoot ($Name + '.exe')) @Arguments
  if ($LASTEXITCODE -ne 0) { throw "$Name exited with $LASTEXITCODE" }
}

if ($Action -eq 'setup') {
  New-Item -ItemType Directory -Force -Path $runtimeRoot | Out-Null
  if (!(Test-Path -LiteralPath (Join-Path $toolRoot '.distribution-18.7'))) {
    if (!(Test-Path -LiteralPath $archivePath) -or (Get-Item -LiteralPath $archivePath).Length -ne 389515503) {
      Write-Host 'Downloading PostgreSQL 18.7 portable binaries from the official EDB distribution...'
      Invoke-WebRequest -Uri $archiveUri -OutFile $archivePath -UseBasicParsing
    }
    New-Item -ItemType Directory -Force -Path $toolRoot | Out-Null
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
      foreach ($entry in $archive.Entries) {
        if ($entry.FullName -notmatch '^pgsql/(bin|lib|share)/' -or $entry.FullName.EndsWith('/')) { continue }
        $destination = [IO.Path]::GetFullPath((Join-Path $toolRoot $entry.FullName))
        if (!$destination.StartsWith($toolRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'Archive entry escapes the PostgreSQL tool directory.' }
        [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($destination)) | Out-Null
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $destination, $true)
      }
    } finally { $archive.Dispose() }
    [IO.File]::WriteAllText((Join-Path $toolRoot '.distribution-18.7'), '18.7')
  }
  if (!(Test-Path -LiteralPath (Join-Path $dataRoot 'PG_VERSION'))) {
    if (Test-Path -LiteralPath $dataRoot) {
      throw 'An incomplete pgdata directory exists. Inspect it manually; this script never deletes database data.'
    }
    $secretBytes = New-Object byte[] 32
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    $generator.GetBytes($secretBytes)
    $generator.Dispose()
    $localPassword = ([BitConverter]::ToString($secretBytes)).Replace('-', '').ToLowerInvariant()
    $passwordPath = Join-Path $instanceRoot 'postgres-init-password.tmp'
    [IO.File]::WriteAllText($passwordPath, $localPassword, (New-Object Text.UTF8Encoding $false))
    try {
      Run-Pg 'initdb' @('-D',$dataRoot,'-U','studio','--encoding=UTF8','--locale=C','--auth-host=scram-sha-256','--auth-local=scram-sha-256',"--pwfile=$passwordPath")
    } finally {
      if (Test-Path -LiteralPath $passwordPath) { Remove-Item -LiteralPath $passwordPath }
    }
    $devUrl = "postgresql://studio:${localPassword}@127.0.0.1:${port}/aigongzuotai_dev?sslmode=disable"
    $testUrl = "postgresql://studio:${localPassword}@127.0.0.1:${port}/aigongzuotai_test?sslmode=disable"
    [IO.File]::WriteAllText($environmentPath, "DATABASE_URL=$devUrl`nTEST_DATABASE_URL=$testUrl`nAUTH_MODE=required`n", (New-Object Text.UTF8Encoding $false))
  }
  $Action = 'start'
}

if (!(Test-Path -LiteralPath (Join-Path $binaryRoot 'pg_ctl.exe'))) { throw 'Run db:setup first.' }
if ($Action -eq 'stop') {
  Run-Pg 'pg_ctl' @('-D',$dataRoot,'-m','fast','-w','stop')
  exit
}
if ($Action -eq 'status') {
  & (Join-Path $binaryRoot 'pg_ctl.exe') -D $dataRoot status
  exit $LASTEXITCODE
}
if (!(Test-Path -LiteralPath $environmentPath)) { throw 'postgres.env is missing; recover local credentials before starting. No data was removed.' }
& (Join-Path $binaryRoot 'pg_ctl.exe') -D $dataRoot status *> $null
if ($LASTEXITCODE -ne 0) {
  Run-Pg 'pg_ctl' @('-D',$dataRoot,'-l',(Join-Path $instanceRoot 'postgres.log'),'-o',"-h 127.0.0.1 -p $port",'-w','start')
}
$databaseLine = Get-Content -LiteralPath $environmentPath | Where-Object { $_.StartsWith('DATABASE_URL=') } | Select-Object -First 1
$localUri = [Uri]$databaseLine.Substring('DATABASE_URL='.Length)
$previousPassword = $env:PGPASSWORD
try {
  $env:PGPASSWORD = $localUri.UserInfo.Split(':',2)[1]
  foreach ($databaseName in @('aigongzuotai_dev','aigongzuotai_test')) {
    $exists = & (Join-Path $binaryRoot 'psql.exe') -h 127.0.0.1 -p $port -U studio -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$databaseName'"
    if ($LASTEXITCODE -ne 0) { throw 'Cannot query the local database.' }
    if ($exists -ne '1') { Run-Pg 'createdb' @('-h','127.0.0.1','-p',$port,'-U','studio',$databaseName) }
  }
} finally { $env:PGPASSWORD = $previousPassword }
Write-Host "PostgreSQL is ready on 127.0.0.1:$port. Credentials are in ignored .runtime/postgres.env (not printed)."
Write-Host "Workspace-specific PostgreSQL data: $dataRoot"
