[CmdletBinding()]
param([switch]$SkipTests)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$goRoot = Join-Path $root 'go'
$haskellRoot = Join-Path $root 'haskell'
$outputDir = Join-Path $root '.build\bin'
$embedDir = Join-Path $goRoot 'internal\statebridge\tool'
$embedTool = Join-Path $embedDir 'bd2-state.exe'

$env:GOCACHE = Join-Path $goRoot '.cache\go-build'
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $outputDir, $embedDir | Out-Null
if (Test-Path -LiteralPath $embedTool) { Remove-Item -LiteralPath $embedTool -Force }

Push-Location $root
try {
    protoc --proto_path=proto --go_out=go --go_opt=module=bd2server `
        proto/bd2/state/v1/state.proto `
        proto/bd2/state/control/v1/control.proto `
        proto/bd2/state/v2/state.proto `
        proto/bd2/state/control/v2/control.proto
    if ($LASTEXITCODE -ne 0) { throw "protoc failed with exit code $LASTEXITCODE" }
} finally { Pop-Location }

Push-Location $haskellRoot
try {
    cabal build all --enable-tests
    if ($LASTEXITCODE -ne 0) { throw "cabal build failed with exit code $LASTEXITCODE" }
    if (-not $SkipTests) {
        cabal test all --test-show-details=direct
        if ($LASTEXITCODE -ne 0) { throw "cabal test failed with exit code $LASTEXITCODE" }
    }
    $haskellTool = (cabal list-bin exe:bd2-state).Trim()
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $haskellTool)) {
        throw 'cannot locate the Cabal-built bd2-state executable'
    }
    Copy-Item -LiteralPath $haskellTool -Destination $embedTool -Force
} finally { Pop-Location }

try {
    Push-Location $goRoot
    try {
        if (-not $SkipTests) {
            go test ./...
            if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
            go vet ./...
            if ($LASTEXITCODE -ne 0) { throw "go vet failed with exit code $LASTEXITCODE" }
        }
        $output = Join-Path $outputDir 'bd2server.exe'
        go build -tags embedded_state_tool -trimpath -ldflags '-s -w' -o $output .\cmd\bd2server
        if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
        Write-Host "Built single-file release: $output"
    } finally { Pop-Location }
} finally {
    if (Test-Path -LiteralPath $embedTool) { Remove-Item -LiteralPath $embedTool -Force }
}
