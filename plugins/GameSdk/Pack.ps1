[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string]$GameDir,
    [string]$VersionConfig,
    [string]$PackageVersion,
    [string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
if (-not $VersionConfig) { $VersionConfig = Join-Path $PSScriptRoot '..\..\versions.json' }
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PSScriptRoot '..\..\.build\nuget' }
$repository = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$versions = Get-Content -LiteralPath $VersionConfig -Raw | ConvertFrom-Json
if (-not $PackageVersion) {
    [xml]$metadata = Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\PackageMetadata.props') -Raw
    $PackageVersion = "$($metadata.Project.PropertyGroup.BD2PackageVersion)-game.$($versions.game_version)"
}
if ($PackageVersion -notmatch '^\d+\.\d+\.\d+-game\.\d+\.\d+\.\d+(?:\.[A-Za-z0-9-]+)*$') {
    throw 'PackageVersion must be SemVer with game version, for example 0.2.1-game.2.35.10.'
}
if ($PackageVersion -notmatch ('-game\.' + [regex]::Escape([string]$versions.game_version) + '(?:\.|$)')) {
    throw 'PackageVersion game suffix must match VersionConfig game_version.'
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$staging = Join-Path $repository ".build\nuget-staging\$PackageVersion"
$sdk = Join-Path $staging 'sdk'
$tool = Join-Path $staging 'tool'
$runtimeObj = Join-Path $staging 'runtime-obj'
$runtimeBin = Join-Path $staging 'runtime-bin'
$packageObj = Join-Path $staging 'package-obj'
$packageBin = Join-Path $staging 'package-bin'
New-Item -ItemType Directory -Force -Path $OutputDirectory, $staging | Out-Null
function Invoke-Dotnet([string[]]$Arguments) {
    & dotnet @Arguments
    if ($LASTEXITCODE -ne 0) { throw "dotnet failed with exit code $LASTEXITCODE" }
}
$toolVersion = $PackageVersion.Split('-')[0]
Invoke-Dotnet @('publish', (Join-Path $PSScriptRoot 'GameSdk.csproj'), '-c', 'Release', '--nologo', '-o', $tool, "-p:Version=$toolVersion", '-p:UseAppHost=false')
$assembly = Join-Path $GameDir 'BrownDust II_Data\Managed\Assembly-CSharp.dll'
$toolDll = Join-Path $tool 'GameSdk.dll'
$previousSdkCache = $env:BD2_GAME_SDK_CACHE
try {
    if (-not $env:BD2_GAME_SDK_CACHE) { $env:BD2_GAME_SDK_CACHE = Join-Path $repository '.build/game-sdk' }
    Invoke-Dotnet @($toolDll, 'prepare-embedded', $assembly, $sdk, $VersionConfig)
} finally { $env:BD2_GAME_SDK_CACHE = $previousSdkCache }
$sharedSdk = [IO.File]::ReadAllText((Join-Path $sdk 'shared-sdk.txt')).Trim()
$table = Join-Path $sharedSdk 'names.json'
$runtimeProject = Join-Path $PSScriptRoot '..\GameNames\GameNames.csproj'
Invoke-Dotnet @('pack', $runtimeProject, '-c', 'Release', '--nologo', '-o', $OutputDirectory,
    "-p:Version=$PackageVersion", "-p:GameNamesTable=$table", "-p:BaseIntermediateOutputPath=$runtimeObj/", "-p:OutputPath=$runtimeBin/")
Invoke-Dotnet @($toolDll, 'verify-runtime', $table, (Join-Path $runtimeBin 'BD2.GameNames.dll'))
$packageProject = Join-Path $PSScriptRoot 'Package\BD2.GameSdk.Package.csproj'
$nugetConfig = Join-Path $staging 'NuGet.Config'
$escapedSource = [Security.SecurityElement]::Escape($OutputDirectory)
[IO.File]::WriteAllText($nugetConfig, "<configuration><packageSources><clear/><add key=`"bd2-local`" value=`"$escapedSource`"/><add key=`"nuget.org`" value=`"https://api.nuget.org/v3/index.json`"/></packageSources></configuration>")
Invoke-Dotnet @('restore', $packageProject, '--configfile', $nugetConfig, "-p:Version=$PackageVersion",
    '-p:BD2Packaging=true', "-p:BaseIntermediateOutputPath=$packageObj/", "-p:OutputPath=$packageBin/")
Invoke-Dotnet @('pack', $packageProject, '-c', 'Release', '--no-restore', '--nologo', '-o', $OutputDirectory,
    "-p:Version=$PackageVersion", "-p:BD2ToolPublishDir=$tool", '-p:BD2Packaging=true',
    "-p:BaseIntermediateOutputPath=$packageObj/", "-p:OutputPath=$packageBin/")
Write-Host "Created BD2.GameNames and BD2.GameSdk $PackageVersion in $OutputDirectory"
