#requires -Version 5.1
[CmdletBinding()]
param(
    [ValidateSet('build', 'test', 'dev', 'validate', 'sync', 'serve', 'status')]
    [string]$Action = 'validate',
    [string]$Config = 'config.yaml',
    [string]$Task = ''
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$cacheVariables = @('GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR', 'CGO_ENABLED')
$previousValues = @{}
foreach ($variable in $cacheVariables) {
    $previousValues[$variable] = [Environment]::GetEnvironmentVariable($variable, 'Process')
}

function Invoke-Go {
    param([string[]]$GoArguments)
    & go @GoArguments
    if ($LASTEXITCODE -ne 0) { throw "go command failed with exit code $LASTEXITCODE" }
}

Push-Location -LiteralPath $projectRoot
try {
    $env:GOPATH = Join-Path $projectRoot '.cache\gopath'
    $env:GOMODCACHE = Join-Path $projectRoot '.cache\gomod'
    $env:GOCACHE = Join-Path $projectRoot '.cache\go-build'
    $env:GOTMPDIR = Join-Path $projectRoot '.cache\tmp'
    # The application and SQLite driver are pure Go; local builds need no C compiler.
    $env:CGO_ENABLED = '0'
    foreach ($variable in @('GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR')) {
        $cachePath = [Environment]::GetEnvironmentVariable($variable, 'Process')
        New-Item -ItemType Directory -Path $cachePath -Force | Out-Null
    }
    if ($Action -eq 'test') {
        Invoke-Go -GoArguments @('test', '-count=1', './...')
        Invoke-Go -GoArguments @('vet', './...')
        return
    }
    if ($Action -eq 'dev') {
        New-Item -ItemType Directory -Path (Join-Path $projectRoot 'bin') -Force | Out-Null
        $binary = Join-Path $projectRoot 'bin\meloport-dev.exe'
        Invoke-Go -GoArguments @('build', '-o', $binary, './cmd/meloport')
        $liveAssets = Join-Path $projectRoot 'internal\webui\assets'
        & $binary serve -config $Config -web-dir $liveAssets
        if ($LASTEXITCODE -ne 0) { throw "meloport exited with code $LASTEXITCODE" }
        return
    }
    New-Item -ItemType Directory -Path (Join-Path $projectRoot 'bin') -Force | Out-Null
    $binary = Join-Path $projectRoot 'bin\meloport.exe'
    Invoke-Go -GoArguments @('build', '-trimpath', '-o', $binary, './cmd/meloport')
    if ($Action -eq 'build') { return }
    $applicationArguments = @($Action, '-config', $Config)
    if ($Task) {
        if ($Action -ne 'sync') { throw '-Task is supported only with -Action sync' }
        $applicationArguments += @('-task', $Task)
    }
    & $binary @applicationArguments
    if ($LASTEXITCODE -ne 0) { throw "meloport exited with code $LASTEXITCODE" }
} finally {
    foreach ($variable in $cacheVariables) {
        [Environment]::SetEnvironmentVariable($variable, $previousValues[$variable], 'Process')
    }
    Pop-Location
}
