#requires -Version 5.1
[CmdletBinding()]
param(
    [ValidateSet('build', 'test', 'dev', 'validate', 'sync', 'retag', 'serve', 'status', 'cloud-check', 'cloud-reconcile')]
    [string]$Action = 'validate',
    [string]$Config = 'config.yaml',
    [string]$Task = '',
    [switch]$Check,
    [switch]$NoPause
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$developmentLock = $null
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

function Invoke-StartupCheck {
    param([string]$Binary, [string]$Assets)
    & (Join-Path $PSScriptRoot 'startup-check.ps1') -Binary $Binary -Assets $Assets -ProjectRoot $projectRoot
}

Push-Location -LiteralPath $projectRoot
try {
    if ($Check -and ($Action -ne 'dev' -or $PSBoundParameters.ContainsKey('Config'))) {
        throw '-Check is for dev startup verification and uses its own isolated configuration; omit -Config.'
    }
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw 'Go was not found on PATH. Install Go 1.26 or newer, then reopen the terminal.'
    }
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
        # Ignored tmp/ reference programs are not project packages.
        Invoke-Go -GoArguments @('test', '-count=1', './cmd/...', './internal/...')
        Invoke-Go -GoArguments @('vet', './cmd/...', './internal/...')
        if (Get-Command node -ErrorAction SilentlyContinue) {
            & node --test internal/webui/auth_test.js internal/webui/schedule_test.js internal/webui/states_test.js
            if ($LASTEXITCODE -ne 0) { throw "frontend tests failed with exit code $LASTEXITCODE" }
        } else {
            Write-Warning 'Node.js was not found; frontend tests were skipped.'
        }
        return
    }
    if ($Action -eq 'dev') {
        New-Item -ItemType Directory -Path (Join-Path $projectRoot 'bin') -Force | Out-Null
        $binary = Join-Path $projectRoot 'bin\meloport-dev.exe'
        try {
            $developmentLock = [IO.File]::Open((Join-Path $projectRoot 'bin\.development.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
        } catch [IO.IOException] {
            throw 'A development run from this checkout is already active. Stop it with Ctrl+C before rebuilding.'
        }
        Invoke-Go -GoArguments @('build', '-o', $binary, './cmd/meloport')
        $liveAssets = Join-Path $projectRoot 'internal\webui\assets'
        if ($Check) {
            Invoke-StartupCheck -Binary $binary -Assets $liveAssets
            return
        }
        if (-not [IO.Path]::IsPathRooted($Config)) { $Config = Join-Path $projectRoot $Config }
        $Config = [IO.Path]::GetFullPath($Config)
        if (Test-Path -LiteralPath $Config) {
            & $binary validate -config $Config
            if ($LASTEXITCODE -ne 0) { throw 'Configuration validation failed; correct the error above and restart.' }
        }
        Write-Host "Configuration: $Config"
        Write-Host 'Default Web UI: http://127.0.0.1:8080/ (use the listening address in the log for a custom port).'
        Write-Host 'Login uses MELOPORT_ADMIN_PASSWORD or server.admin_password in the ignored local YAML.'
        Write-Host 'Frontend changes: refresh the browser. Go/YAML changes: stop and restart.'
        Write-Host 'Press Ctrl+C to stop the server when finished.'
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
        if ($Action -notin @('sync', 'retag')) { throw '-Task is supported only with -Action sync or retag' }
        $applicationArguments += @('-task', $Task)
    }
    & $binary @applicationArguments
    if ($LASTEXITCODE -ne 0) { throw "meloport exited with code $LASTEXITCODE" }
} catch {
    if ($Action -ne 'dev') { throw }
    Write-Host ("Meloport could not complete the test run: " + $_.Exception.Message) -ForegroundColor Red
    if (-not $NoPause -and -not $Check) {
        [void](Read-Host 'Press Enter to close')
    }
    exit 1
} finally {
    if ($developmentLock) { $developmentLock.Dispose() }
    foreach ($variable in $cacheVariables) {
        [Environment]::SetEnvironmentVariable($variable, $previousValues[$variable], 'Process')
    }
    Pop-Location
}
