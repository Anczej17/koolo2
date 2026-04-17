@echo off
setlocal enabledelayedexpansion

:: Preserve only packages that break under obfuscation
:: server: html/template reflection + JSON field names
:: event: type switches in non-garbled consumers (server, discord)
set GOGARBLE=!local/internal/svc/internal/server*,!local/internal/svc/internal/event*,!github.com/inkeliz/gowebview*
set GARBLE_EXPERIMENTAL_CONTROLFLOW=1
set GOTOOLCHAIN=local

:: R1: build from neutral junction if present — strips folder-name leaks
:: from Go's panic tables (374+ literals of "Audyt Koolo/koolo2-rebranding"
:: in dist/AppService.exe otherwise). Create once:
::   powershell -Command "New-Item -Path 'C:\src\svc' -ItemType Junction -Target '%~dp0'"
if exist "C:\src\svc\build.bat" (
    cd /d "C:\src\svc"
    echo [R1] Building from neutral path C:\src\svc
) else (
    echo [R1-WARN] C:\src\svc junction not found — build may leak folder name.
)

echo Start building application
echo Cleaning up previous artifacts...
::if exist build rmdir /s /q build > NUL || goto :error

:: Generate unique identifiers
for /f "delims=" %%a in ('powershell "[guid]::NewGuid().ToString()"') do set "BUILD_ID=%%a"
for /f "delims=" %%b in ('powershell "Get-Date -Format 'o'"') do set "BUILD_TIME=%%b"

echo Generating per-build noise...
powershell -ExecutionPolicy Bypass -File "%~dp0generate_noise.ps1"

echo Building application binary...
if "%1"=="" (set VERSION=dev) else (set VERSION=%1)
garble -seed=random -literals build -a -trimpath -tags static --ldflags "-s -w -H windowsgui -X 'main._bMeta0=%BUILD_ID%' -X 'main._bMeta1=%BUILD_TIME%' -X 'local/internal/svc/internal/config.Version=%VERSION%'" -o "build\%BUILD_ID%.exe" ./cmd/app > NUL || goto :error

echo Copying assets...
mkdir build\config > NUL || goto :error
copy config\settings.yaml.dist build\config\settings.yaml  > NUL || goto :error
copy config\Settings.json build\config\Settings.json  > NUL || goto :error
xcopy /q /E /I /y config\template build\config\template  > NUL || goto :error
xcopy /q /E /I /y /EXCLUDE:tools\xcopy_exclude.txt tools build\tools > NUL || goto :error
:: README not copied to build (signature risk)

echo Done! Artifacts are in build directory.

:error
if %errorlevel% neq 0 (
    echo Error occurred #%errorlevel%.
    exit /b %errorlevel%
)
