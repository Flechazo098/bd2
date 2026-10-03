# Dot-source in an external PowerShell terminal: . .\env.ps1
# Applies only to this terminal and child processes; no user-level Go settings.
$env:GOCACHE = Join-Path $PSScriptRoot '.cache\go-build'
