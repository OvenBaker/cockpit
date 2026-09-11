# Copies the launcher locally and creates Start-menu and sign-in shortcuts.
param([string]$Distribution = 'Ubuntu', [string]$LinuxUser = 'gareth')
$ErrorActionPreference = 'Stop'
if ($Distribution -notmatch '^[A-Za-z0-9._-]+$' -or $LinuxUser -notmatch '^[A-Za-z0-9._-]+$') {
    throw 'Distribution and Linux user must contain only letters, digits, dot, underscore or dash.'
}
$destination = Join-Path $env:LOCALAPPDATA 'Cockpit'
New-Item -ItemType Directory -Force -Path $destination | Out-Null
$launcher = Join-Path $destination 'Launch-Codex.ps1'
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'Launch-Codex.ps1') -Destination $launcher -Force
$shell = New-Object -ComObject WScript.Shell
foreach ($folder in @([Environment]::GetFolderPath('Programs'), [Environment]::GetFolderPath('Startup'))) {
    $shortcut = $shell.CreateShortcut((Join-Path $folder 'ChatGPT (Cockpit).lnk'))
    $shortcut.TargetPath = Join-Path $PSHOME 'powershell.exe'
    $shortcut.Arguments = '-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "' + $launcher + '" -Distribution ' + $Distribution + ' -LinuxUser ' + $LinuxUser
    $shortcut.WorkingDirectory = $destination
    $shortcut.Description = 'ChatGPT connected to the primary Linux Codex backend'
    $shortcut.Save()
}
Write-Output "Installed launcher: $launcher"
