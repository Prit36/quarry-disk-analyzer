param([string]$Path)
$ErrorActionPreference = 'Stop'
$projectDirectory = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $projectDirectory
$goCommand = Get-Command go -ErrorAction SilentlyContinue
if ($goCommand) { $goExecutable = $goCommand.Source }
elseif (Test-Path -LiteralPath '.tools\go\bin\go.exe') { $goExecutable = Join-Path $projectDirectory '.tools\go\bin\go.exe' }
else { throw 'Install Go 1.27.1 or later from https://go.dev/dl/ first.' }
if ($Path) { & $goExecutable run . -path $Path }
else { & $goExecutable run . }
