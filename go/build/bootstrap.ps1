param(
    [Parameter(Mandatory)][string]$Root,
    [Parameter(Mandatory)][string]$Candidate
)

$ErrorActionPreference = 'Stop'
try {
    $inputs = [Collections.Generic.List[string]]::new()
    foreach ($directory in @('go/build', 'go/internal')) {
        foreach ($file in Get-ChildItem -LiteralPath (Join-Path $Root $directory) -Recurse -File) {
            $inputs.Add("$($file.FullName)|$($file.Length)|$($file.LastWriteTimeUtc.Ticks)")
        }
    }
    foreach ($name in @('bd2w.cmd', 'go/go.mod', 'go/go.sum')) {
        $file = Get-Item -LiteralPath (Join-Path $Root $name)
        $inputs.Add("$($file.FullName)|$($file.Length)|$($file.LastWriteTimeUtc.Ticks)")
    }
    $compiler = Get-Item -LiteralPath (Get-Command go -CommandType Application).Source
    $inputs.Add("$($compiler.FullName)|$($compiler.Length)|$($compiler.LastWriteTimeUtc.Ticks)")
    $inputs.Sort([StringComparer]::Ordinal)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $signature = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes([string]::Join("`n", $inputs)))).Replace("-", "").ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
    $tool = Join-Path $Root ('.build/tools/bd2w-' + $signature + '.exe')
    [IO.File]::WriteAllText($Candidate, $tool, [Text.Encoding]::Default)
    if (Test-Path -LiteralPath $tool -PathType Leaf) { exit 0 }
    exit 1
} catch {
    [Console]::Error.WriteLine("bd2w: cannot check build tool inputs: $($_.Exception.Message)")
    exit 2
}
