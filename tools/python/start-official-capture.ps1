param(
    [Parameter(Mandatory)]
    [string]$GameDir,

    [Parameter(Mandatory)]
    [string]$ResourceRoot,

    [ValidatePattern('^\d{14}$')]
    [string]$BundleVersion,

    [ValidatePattern('^\d{14}$')]
    [string]$GameDataVersion,

    [string]$VersionConfig = (Join-Path $PSScriptRoot '..\..\versions.json'),

    [string]$SharedDataDir,

    [switch]$Wait
)

$ErrorActionPreference = 'Stop'

$VersionConfig = [IO.Path]::GetFullPath($VersionConfig)
if (-not (Test-Path -LiteralPath $VersionConfig -PathType Leaf)) {
    throw "Version config is missing: $VersionConfig"
}
$versions = Get-Content -LiteralPath $VersionConfig -Raw | ConvertFrom-Json
if (-not $BundleVersion) { $BundleVersion = [string]$versions.bundle_version }
if (-not $GameDataVersion) { $GameDataVersion = [string]$versions.game_data_version }
if ($BundleVersion -notmatch '^\d{14}$' -or $GameDataVersion -notmatch '^\d{14}$') {
    throw "Invalid resource version in $VersionConfig"
}

$GameDir = [IO.Path]::GetFullPath($GameDir)
$ResourceRoot = [IO.Path]::GetFullPath($ResourceRoot)
if (-not $SharedDataDir) {
    $localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    $SharedDataDir = Join-Path (Split-Path -Parent $localAppData) 'LocalLow\Gamfs\BrownDust II'
}
$SharedDataDir = [IO.Path]::GetFullPath($SharedDataDir)

$exe = Join-Path $GameDir 'BrownDust II.exe'
$pluginDir = Join-Path $GameDir 'BepInEx\plugins'
$localRes = Join-Path $pluginDir 'PluginLocalRes.dll'
$capturePlugin = Join-Path $pluginDir 'BD2CaptureEnvironment.dll'
$identityPlugin = Join-Path $pluginDir 'BD2LocalIdentity.dll'
$doorstop = Join-Path $GameDir 'doorstop_config.ini'
$isolatedData = Join-Path $GameDir 'IsolatedUserData'
$stagedCapturePlugin = Join-Path $PSScriptRoot '..\..\plugins\CaptureEnvironment\bin\Release\netstandard2.1\BD2CaptureEnvironment.dll'
$stagedLocalResConfig = Join-Path $PSScriptRoot '..\..\plugins\CaptureEnvironment\bd2.localres.cfg'
$localResConfig = Join-Path $GameDir 'BepInEx\config\bd2.localres.cfg'

if (Get-Process -Name 'BrownDust II' -ErrorAction SilentlyContinue) {
    throw 'BrownDust II is already running. Exit it before starting the isolated official client.'
}

foreach ($requiredDirectory in @($GameDir, $ResourceRoot, $SharedDataDir)) {
    if (-not (Test-Path -LiteralPath $requiredDirectory -PathType Container)) {
        throw "Required directory is missing: $requiredDirectory"
    }
}

if (Test-Path -LiteralPath $stagedCapturePlugin -PathType Leaf) {
    $installedHash = if (Test-Path -LiteralPath $capturePlugin -PathType Leaf) {
        (Get-FileHash -LiteralPath $capturePlugin -Algorithm SHA256).Hash
    }
    $stagedHash = (Get-FileHash -LiteralPath $stagedCapturePlugin -Algorithm SHA256).Hash
    if ($installedHash -ne $stagedHash) {
        New-Item -ItemType Directory -Force -Path $pluginDir | Out-Null
        Copy-Item -LiteralPath $stagedCapturePlugin -Destination $capturePlugin -Force
        Write-Host "Updated capture plugin from staged build ($stagedHash)."
    }
}

foreach ($required in @($exe, $doorstop, $localRes, $capturePlugin)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required file is missing: $required"
    }
}

if (-not (Test-Path -LiteralPath $stagedLocalResConfig -PathType Leaf)) {
    throw "Local-resource config is missing: $stagedLocalResConfig"
}
$resourceManifest = Join-Path $ResourceRoot 'resource-fetch-manifest.json'
if (-not (Test-Path -LiteralPath $resourceManifest -PathType Leaf)) {
    throw "Resource download is incomplete; manifest is missing: $resourceManifest"
}
$resourceState = Get-Content -LiteralPath $resourceManifest -Raw | ConvertFrom-Json
if ($resourceState.bundle_version -ne $BundleVersion -or
    $resourceState.game_data_version -ne $GameDataVersion) {
    throw "Resource manifest version mismatch: $resourceManifest"
}
$catalogDir = Join-Path $ResourceRoot "ServerData\StandaloneWindows64\HD\$BundleVersion"
$catalogJson = Join-Path $catalogDir 'catalog_alpha.json'
$catalogHash = Join-Path $catalogDir 'catalog_alpha.hash'
foreach ($catalogFile in @($catalogJson, $catalogHash)) {
    if (-not (Test-Path -LiteralPath $catalogFile -PathType Leaf)) {
        throw "Addressables catalog is incomplete: $catalogFile"
    }
}
$actualCatalogHash = (Get-Content -LiteralPath $catalogHash -Raw).Trim().ToLowerInvariant()
if ($actualCatalogHash -notmatch '^[0-9a-f]{32}$' -or
    [string]$resourceState.server_data.catalog_hash -ne $actualCatalogHash) {
    throw "Addressables catalog hash does not match the resource manifest: $catalogHash"
}
$localResText = Get-Content -LiteralPath $stagedLocalResConfig -Raw
$localResText = $localResText.Replace('<RESOURCE_ROOT>', $ResourceRoot)
$localResText = $localResText.Replace('<BUNDLE_VERSION>', $BundleVersion)
$localResText = $localResText.Replace('<GAME_DATA_VERSION>', $GameDataVersion)
[IO.File]::WriteAllText($localResConfig, $localResText, [Text.UTF8Encoding]::new($false))

