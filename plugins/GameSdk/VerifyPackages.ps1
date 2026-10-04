[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string]$GameDir,
    [string]$PackageVersion,
    [string]$PackageDirectory
)
$ErrorActionPreference = 'Stop'
if (-not $PackageDirectory) { $PackageDirectory = Join-Path $PSScriptRoot '..\..\.build\nuget' }
if (-not $PackageVersion) {
    [xml]$metadata = Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\PackageMetadata.props') -Raw
    $versions = Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\..\versions.json') -Raw | ConvertFrom-Json
    $PackageVersion = "$($metadata.Project.PropertyGroup.BD2PackageVersion)-game.$($versions.game_version)"
}
$PackageDirectory = [IO.Path]::GetFullPath($PackageDirectory)
# A fresh external directory and package cache prevent repository imports and stale
# same-version development packages from making an invalid package appear to work.
$verificationRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../.build/nuget-tests'))
$workspace = Join-Path $verificationRoot ([guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $workspace | Out-Null
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'samples\ExamplePlugin\ExamplePlugin.csproj'), (Join-Path $PSScriptRoot 'samples\ExamplePlugin\Plugin.cs') -Destination $workspace
$project = Join-Path $workspace 'ExamplePlugin.csproj'
$projectText = [IO.File]::ReadAllText($project).Replace('Version="0.2.1-game.2.35.10"', "Version=`"$PackageVersion`"")
[IO.File]::WriteAllText($project, $projectText)
$feed = [Security.SecurityElement]::Escape($PackageDirectory)
$cache = [Security.SecurityElement]::Escape((Join-Path $workspace 'packages'))
[IO.File]::WriteAllText((Join-Path $workspace 'NuGet.Config'), "<configuration><packageSources><clear/><add key=`"bd2`" value=`"$feed`"/><add key=`"nuget.org`" value=`"https://api.nuget.org/v3/index.json`"/></packageSources><config><add key=`"globalPackagesFolder`" value=`"$cache`"/></config></configuration>")
function Invoke-Dotnet([string[]]$Arguments) {
    & dotnet @Arguments
    if ($LASTEXITCODE -ne 0) { throw "dotnet failed with exit code $LASTEXITCODE" }
}
$sharedCacheRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../.build/game-sdk'))
Invoke-Dotnet @('build', $project, '-c', 'Release', '--nologo', "-p:GameDir=$GameDir", "-p:BD2GameSdkCache=$sharedCacheRoot")
$output = Join-Path $workspace 'bin\Release\netstandard2.1'
$runtime = Join-Path $output 'BD2.GameNames.dll'
foreach ($required in @('BD2.GameNames.dll', 'ExamplePlugin.dll')) {
    if (-not (Test-Path -LiteralPath (Join-Path $output $required))) { throw "Missing output: $required" }
}
foreach ($forbidden in @('Assembly-CSharp.Readable.dll', 'Assembly-CSharp.Readable.pdb', 'Assembly-CSharp.dll', 'GameSdk.dll', 'Mono.Cecil.dll', 'ICSharpCode.Decompiler.dll', 'BepInEx.dll', '0Harmony.dll', 'UnityEngine.dll', 'ExamplePlugin.pdb', 'navigation.json', 'sources', 'ref', 'lib')) {
    if (Test-Path -LiteralPath (Join-Path $output $forbidden)) { throw "Unexpected deployment artifact: $forbidden" }
}
$tool = Join-Path $workspace "packages\bd2.gamesdk\$PackageVersion\tools\net8.0\GameSdk.dll"
$sdk = Join-Path $workspace 'obj\Release\netstandard2.1\bd2-game-sdk'
$sharedSdk = [IO.File]::ReadAllText((Join-Path $sdk 'shared-sdk.txt')).Trim()
$table = Join-Path $sharedSdk 'names.json'
if ((Get-ChildItem -LiteralPath $sdk -Recurse -File | Measure-Object Length -Sum).Sum -gt 65536) {
    throw 'SDK obj still contains large copied artifacts instead of shared references.'
}
[xml]$exampleProject = Get-Content -LiteralPath $project -Raw
if ([IO.Path]::GetFileName([IO.Path]::GetDirectoryName($sharedSdk)) -ne $exampleProject.Project.PropertyGroup.BD2GameVersion) {
    throw 'Shared SDK directory is not grouped by the plugin-declared game version.'
}
$sharedReadyTimestamp = (Get-Item -LiteralPath (Join-Path $sharedSdk 'ready.txt')).LastWriteTimeUtc
Invoke-Dotnet @($tool, 'verify-runtime', $table, $runtime)
Invoke-Dotnet @($tool, 'verify-navigation', $sdk)
Invoke-Dotnet @($tool, 'verify', $table, (Join-Path $output 'ExamplePlugin.dll'), (Join-Path $GameDir 'BrownDust II_Data\Managed\Assembly-CSharp.dll'))
# Repeated builds must always transform the readable obj DLL, never reprocess bin.
$navigationProps = Join-Path $sdk 'GameSourceNavigation.props'
$navigationTimestamp = (Get-Item -LiteralPath $navigationProps).LastWriteTimeUtc
Invoke-Dotnet @('build', $project, '-c', 'Release', '--no-restore', '--nologo', "-p:GameDir=$GameDir", "-p:BD2GameSdkCache=$sharedCacheRoot")
if ((Get-Item -LiteralPath $navigationProps).LastWriteTimeUtc -ne $navigationTimestamp) {
    throw 'Unchanged navigation props were rewritten; this triggers repeated IDE reloads.'
}
if ((Get-Item -LiteralPath (Join-Path $sharedSdk 'ready.txt')).LastWriteTimeUtc -ne $sharedReadyTimestamp) {
    throw 'Repeated build unexpectedly regenerated the shared SDK.'
}
Invoke-Dotnet @($tool, 'verify', $table, (Join-Path $output 'ExamplePlugin.dll'), (Join-Path $GameDir 'BrownDust II_Data\Managed\Assembly-CSharp.dll'))
foreach ($invalidVersion in @('', '0.0.0')) {
    $rejectedBuild = & dotnet build $project -c Release --no-restore --nologo "-p:GameDir=$GameDir" "-p:BD2GameSdkCache=$sharedCacheRoot" "-p:BD2GameVersion=$invalidVersion" 2>&1 | Out-String
    if ($LASTEXITCODE -eq 0 -or ($invalidVersion -eq '' -and -not $rejectedBuild.Contains('BD2GameVersion')) -or
        ($invalidVersion -ne '' -and -not $rejectedBuild.Contains('Plugin requires game 0.0.0'))) {
        throw "Missing/mismatched game version was not rejected: $invalidVersion"
    }
}
Write-Host "Verified external PackageReference consumer, complete source navigation, shared runtime and incremental rebuild: $workspace"
# Successful probes are disposable. Retain a failed probe for diagnostics, but
# do not accumulate package caches after successful verification.
$resolvedProbe = [IO.Path]::GetFullPath($workspace)
if ([IO.Path]::GetDirectoryName($resolvedProbe) -ne $verificationRoot -or
    [IO.Path]::GetFileName($resolvedProbe) -notmatch '^[a-f0-9]{32}$') {
    throw 'Invalid verification cleanup path.'
}
Remove-Item -LiteralPath $resolvedProbe -Recurse -Force
