[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string]$Config,
    [Parameter(Mandatory)] [string]$Output,
    [Parameter(Mandatory)] [ValidateSet('local_identity', 'capture_environment')] [string]$Plugin
)

$ErrorActionPreference = 'Stop'
$versions = Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
$pluginVersion = $versions.plugins.$Plugin
$semver = '^[0-9]+\.[0-9]+\.[0-9]+$'
$resource = '^[0-9]{14}$'
foreach ($entry in @(
    @('client_version', $versions.client_version, $semver),
    @('protocol_version', $versions.protocol_version, $semver),
    @('game_data_version', $versions.game_data_version, $resource),
    @('bundle_version', $versions.bundle_version, $resource),
    @("plugins.$Plugin", $pluginVersion, $semver)
)) {
    if ([string]$entry[1] -notmatch $entry[2]) {
        throw "Invalid $($entry[0]) in $Config"
    }
}

$source = @"
// Generated from versions.json. Do not edit.
namespace Bd2Build
{
    internal static class Versions
    {
        internal const string Client = "$($versions.client_version)";
        internal const string Protocol = "$($versions.protocol_version)";
        internal const string GameData = "$($versions.game_data_version)";
        internal const string Bundle = "$($versions.bundle_version)";
        internal const string Plugin = "$pluginVersion";
    }
}
"@
$directory = Split-Path -Parent $Output
if ($directory) {
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
}
[IO.File]::WriteAllText($Output, $source, [Text.UTF8Encoding]::new($false))
