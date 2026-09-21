param(
    [Parameter(Mandatory)]
    [string]$GameDir,

    [Parameter(Mandatory)]
    [string]$LocalClientDir,

    [string]$SharedDataDir,

    [switch]$Wait
)

$ErrorActionPreference = 'Stop'

$GameDir = [IO.Path]::GetFullPath($GameDir)
$LocalClientDir = [IO.Path]::GetFullPath($LocalClientDir)
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
$sharedGameData = Join-Path $SharedDataDir 'Data\t'
$stagedCapturePlugin = Join-Path $PSScriptRoot '..\..\plugins\CaptureEnvironment\bin\Release\netstandard2.1\BD2CaptureEnvironment.dll'

if (Get-Process -Name 'BrownDust II' -ErrorAction SilentlyContinue) {
    throw 'BrownDust II is already running. Exit it before starting the isolated official client.'
}

foreach ($requiredDirectory in @($GameDir, $LocalClientDir, $SharedDataDir, $sharedGameData)) {
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
    (Join-Path $isolatedData 'com.unity.addressables') = (Join-Path $SharedDataDir 'com.unity.addressables')
    (Join-Path $isolatedData 'VideoData') = (Join-Path $SharedDataDir 'VideoData')
    (Join-Path $GameDir 'BrownDust II_Data\StreamingAssets\aa') = (Join-Path $LocalClientDir 'BrownDust II_Data\StreamingAssets\aa')
}
foreach ($link in $sharedStaticPaths.GetEnumerator()) {
    $item = Get-Item -LiteralPath $link.Key -Force -ErrorAction SilentlyContinue
    if (-not $item -or $item.LinkType -ne 'Junction') {
        throw "Shared static-data junction is missing: $($link.Key)"
    }
    $actualTarget = [IO.Path]::GetFullPath([string]$item.Target)
    $expectedTarget = [IO.Path]::GetFullPath([string]$link.Value)
    if ($actualTarget -ne $expectedTarget) {
        throw "Unexpected junction target for $($link.Key): $actualTarget"
    }
}

New-Item -ItemType Directory -Force -Path $isolatedData | Out-Null
$unityLog = Join-Path $isolatedData 'Player.log'
$process = Start-Process -FilePath $exe -WorkingDirectory $GameDir `
    -ArgumentList @(
        '-logFile', ('"' + $unityLog + '"'),
        '-bd2SharedGameData', ('"' + $sharedGameData + '"')
    ) -PassThru
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
