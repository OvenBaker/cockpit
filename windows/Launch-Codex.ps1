# Install a copy on the Windows filesystem so this works before WSL is running.
param(
    [string]$Distribution = 'Ubuntu',
    [string]$LinuxUser = 'gareth',
    [switch]$Restart
)
$ErrorActionPreference = 'Stop'
try {
    $package = Get-AppxPackage -Name OpenAI.Codex
    if (-not $package) { throw 'Install the OpenAI.Codex Windows app first.' }
    $exe = Join-Path $package.InstallLocation 'app\ChatGPT.exe'
    & wsl.exe -d $Distribution -u $LinuxUser -- systemctl --user start cockpit-codex-server.service
    if ($LASTEXITCODE -ne 0) { throw 'The WSL Codex backend service could not start.' }
    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        try {
            $null = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:43129/readyz' -TimeoutSec 2
            $ready = $true
            break
        } catch { Start-Sleep -Seconds 1 }
    }
    if (-not $ready) { throw 'The WSL Codex backend did not become ready on localhost:43129.' }
    $running = @(Get-Process -Name ChatGPT -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe })
    if ($running.Count -gt 0 -and -not $Restart) {
        Write-Output 'ChatGPT is already running. Use -Restart to reconnect it through this launcher.'
        exit 0
    }
    foreach ($process in $running) {
        if ($process.MainWindowHandle -ne 0) { [void]$process.CloseMainWindow() }
    }
    if ($running.Count -gt 0) {
        Start-Sleep -Seconds 3
        Get-Process -Name ChatGPT -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe } | Stop-Process -Force
    }
    $env:CODEX_APP_SERVER_WS_URL = 'ws://127.0.0.1:43129'
    Remove-Item Env:CODEX_APP_SERVER_FORCE_CLI -ErrorAction SilentlyContinue
    Start-Process -FilePath $exe
} catch {
    $message = $_.Exception.Message
    Write-Error $message -ErrorAction Continue
    Add-Type -AssemblyName PresentationFramework
    [System.Windows.MessageBox]::Show($message, 'Cockpit Codex startup failed') | Out-Null
    exit 1
}
