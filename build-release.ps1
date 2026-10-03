[CmdletBinding()]
param(
    [string]$GameDir,
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$goRoot = Join-Path $root 'go'
$buildRoot = Join-Path $root '.build'
$packageParent = Join-Path $buildRoot 'package'
$serverPackage = Join-Path $packageParent 'bd2server'
$clientPackage = Join-Path $packageParent 'bd2client'
$serverGoDir = Join-Path $serverPackage 'go'
$serverStateDir = Join-Path $serverPackage 'data\state'
$clientPluginDir = Join-Path $clientPackage 'plugins'
$versionConfig = Join-Path $root 'versions.json'
$authenticationConfig = Join-Path $root 'authentication.json'
$resourceConfig = Join-Path $root 'resources.json'

try {
    $releaseVersions = Get-Content -LiteralPath $versionConfig -Raw | ConvertFrom-Json -ErrorAction Stop
} catch {
    throw "Could not read release versions at ${versionConfig}: $($_.Exception.Message)"
}
$serverArchive = Join-Path $buildRoot ("bd2server-{0}-windows-x64.zip" -f $releaseVersions.server_version)
$clientArchive = Join-Path $buildRoot ("bd2client-{0}-windows-x64.zip" -f $releaseVersions.client_version)

if ([string]::IsNullOrWhiteSpace($GameDir)) {
    $developmentConfigPath = Join-Path $goRoot 'config.json'
    if (-not [IO.File]::Exists($developmentConfigPath)) {
        throw "GameDir was not provided and $developmentConfigPath does not exist. Copy go/config.example.json to go/config.json and set game_directory, or pass -GameDir explicitly."
    }
    try {
        $developmentConfig = Get-Content -LiteralPath $developmentConfigPath -Raw | ConvertFrom-Json -ErrorAction Stop
    } catch {
        throw "Could not read development config at ${developmentConfigPath}: $($_.Exception.Message)"
    }
    $propertyNames = @($developmentConfig.PSObject.Properties.Name | Sort-Object)
    if (($propertyNames -join ',') -ne 'game_directory,schema_version' -or
        $developmentConfig.schema_version -ne 1 -or
        -not ($developmentConfig.game_directory -is [string]) -or
        [string]::IsNullOrWhiteSpace($developmentConfig.game_directory)) {
        throw "Development config must contain only schema_version 1 and a non-empty game_directory: $developmentConfigPath"
    }
    $GameDir = $developmentConfig.game_directory
    Write-Host "Using development game directory: $GameDir"
}

$GameDir = [IO.Path]::GetFullPath($GameDir)
$gameExecutable = Join-Path $GameDir 'BrownDust II.exe'
if (-not [IO.File]::Exists($gameExecutable)) {
    throw "GameDir does not contain Brown Dust II.exe: $GameDir"
}

$env:GOCACHE = Join-Path $goRoot '.cache\go-build'
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $buildRoot | Out-Null

foreach ($target in @($packageParent, $serverArchive, $clientArchive)) {
    if (Test-Path -LiteralPath $target) {
        $resolved = [IO.Path]::GetFullPath($target)
        $expectedRoot = [IO.Path]::GetFullPath($buildRoot) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected build path: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
New-Item -ItemType Directory -Force -Path $serverPackage, $serverGoDir, $serverStateDir, $clientPackage, $clientPluginDir | Out-Null

Push-Location $goRoot
try {
    if (-not $SkipTests) {
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
        go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed with exit code $LASTEXITCODE" }
        go test -tags 'release,production' ./...
        if ($LASTEXITCODE -ne 0) { throw "release-tag go test failed with exit code $LASTEXITCODE" }
        go vet -tags 'release,production' ./...
        if ($LASTEXITCODE -ne 0) { throw "release-tag go vet failed with exit code $LASTEXITCODE" }
    }
    go build -tags release -trimpath -ldflags '-s -w' -o (Join-Path $serverPackage 'bd2server.exe') .\cmd\bd2server
    if ($LASTEXITCODE -ne 0) { throw "bd2server build failed with exit code $LASTEXITCODE" }
    go build -tags 'release,production' -trimpath -ldflags '-H windowsgui -s -w' -o (Join-Path $clientPackage 'bd2client.exe') .\cmd\bd2client
    if ($LASTEXITCODE -ne 0) { throw "bd2client build failed with exit code $LASTEXITCODE" }
} finally { Pop-Location }

$clientPlugins = @(
    @{ Name = 'LocalIdentity'; Project = Join-Path $root 'plugins\LocalIdentity\LocalIdentity.csproj'; Output = Join-Path $root 'plugins\LocalIdentity\bin\Release\netstandard2.1\BD2LocalIdentity.dll'; FileName = 'BD2LocalIdentity.dll' },
    @{ Name = 'LoginUI'; Project = Join-Path $root 'plugins\LoginUI\LoginUI.csproj'; Output = Join-Path $root 'plugins\LoginUI\bin\Release\netstandard2.1\BD2LoginUI.dll'; FileName = 'BD2LoginUI.dll' }
)
foreach ($plugin in $clientPlugins) {
    dotnet build $plugin.Project -c Release "-p:GameDir=$GameDir" --nologo
    if ($LASTEXITCODE -ne 0) { throw "$($plugin.Name) build failed with exit code $LASTEXITCODE" }
    if (-not (Test-Path -LiteralPath $plugin.Output -PathType Leaf)) { throw "$($plugin.Name) build output is missing" }
    Copy-Item -LiteralPath $plugin.Output -Destination (Join-Path $clientPluginDir $plugin.FileName) -Force
}

Copy-Item -LiteralPath (Join-Path $goRoot 'seed') -Destination $serverGoDir -Recurse -Force
Copy-Item -LiteralPath $versionConfig -Destination (Join-Path $serverPackage 'versions.json') -Force
Copy-Item -LiteralPath $authenticationConfig -Destination (Join-Path $serverPackage 'authentication.json') -Force
Copy-Item -LiteralPath $resourceConfig -Destination (Join-Path $serverPackage 'resources.json') -Force
Copy-Item -LiteralPath $versionConfig -Destination (Join-Path $clientPackage 'versions.json') -Force
Copy-Item -LiteralPath (Join-Path $root 'RELEASE.md') -Destination (Join-Path $serverPackage 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'AUTHENTICATION.md') -Destination (Join-Path $serverPackage 'AUTHENTICATION.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'RESOURCES.md') -Destination (Join-Path $serverPackage 'RESOURCES.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'docs\CLIENT.md') -Destination (Join-Path $clientPackage 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $serverPackage 'LICENSE') -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $clientPackage 'LICENSE') -Force

Compress-Archive -LiteralPath $serverPackage -DestinationPath $serverArchive -CompressionLevel Optimal
Compress-Archive -LiteralPath $clientPackage -DestinationPath $clientArchive -CompressionLevel Optimal
Write-Host "Built pure server directory: $serverPackage"
Write-Host "Built pure server archive:   $serverArchive"
Write-Host "Built client directory:       $clientPackage"
Write-Host "Built client archive:         $clientArchive"
