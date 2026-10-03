[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string]$GameDir,
    [Parameter(Mandatory)] [string]$GameMapping,
    [string]$VersionConfig
)
$ErrorActionPreference = 'Stop'
if (-not $VersionConfig) { $VersionConfig = Join-Path $PSScriptRoot '..\..\versions.json' }
$repository = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$staging = Join-Path $repository ('.build\names-update\' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $staging | Out-Null
function Invoke-Dotnet([string[]]$Arguments) {
    & dotnet @Arguments
    if ($LASTEXITCODE -ne 0) { throw "dotnet failed with exit code $LASTEXITCODE" }
}
Invoke-Dotnet @('build', (Join-Path $PSScriptRoot 'GameSdk.csproj'), '-c', 'Release', '--nologo')
$tool = Join-Path $PSScriptRoot 'bin\Release\net8.0\GameSdk.dll'
$assembly = Join-Path $GameDir 'BrownDust II_Data\Managed\Assembly-CSharp.dll'
$table = Join-Path $staging 'names.json'
Invoke-Dotnet @($tool, 'names', $assembly, $GameMapping, $VersionConfig, $table)
# Validate the full metadata transformation before replacing the checked-in table.
Invoke-Dotnet @($tool, 'shell', $table, $assembly, (Join-Path $staging 'Assembly-CSharp.Readable.dll'))
$destination = Join-Path $PSScriptRoot '..\GameNames\Mappings\names.json.gz'
Copy-Item -LiteralPath "$table.gz" -Destination $destination -Force
Write-Host "Updated SDK/runtime shared table: $destination. Rebuild SDK and plugins, then pack a new version."
