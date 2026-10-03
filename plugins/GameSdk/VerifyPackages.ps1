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
$workspace = Join-Path ([IO.Path]::GetTempPath()) ('BD2 NuGet Test ' + [guid]::NewGuid().ToString('N'))
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
Invoke-Dotnet @('build', $project, '-c', 'Release', '--nologo', "-p:GameDir=$GameDir")
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
$table = Join-Path $sdk 'names.json'
Invoke-Dotnet @($tool, 'verify-runtime', $table, $runtime)
Invoke-Dotnet @($tool, 'verify-navigation', $sdk)
Invoke-Dotnet @($tool, 'verify', $table, (Join-Path $output 'ExamplePlugin.dll'), (Join-Path $GameDir 'BrownDust II_Data\Managed\Assembly-CSharp.dll'))
# Repeated builds must always transform the readable obj DLL, never reprocess bin.
$navigationProps = Join-Path $sdk 'GameSourceNavigation.props'
$navigationTimestamp = (Get-Item -LiteralPath $navigationProps).LastWriteTimeUtc
Invoke-Dotnet @('build', $project, '-c', 'Release', '--no-restore', '--nologo', "-p:GameDir=$GameDir")
if ((Get-Item -LiteralPath $navigationProps).LastWriteTimeUtc -ne $navigationTimestamp) {
    throw 'Unchanged navigation props were rewritten; this triggers repeated IDE reloads.'
}
Invoke-Dotnet @($tool, 'verify', $table, (Join-Path $output 'ExamplePlugin.dll'), (Join-Path $GameDir 'BrownDust II_Data\Managed\Assembly-CSharp.dll'))
Write-Host "Verified external PackageReference consumer, complete source navigation, shared runtime and incremental rebuild: $workspace"
