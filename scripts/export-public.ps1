param()
$ErrorActionPreference = 'Stop'
$projectDirectory = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $projectDirectory
try {
    # Explicit additions make this usable before the publication changes are committed.
    $files = @(& git ls-files) + @('LICENSE', 'docs/PUBLISHING.md', 'scripts/export-public.ps1', 'resources/licenses/Quarry-LICENSE.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Could not list tracked source files.' }
    $outputDirectory = Join-Path $projectDirectory ('build\public-source-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $outputDirectory | Out-Null
    foreach ($relative in ($files | Sort-Object -Unique)) {
        if ($relative -match '(^|/)(\.git|\.claude|\.tools|build)(/|$)|(^|/)\.env($|\.)|\.(pem|key|pfx|p12|quarry|stratum)$|(^|/)brand\.png$') {
            throw "Unexpected private or historical file in source list: $relative"
        }
        $source = Join-Path $projectDirectory $relative
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { continue }
        $destination = Join-Path $outputDirectory $relative
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
        Copy-Item -LiteralPath $source -Destination $destination
    }
    Write-Output "Public source snapshot (no Git history): $outputDirectory"
    Write-Output 'Review the snapshot, then initialize a new repository there. No remote was changed.'
} finally {
    Pop-Location
}
