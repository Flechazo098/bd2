[CmdletBinding()]
param(
    [Parameter(Mandatory)]
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
$macAMD64Package = Join-Path $packageParent 'bd2client-macos-amd64'
$macARM64Package = Join-Path $packageParent 'bd2client-macos-arm64'
$serverGoDir = Join-Path $serverPackage 'go'
$serverStateDir = Join-Path $serverPackage 'data\state'
$clientPluginDir = Join-Path $clientPackage 'plugins'
$macAMD64ExecutableDir = Join-Path $macAMD64Package 'BD2 Client Studio.app\Contents\MacOS'
$macARM64ExecutableDir = Join-Path $macARM64Package 'BD2 Client Studio.app\Contents\MacOS'
$serverArchive = Join-Path $buildRoot 'bd2server-windows-x64.zip'
$clientArchive = Join-Path $buildRoot 'bd2client-windows-x64.zip'
$macAMD64Archive = Join-Path $buildRoot 'bd2client-macos-amd64.zip'
$macARM64Archive = Join-Path $buildRoot 'bd2client-macos-arm64.zip'
$versionConfig = Join-Path $root 'versions.json'
$authenticationConfig = Join-Path $root 'authentication.json'
$resourceConfig = Join-Path $root 'resources.json'

$GameDir = [IO.Path]::GetFullPath($GameDir)
$gameExecutable = Join-Path $GameDir 'BrownDust II.exe'
if (-not [IO.File]::Exists($gameExecutable)) {
    throw "GameDir does not contain Brown Dust II.exe: $GameDir"
}

$env:GOCACHE = Join-Path $goRoot '.cache\go-build'
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $buildRoot | Out-Null

foreach ($target in @($packageParent, $serverArchive, $clientArchive, $macAMD64Archive, $macARM64Archive)) {
    if (Test-Path -LiteralPath $target) {
        $resolved = [IO.Path]::GetFullPath($target)
        $expectedRoot = [IO.Path]::GetFullPath($buildRoot) + [IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unexpected build path: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
New-Item -ItemType Directory -Force -Path $serverPackage, $serverGoDir, $serverStateDir, $clientPackage, $clientPluginDir, $macAMD64ExecutableDir, $macARM64ExecutableDir | Out-Null

Push-Location $goRoot
try {
    if (-not $SkipTests) {
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
        go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed with exit code $LASTEXITCODE" }
        go test -tags release ./...
        if ($LASTEXITCODE -ne 0) { throw "release-tag go test failed with exit code $LASTEXITCODE" }
        go vet -tags release ./...
        if ($LASTEXITCODE -ne 0) { throw "release-tag go vet failed with exit code $LASTEXITCODE" }
    }
    go build -tags release -trimpath -ldflags '-s -w' -o (Join-Path $serverPackage 'bd2server.exe') .\cmd\bd2server
    if ($LASTEXITCODE -ne 0) { throw "bd2server build failed with exit code $LASTEXITCODE" }
    go build -tags release -trimpath -ldflags '-H windowsgui -s -w' -o (Join-Path $clientPackage 'bd2client.exe') .\cmd\bd2client
    if ($LASTEXITCODE -ne 0) { throw "bd2client build failed with exit code $LASTEXITCODE" }
    $savedGOOS = $env:GOOS
    $savedGOARCH = $env:GOARCH
    try {
        $env:GOOS = 'darwin'
        $env:GOARCH = 'amd64'
        go build -tags release -trimpath -ldflags '-s -w' -o (Join-Path $macAMD64ExecutableDir 'bd2client') .\cmd\bd2client
        if ($LASTEXITCODE -ne 0) { throw "bd2client macOS amd64 build failed with exit code $LASTEXITCODE" }
        $env:GOARCH = 'arm64'
        go build -tags release -trimpath -ldflags '-s -w' -o (Join-Path $macARM64ExecutableDir 'bd2client') .\cmd\bd2client
        if ($LASTEXITCODE -ne 0) { throw "bd2client macOS arm64 build failed with exit code $LASTEXITCODE" }
    } finally {
        $env:GOOS = $savedGOOS
        $env:GOARCH = $savedGOARCH
    }
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
    foreach ($macExecutableDir in @($macAMD64ExecutableDir, $macARM64ExecutableDir)) {
        $macPluginDir = Join-Path $macExecutableDir 'plugins'
        New-Item -ItemType Directory -Force -Path $macPluginDir | Out-Null
        Copy-Item -LiteralPath $plugin.Output -Destination (Join-Path $macPluginDir $plugin.FileName) -Force
    }
}

Copy-Item -LiteralPath (Join-Path $goRoot 'seed') -Destination $serverGoDir -Recurse -Force
Copy-Item -LiteralPath $versionConfig -Destination (Join-Path $serverPackage 'versions.json') -Force
Copy-Item -LiteralPath $authenticationConfig -Destination (Join-Path $serverPackage 'authentication.json') -Force
Copy-Item -LiteralPath $resourceConfig -Destination (Join-Path $serverPackage 'resources.json') -Force
Copy-Item -LiteralPath $versionConfig -Destination (Join-Path $clientPackage 'versions.json') -Force
foreach ($macExecutableDir in @($macAMD64ExecutableDir, $macARM64ExecutableDir)) {
    Copy-Item -LiteralPath $versionConfig -Destination (Join-Path $macExecutableDir 'versions.json') -Force
}
Copy-Item -LiteralPath (Join-Path $root 'RELEASE.md') -Destination (Join-Path $serverPackage 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'docs\AUTHENTICATION.md') -Destination (Join-Path $serverPackage 'AUTHENTICATION.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'docs\RESOURCES.md') -Destination (Join-Path $serverPackage 'RESOURCES.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'docs\CLIENT.md') -Destination (Join-Path $clientPackage 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'docs\CLIENT.md') -Destination (Join-Path $macAMD64Package 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'docs\CLIENT.md') -Destination (Join-Path $macARM64Package 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $serverPackage 'LICENSE') -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $clientPackage 'LICENSE') -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $macAMD64Package 'LICENSE') -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $macARM64Package 'LICENSE') -Force

$infoPlist = @'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleDevelopmentRegion</key><string>en</string>
<key>CFBundleDisplayName</key><string>BD2 Client Studio</string>
<key>CFBundleExecutable</key><string>bd2client</string>
<key>CFBundleIdentifier</key><string>cc.sighs.bd2.clientstudio</string>
<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
<key>CFBundleName</key><string>BD2 Client Studio</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>1.0.0</string>
<key>LSMinimumSystemVersion</key><string>11.0</string>
</dict></plist>
'@
foreach ($macPackage in @($macAMD64Package, $macARM64Package)) {
    $plistPath = Join-Path $macPackage 'BD2 Client Studio.app\Contents\Info.plist'
    [IO.File]::WriteAllText($plistPath, $infoPlist, [Text.UTF8Encoding]::new($false))
}

function Compress-MacApp {
    param([string]$Source, [string]$Destination)
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::Open($Destination, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($file in Get-ChildItem -LiteralPath $Source -File -Recurse) {
            $relative = [IO.Path]::GetRelativePath($Source, $file.FullName).Replace('\', '/')
            $entry = [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $file.FullName, $relative, [IO.Compression.CompressionLevel]::Optimal)
            if ($relative.EndsWith('/Contents/MacOS/bd2client', [StringComparison]::Ordinal) -or $relative -eq 'BD2 Client Studio.app/Contents/MacOS/bd2client') {
                $entry.ExternalAttributes = -2115174400 # Unix regular file mode 0755.
            } else {
                $entry.ExternalAttributes = -2119958528 # Unix regular file mode 0644.
            }
        }
    } finally {
        $archive.Dispose()
    }
}

Compress-Archive -LiteralPath $serverPackage -DestinationPath $serverArchive -CompressionLevel Optimal
Compress-Archive -LiteralPath $clientPackage -DestinationPath $clientArchive -CompressionLevel Optimal
Compress-MacApp -Source $macAMD64Package -Destination $macAMD64Archive
Compress-MacApp -Source $macARM64Package -Destination $macARM64Archive
Write-Host "Built pure server directory: $serverPackage"
Write-Host "Built pure server archive:   $serverArchive"
Write-Host "Built client directory:       $clientPackage"
Write-Host "Built client archive:         $clientArchive"
Write-Host "Built macOS amd64 client:     $macAMD64Archive"
Write-Host "Built macOS arm64 client:     $macARM64Archive"