if (Test-Path -LiteralPath $identityPlugin) {
    throw "Unsafe plugin found in official environment: $identityPlugin"
}

$unexpectedIdentity = Get-ChildItem -LiteralPath (Join-Path $GameDir 'BepInEx') `
    -Recurse -File -Filter 'BD2LocalIdentity.dll' -ErrorAction SilentlyContinue
if ($unexpectedIdentity) {
    throw "BD2LocalIdentity.dll must not exist anywhere in the official environment."
}

$sharedStaticPaths = @{
    (Join-Path $isolatedData 'SoundData') = (Join-Path $SharedDataDir 'SoundData')
    (Join-Path $isolatedData 'VideoData') = (Join-Path $SharedDataDir 'VideoData')
}
$bundledAA = Join-Path $GameDir 'BrownDust II_Data\StreamingAssets\aa'
$bundledItem = Get-Item -LiteralPath $bundledAA -Force -ErrorAction SilentlyContinue
if (-not $bundledItem -or -not $bundledItem.PSIsContainer -or $bundledItem.LinkType) {
    throw "Bundled StreamingAssets/aa must be a real directory: $bundledAA"
}
New-Item -ItemType Directory -Force -Path $isolatedData | Out-Null
$isolatedCatalogDir = Join-Path $isolatedData 'com.unity.addressables'
New-Item -ItemType Directory -Force -Path $isolatedCatalogDir | Out-Null
# The packaged settings.json names this exact persistent catalog cache.  Stage
# both files together so a previously written bundle_version PlayerPrefs value
# cannot leave the bundled catalog paired with a newer ServerData directory.
Copy-Item -LiteralPath $catalogJson `
    -Destination (Join-Path $isolatedCatalogDir 'catalog_alpha.json') -Force
Copy-Item -LiteralPath $catalogHash `
    -Destination (Join-Path $isolatedCatalogDir 'catalog_alpha.hash') -Force
foreach ($link in $sharedStaticPaths.GetEnumerator()) {
    $item = Get-Item -LiteralPath $link.Key -Force -ErrorAction SilentlyContinue
    if (-not (Test-Path -LiteralPath $link.Value -PathType Container)) {
        throw "Shared static-data source is missing: $($link.Value)"
    }
    if (-not $item) {
        New-Item -ItemType Junction -Path $link.Key -Target $link.Value | Out-Null
        $item = Get-Item -LiteralPath $link.Key -Force
    }
    if ($item.LinkType -ne 'Junction') {
        throw "Static-data destination must be a junction: $($link.Key)"
    }
    $actualTarget = [IO.Path]::GetFullPath([string]$item.Target)
    $expectedTarget = [IO.Path]::GetFullPath([string]$link.Value)
    if ($actualTarget -ne $expectedTarget) {
        throw "Unexpected junction target for $($link.Key): $actualTarget"
    }
}

$unityLog = Join-Path $isolatedData 'Player.log'
$process = Start-Process -FilePath $exe -WorkingDirectory $GameDir `
    -ArgumentList @(
        '-logFile', ('"' + $unityLog + '"')
    ) -PassThru
Start-Sleep -Milliseconds 500
$actualProcess = Get-Process -Id $process.Id -ErrorAction SilentlyContinue
if ($actualProcess -and
    -not [string]::Equals(
        [IO.Path]::GetFullPath($actualProcess.Path),
        [IO.Path]::GetFullPath($exe),
        [StringComparison]::OrdinalIgnoreCase)) {
    Stop-Process -Id $process.Id -Force
    throw "Wrong client executable started: $($actualProcess.Path)"
}
Write-Host "Started isolated official client (PID $($process.Id))."
Write-Host "BepInEx log: $(Join-Path $GameDir 'BepInEx\LogOutput.log')"
Write-Host "Capture root: $(Join-Path $GameDir 'Capture')"
Write-Host "Isolated data: $isolatedData"

if ($Wait) {
    $process.WaitForExit()
    Write-Host "Client exited with code $($process.ExitCode)."
    $latest = Get-ChildItem -LiteralPath (Join-Path $GameDir 'Capture') `
        -Directory -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1
    if ($latest) {
        Write-Host "Latest capture: $($latest.FullName)"
    }
}
