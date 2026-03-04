Set-Location $PSScriptRoot

# Generate noise
& powershell -ExecutionPolicy Bypass -File ".\generate_noise.ps1"

$bid = [guid]::NewGuid().ToString()
$bt = Get-Date -Format 'o'

Write-Host "BUILD_ID=$bid"
Write-Host "Starting garble build..."

if (!(Test-Path build)) { New-Item -ItemType Directory -Path build | Out-Null }

$output = "build\$bid.exe"

# Use CMD to set GOGARBLE with ! characters (PowerShell doesn't need escaping for !)
# The ! prefix means "exclude this package from obfuscation"
$env:GOGARBLE = "github.com/hectorgimenez/koolo/internal/server*;github.com/hectorgimenez/koolo/internal/event*;github.com/inkeliz/gowebview*"

# Build via CMD wrapper to properly handle GOGARBLE with ! exclusion syntax
cmd.exe /c "set `"GOGARBLE=!github.com/hectorgimenez/koolo/internal/server*,!github.com/hectorgimenez/koolo/internal/event*,!github.com/inkeliz/gowebview*`" && garble -literals -tiny -seed=random build -a -trimpath -tags static --ldflags `"-s -w -H windowsgui -X 'main._bMeta0=$bid' -X 'main._bMeta1=$bt' -X 'github.com/hectorgimenez/koolo/internal/config.Version=dev'`" -o $output ./cmd/koolo"

if ($LASTEXITCODE -eq 0) {
    Write-Host "BUILD SUCCESS: $output" -ForegroundColor Green

    # Copy tools
    if (Test-Path build\tools) { Remove-Item -Recurse -Force build\tools }
    Copy-Item -Recurse tools build\tools

    # Copy config
    if (!(Test-Path build\config)) { New-Item -ItemType Directory -Path build\config | Out-Null }
    if (!(Test-Path build\config\ctfmon.yaml)) {
        Copy-Item config\ctfmon.yaml.dist build\config\ctfmon.yaml
    }
    if (!(Test-Path build\config\Settings.json)) {
        Copy-Item config\Settings.json build\config\Settings.json
    }
    if (Test-Path build\config\template) { Remove-Item -Recurse -Force build\config\template }
    Copy-Item -Recurse config\template build\config\template

    Write-Host "Build artifacts ready in build/" -ForegroundColor Green
} else {
    Write-Host "BUILD FAILED with exit code $LASTEXITCODE" -ForegroundColor Red
    exit 1
}
