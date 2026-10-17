param([switch]$Portable)
$ErrorActionPreference = 'Stop'
$projectDirectory = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $projectDirectory
$goCommand = Get-Command go -ErrorAction SilentlyContinue
if ($goCommand) { $goExecutable = $goCommand.Source }
elseif (Test-Path -LiteralPath '.tools\go\bin\go.exe') { $goExecutable = Join-Path $projectDirectory '.tools\go\bin\go.exe' }
else { throw 'Install Go 1.27.1 or later from https://go.dev/dl/ first.' }
$env:CGO_ENABLED = '0'
& $goExecutable test ./...
if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
& $goExecutable vet ./...
if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
& $goExecutable tool mygo vet
if ($LASTEXITCODE -ne 0) { throw 'MyGO UI lifetime checks failed' }
if ($Portable) {
    New-Item -ItemType Directory -Force -Path 'build\portable' | Out-Null
    & $goExecutable build -trimpath -ldflags '-s -w -H=windowsgui' -o 'build\portable\Quarry.exe' .
    if ($LASTEXITCODE -ne 0) { throw 'Compilation failed' }
    New-Item -ItemType Directory -Force -Path 'build\portable\licenses' | Out-Null
    Copy-Item -Path 'resources\licenses\*' -Destination 'build\portable\licenses' -Force
} else {
    & $goExecutable tool mygo build
    if ($LASTEXITCODE -ne 0) { throw 'Packaging failed' }
}
