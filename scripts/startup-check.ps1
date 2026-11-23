#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Assets,
    [Parameter(Mandatory = $true)][string]$ProjectRoot
)

$ErrorActionPreference = 'Stop'
$checkRoot = Join-Path $ProjectRoot ('tmp\startup-check-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $checkRoot -Force | Out-Null
# A space in the file name exercises the actual Windows argument quoting.
$configPath = Join-Path $checkRoot 'startup check.yaml'
$stdoutPath = Join-Path $checkRoot 'stdout.log'
$stderrPath = Join-Path $checkRoot 'stderr.log'
$passwordVariable = 'MELOPORT_ADMIN_PASSWORD'
$previousPassword = [Environment]::GetEnvironmentVariable($passwordVariable, 'Process')
$checkPassword = [Guid]::NewGuid().ToString('N') + [Guid]::NewGuid().ToString('N')
$server = $null
$client = $null
$handler = $null
$port = 0

function Assert-Check {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Read-CheckLog {
    param([string]$Path)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite)
    $reader = [IO.StreamReader]::new($stream, [Text.Encoding]::UTF8)
    try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
}

function Invoke-CheckRequest {
    param([string]$Method, [string]$Path, [int]$Status = 200, $Body = $null)
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::new($Method), $script:origin + $Path)
    $response = $null
    try {
        [void]$request.Headers.TryAddWithoutValidation('X-Meloport-Request', '1')
        [void]$request.Headers.TryAddWithoutValidation('Origin', $script:origin)
        if ($null -ne $Body -or $Method -notin @('GET', 'HEAD')) {
            $payload = if ($null -eq $Body) { @{} } else { $Body }
            $request.Content = [Net.Http.StringContent]::new(($payload | ConvertTo-Json -Compress), [Text.Encoding]::UTF8, 'application/json')
        }
        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        Assert-Check ([int]$response.StatusCode -eq $Status) ("Unexpected HTTP status for $Method $Path; expected $Status, got " + [int]$response.StatusCode)
        return $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    } finally {
        if ($response) { $response.Dispose() }
        $request.Dispose()
    }
}

try {
    $yaml = @'
timezone: Asia/Shanghai
schedule: '0 3 * * *'
run_on_start: false
api:
  key_env: MELOPORT_STARTUP_CHECK_NO_API_KEY
server:
  listen: '127.0.0.1:0'
tasks: []
'@
    [IO.File]::WriteAllText($configPath, $yaml, [Text.UTF8Encoding]::new($false))
    [Environment]::SetEnvironmentVariable($passwordVariable, $checkPassword, 'Process')
    Add-Type -AssemblyName System.Net.Http
    $handler = [Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $handler.CookieContainer = [Net.CookieContainer]::new()
    $client = [Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(3)
    $arguments = @('serve', '-config', ('"' + $configPath + '"'), '-web-dir', ('"' + $Assets + '"'))
    $server = Start-Process -FilePath $Binary -ArgumentList $arguments -WorkingDirectory $ProjectRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $ready = $false
    do {
        $server.Refresh()
        if ($server.HasExited) { throw "The test server exited before readiness. Inspect $stderrPath" }
        foreach ($line in ((Read-CheckLog $stderrPath) -split '\r?\n')) {
            try { $entry = $line | ConvertFrom-Json } catch { continue }
            if ($entry.msg -eq 'local web server listening' -and $entry.address -match '^127\.0\.0\.1:(\d+)$') {
                $port = [int]$Matches[1]
                $script:origin = 'http://' + $entry.address
            }
        }
        if ($port) {
            try {
                [void](Invoke-CheckRequest -Method GET -Path '/readyz')
                $ready = $true
            } catch {
                if ([DateTime]::UtcNow -ge $deadline) { throw }
            }
        }
        if (-not $ready) { Start-Sleep -Milliseconds 100 }
    } until ($ready -or [DateTime]::UtcNow -ge $deadline)
    Assert-Check $ready "The test server did not become ready. Inspect $stderrPath"
    Write-Host "Test server ready: $script:origin"
    $page = Invoke-CheckRequest -Method GET -Path '/'
    Assert-Check ($page.Contains('id="login-form"')) 'The Web UI login page was not served.'
    $css = Invoke-CheckRequest -Method GET -Path '/assets/app.css'
    $js = Invoke-CheckRequest -Method GET -Path '/assets/app.js'
    Assert-Check ($css.Contains('.app-shell') -and $js.Contains('DOMContentLoaded')) 'Live frontend assets were not served.'
    $session = (Invoke-CheckRequest -Method GET -Path '/api/session') | ConvertFrom-Json
    Assert-Check ($session.configured -and -not $session.authenticated) 'The isolated administrator login was not configured.'
    [void](Invoke-CheckRequest -Method GET -Path '/api/overview' -Status 401)
    [void](Invoke-CheckRequest -Method POST -Path '/api/session' -Body @{ password = $checkPassword })
    $overview = (Invoke-CheckRequest -Method GET -Path '/api/overview') | ConvertFrom-Json
    Assert-Check ($overview.stats.tasks -eq 0) 'Startup check must not load real subscriptions.'
    [void](Invoke-CheckRequest -Method PUT -Path '/api/settings' -Body @{ timezone = 'UTC'; schedule = '0 4 * * *'; quality = 'lossless'; auto_downgrade = $true; music_dir = 'check-music' })
    $saved = (Invoke-CheckRequest -Method GET -Path '/api/overview') | ConvertFrom-Json
    Assert-Check ($saved.timezone -eq 'UTC' -and $saved.schedule -eq '0 4 * * *' -and $saved.music_dir -eq 'check-music') 'Saved settings were not read back.'
    [void](Invoke-CheckRequest -Method DELETE -Path '/api/session')
    [void](Invoke-CheckRequest -Method GET -Path '/api/overview' -Status 401)
    Assert-Check (-not [IO.File]::ReadAllText($configPath).Contains($checkPassword)) 'The test credential was written to YAML.'
    Assert-Check (-not (Read-CheckLog $stderrPath).Contains($checkPassword)) 'The test credential was written to logs.'
} finally {
    try {
        if ($client) { $client.Dispose() }
        if ($handler) { $handler.Dispose() }
        if ($server) {
            $server.Refresh()
            if (-not $server.HasExited) {
                # The Process object identifies only the server owned by this check.
                Stop-Process -InputObject $server -Force -ErrorAction Stop
                Assert-Check ($server.WaitForExit(5000)) 'The owned test server did not exit.'
            }
            $server.Dispose()
        }
        if ($port) {
            $probe = [Net.Sockets.TcpClient]::new()
            try {
                try { $connected = $probe.ConnectAsync('127.0.0.1', $port).Wait(1000) -and $probe.Connected } catch { $connected = $false }
                Assert-Check (-not $connected) "The owned test server left port $port listening."
            } finally { $probe.Dispose() }
        }
    } finally {
        [Environment]::SetEnvironmentVariable($passwordVariable, $previousPassword, 'Process')
    }
}
Write-Host 'Startup check passed: Web UI, authentication, settings persistence, logout and port cleanup.'
Write-Host "Check artifacts: $checkRoot"
