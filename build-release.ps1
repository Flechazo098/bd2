[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$GameDir,

    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$goRoot = Join-Path $root 'go'
$localIdentityProject = Join-Path $root 'plugins\LocalIdentity\LocalIdentity.csproj'
$buildRoot = Join-Path $root '.build'
$packageParent = Join-Path $buildRoot 'package'
$packageDir = Join-Path $packageParent 'bd2server'
$packagePluginDir = Join-Path $packageDir 'plugins'
$packageGoDir = Join-Path $packageDir 'go'
$packageStateDir = Join-Path $packageDir 'data\state'
$archive = Join-Path $buildRoot 'bd2server-windows-x64.zip'
$versionConfig = Join-Path $root 'versions.json'

$GameDir = [IO.Path]::GetFullPath($GameDir)
if (-not (Test-Path -LiteralPath (Join-Path $GameDir 'BrownDust II.exe') -PathType Leaf)) {
    throw "GameDir does not contain BrownDust II.exe: $GameDir"
}

$env:GOCACHE = Join-Path $goRoot '.cache\go-build'
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $buildRoot | Out-Null

foreach ($target in @($packageParent, $archive)) {
    if (Test-Path -LiteralPath $target) {
        $resolved = [IO.Path]::GetFullPath($target)
        $expectedRoot = [IO.Path]::GetFullPath($buildRoot) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected build path: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
New-Item -ItemType Directory -Force -Path $packageDir, $packagePluginDir, $packageGoDir, $packageStateDir | Out-Null

dotnet build $localIdentityProject -c Release "-p:GameDir=$GameDir" --nologo
if ($LASTEXITCODE -ne 0) { throw "LocalIdentity build failed with exit code $LASTEXITCODE" }
$identityPlugin = Join-Path $root 'plugins\LocalIdentity\bin\Release\netstandard2.1\BD2LocalIdentity.dll'
if (-not (Test-Path -LiteralPath $identityPlugin -PathType Leaf)) {
    throw "LocalIdentity build output is missing: $identityPlugin"
}
Copy-Item -LiteralPath $identityPlugin -Destination (Join-Path $packagePluginDir 'BD2LocalIdentity.dll') -Force

Push-Location $goRoot
try {
    if (-not $SkipTests) {
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
        go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed with exit code $LASTEXITCODE" }
    }
    go build -trimpath -ldflags '-s -w' -o (Join-Path $packageDir 'bd2server.exe') .\cmd\bd2server
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally { Pop-Location }

Copy-Item -LiteralPath (Join-Path $goRoot 'seed') -Destination (Join-Path $packageGoDir 'seed') -Recurse -Force
Copy-Item -LiteralPath $versionConfig -Destination (Join-Path $packageDir 'versions.json') -Force
Copy-Item -LiteralPath (Join-Path $root 'RELEASE.md') -Destination (Join-Path $packageDir 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $packageDir 'LICENSE') -Force

Compress-Archive -LiteralPath $packageDir -DestinationPath $archive -CompressionLevel Optimal
Write-Host "Built release directory: $packageDir"
Write-Host "Built release archive:   $archive"
