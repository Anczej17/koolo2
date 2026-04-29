@echo off
setlocal enabledelayedexpansion

:: Preserve only packages that break under obfuscation
:: server: html/template reflection + JSON field names
:: event: type switches in non-garbled consumers (server, discord)
set GOGARBLE=*,!local/internal/svc/internal/server*,!local/internal/svc/internal/event*,!github.com/inkeliz/gowebview*
set GARBLE_EXPERIMENTAL_CONTROLFLOW=1
set GOTOOLCHAIN=local

:: Required versions
set REQUIRED_GO_VERSION=1.25
set REQUIRED_GARBLE_VERSION=0.15.0

:: Change to the script's directory (via neutral junction if present)
::
:: R1 mitigation: Go embeds absolute source-file paths into the runtime panic
:: tables even with -trimpath when the module lives outside GOPATH. If the
:: folder name contains "Audyt Koolo / koolo2-rebranding", those literals end
:: up in dist/AppService.exe — 374+ leaks, trivial to YARA-signature.
::
:: Solution: build from a neutral junction at C:\src\svc\. Create once:
::   powershell -Command "New-Item -Path 'C:\src\svc' -ItemType Junction -Target 'C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding'"
::
:: If the junction exists, cd there; otherwise fall back to script dir and
:: warn so the operator knows to create it.
if exist "C:\src\svc\better_build.bat" (
    cd /d "C:\src\svc"
    echo [R1] Building from neutral path C:\src\svc
) else (
    cd /d "%~dp0"
    echo [R1-WARN] C:\src\svc junction not found — build may leak folder name.
    echo [R1-WARN] Create it with: powershell -Command "New-Item -Path 'C:\src\svc' -ItemType Junction -Target '%~dp0'"
)

:: Use a static build folder to avoid temp paths being flagged by AV
set "STATIC_BUILD_DIR=%cd%\build\tmp"
if not exist "%STATIC_BUILD_DIR%" mkdir "%STATIC_BUILD_DIR%"
set "GOCACHE=%STATIC_BUILD_DIR%\gocache"
set "GOTMPDIR=%STATIC_BUILD_DIR%"
call :print_info "Using static build folder: %STATIC_BUILD_DIR%"

call :print_header "Starting Application Build Process"
set "LAB_TOOLS=0"
if /i "%2"=="--lab-tools" set "LAB_TOOLS=1"
if /i "%1"=="--lab-tools" (
    set "LAB_TOOLS=1"
    set "VERSION=dev"
)

:: Check for Go installation
call :check_go_installation
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

:: Check for Garble installation
call :check_garble_installation
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

:: Check for Rust/Cargo installation (required for rmod.dll)
call :check_cargo_installation
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

:: Main script execution
call :main %*
if !errorlevel! neq 0 (
    call :print_error "Build process failed with error code !errorlevel!"
    call :pause_and_exit !errorlevel!
)
echo.
powershell -Command "Write-Host 'Press any key to exit...' -ForegroundColor Yellow"
pause > nul
exit /b 0

:check_go_installation
call :print_info "Checking if Go is installed"
where go >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error "Go is not installed or not in the system PATH."
    call :print_info "You can download Go from https://golang.org/dl/"
    call :get_user_input "Do you want to attempt automatic installation using Chocolatey? (Y/N) " install_go
    if /i "!install_go!"=="Y" (
        call :install_go_with_chocolatey
    ) else (
        call :print_info "Please install Go manually and run this script again."
        call :pause_and_exit 1
    )
) else (
    :: Extract Go version and check if it matches required version
    for /f "tokens=3" %%v in ('go version') do set go_version_full=%%v
    set go_version=!go_version_full:go=!

    :: Extract major.minor version (e.g., 1.24 from 1.24.3)
    for /f "tokens=1,2 delims=." %%a in ("!go_version!") do set go_major_minor=%%a.%%b

    if "!go_major_minor!"=="%REQUIRED_GO_VERSION%" (
        call :print_success "Go version !go_version! is installed."
    ) else (
        call :print_warning "You are currently using Go version !go_version!"
        call :print_warning "The recommended version for this build is Go %REQUIRED_GO_VERSION%"
        call :print_info "Please consider downloading Go %REQUIRED_GO_VERSION% from https://golang.org/dl/"
        call :print_info "Using a different Go version may cause compatibility issues."
        echo.
        call :get_user_input "Do you want to continue anyway? (Y/N) " continue_with_go
        if /i "!continue_with_go!" neq "Y" (
            call :print_info "Build process cancelled. Please install Go %REQUIRED_GO_VERSION% and try again."
            call :pause_and_exit 1
        )
    )
)
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!
goto :eof

:check_garble_installation
call :print_info "Checking if Garble is installed"
where garble >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error "Garble is not installed or not in the system PATH."
    call :print_info "You can install Garble using: go install mvdan.cc/garble@v%REQUIRED_GARBLE_VERSION%"
    call :get_user_input "Do you want to attempt automatic installation? (Y/N) " install_garble
    if /i "!install_garble!"=="Y" (
        call :install_garble
    ) else (
        call :print_info "Please install Garble manually and run this script again."
        call :pause_and_exit 1
    )
) else (
    :: Check Garble version
    for /f "tokens=1,2" %%a in ('garble version 2^>^&1') do (
        if "%%a"=="mvdan.cc/garble" set garble_version_with_v=%%b
    )

    :: Remove the 'v' prefix
    set garble_version=!garble_version_with_v:~1!

    :: Extract exact version
    for /f "tokens=1,2,3 delims=." %%a in ("!garble_version!") do set garble_major_minor=%%a.%%b.%%c

    if "!garble_major_minor!"=="%REQUIRED_GARBLE_VERSION%" (
        call :print_success "Garble version !garble_version! is installed."
    ) else (
        call :print_warning "You are currently using Garble version !garble_version!"
        call :print_warning "The recommended version for this build is Garble %REQUIRED_GARBLE_VERSION%"
        call :print_info "Please consider installing Garble %REQUIRED_GARBLE_VERSION% using:"
        call :print_info "go install mvdan.cc/garble@v%REQUIRED_GARBLE_VERSION%"
        call :print_info "Using a different Garble version may cause build issues."
        echo.
        call :get_user_input "Do you want to continue anyway? (Y/N) " continue_with_garble
        if /i "!continue_with_garble!" neq "Y" (
            call :print_info "Build process cancelled. Please install Garble %REQUIRED_GARBLE_VERSION% and try again."
            call :pause_and_exit 1
        )
    )
)
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!
goto :eof

:install_garble
call :print_step "Attempting to install Garble %REQUIRED_GARBLE_VERSION%..."
go install mvdan.cc/garble@v%REQUIRED_GARBLE_VERSION% >nul 2>&1
where garble >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error "Failed to install Garble. Please install it manually."
    call :print_info "Run: go install mvdan.cc/garble@v%REQUIRED_GARBLE_VERSION%"
    call :pause_and_exit 1
) else (
    call :print_success "Garble %REQUIRED_GARBLE_VERSION% has been successfully installed."
)
goto :eof

:install_go_with_chocolatey
call :print_step "Attempting to install Go using Chocolatey..."
where choco >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error "Chocolatey is not installed. Please install Go manually."
    call :print_info "You can install Chocolatey from https://chocolatey.org/install"
    call :pause_and_exit 1
)
powershell -Command "Start-Process powershell -Verb runAs -ArgumentList 'choco install golang -y' -Wait"
where go >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error "Failed to install Go. Please install it manually."
    call :pause_and_exit 1
) else (
    call :print_success "Go has been successfully installed."
)
goto :eof

:check_cargo_installation
call :print_info "Checking if Cargo is installed"
where cargo >nul 2>&1
if %errorlevel% neq 0 (
    call :print_error "Cargo is not installed or not in the system PATH."
    call :print_info "Install Rust from https://rustup.rs/ and run this script again."
    exit /b 1
)
cargo --version >nul 2>&1
if !errorlevel! neq 0 (
    call :print_error "Cargo exists but failed to run"
    exit /b 1
)
call :print_success "Cargo is installed."
goto :eof

:main
:: Initial validation checks
call :validate_environment
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

call :clean_release_artifacts
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!
if not exist "%STATIC_BUILD_DIR%" mkdir "%STATIC_BUILD_DIR%"
if not exist "%GOCACHE%" mkdir "%GOCACHE%"

call :build_native_tools
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

:: Build application binary with Garble
call :print_header "Building Application Binary"
if "%1"=="" (set VERSION=dev) else (set VERSION=%1)
if /i "%1"=="--lab-tools" set VERSION=dev
call :print_info "Building %VERSION%"
:: Generate unique build identifiers
for /f "delims=" %%a in ('powershell -Command "[guid]::NewGuid().ToString()"') do set "BUILD_ID=%%a"
for /f "delims=" %%b in ('powershell -Command "Get-Date -Format 'o'"') do set "BUILD_TIME=%%b"

:: Set the expected output executable path
set "OUTPUT_EXE=build\%BUILD_ID%.exe"

:: Build an obfuscated application binary
call :print_step "Compiling Obfuscated application executable"
call :print_step "Generating per-build noise..."
powershell -ExecutionPolicy Bypass -File "%~dp0generate_noise.ps1"
(
    garble -seed=random -literals build -a -trimpath -tags static --ldflags "-s -w -H windowsgui -X 'main._bMeta0=%BUILD_ID%' -X 'main._bMeta1=%BUILD_TIME%' -X 'local/internal/svc/internal/config.Version=%VERSION%'" -o "%OUTPUT_EXE%" ./cmd/app 2>&1
) > garble.log
set "GARBLE_EXIT_CODE=!errorlevel!"

if !GARBLE_EXIT_CODE! neq 0 (
    call :print_error "Garble build failed. These logs may be useful:"
    for /f "usebackq delims=" %%l in (`type garble.log`) do (
        call :print_error "%%l"
    )
) else (
    :: Capture and style seed information
    for /f "tokens=4" %%s in ('findstr /C:"-seed chosen at random:" garble.log') do (
        call :print_step "Obfuscation seed: !BUILD_ID!"
    )
)
del garble.log
if exist "%STATIC_BUILD_DIR%" (
    call :print_step "Cleaning up temporary build folder"
    rmdir /s /q "%STATIC_BUILD_DIR%"
)

:: Check if the executable was actually created
if exist "%OUTPUT_EXE%" (
    call :print_success "Successfully built obfuscated executable: %BUILD_ID%.exe"
) else (
    call :print_error "Failed to build application binary - executable was not created"
    echo.
    call :print_warning "Please verify the following:"
    call :print_info "- Are you using the correct Go version? (Recommended: %REQUIRED_GO_VERSION%)"
    call :print_info "- Are you using the correct Garble version? (Recommended: %REQUIRED_GARBLE_VERSION%)"
    call :print_info "- Have you added your application folder to the exclusion list in your Anti-Virus software?"
    call :print_info "- Have you tried temporarily disabling your Anti-Virus completely?"
    echo.
    call :print_info "Anti-Virus software can sometimes interfere with the compilation process."
    call :print_info "If the issue persists, please check the compilation errors above."
    call :pause_and_exit 1
)

call :audit_obfuscated_binary "%OUTPUT_EXE%"
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

call :copy_runtime_tools
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

call :validate_release_tools
if !errorlevel! neq 0 call :pause_and_exit !errorlevel!

:: Handle Settings.json
call :print_header "Handling Configuration Files"
if not exist build\config mkdir build\config
if exist build\config\Settings.json (
    call :print_step "Checking Settings.json"
    call :print_info "Settings.json found in %cd%\build\config"
    call :get_user_input "    Do you want to replace it? (Y/N) " replace_settings
    if /i "!replace_settings!"=="Y" (
        call :print_step "Replacing Settings.json"
        copy /y config\Settings.json build\config\Settings.json > nul
        if !errorlevel! equ 0 (
            call :print_success "Settings.json successfully replaced"
        ) else (
            call :print_error "Failed to copy Settings.json"
            call :pause_and_exit 1
        )
    ) else (
        call :print_info "Keeping existing Settings.json"
    )
) else (
    call :print_info "No existing Settings.json found in %cd%\build\config"
    call :print_step "Copying Settings.json"
    copy /y config\Settings.json build\config\Settings.json > nul
    if !errorlevel! neq 0 (
        call :print_error "Failed to copy Settings.json"
        call :pause_and_exit 1
    )
    call :print_success "Settings.json successfully copied"
)

:: Handle settings.yaml
if not exist build\config\settings.yaml (
    call :print_step "Copying settings.yaml.dist"
    copy config\settings.yaml.dist build\config\settings.yaml > nul
    if !errorlevel! neq 0 (
        call :print_error "Failed to copy settings.yaml.dist"
        call :pause_and_exit 1
    )
    call :print_success "settings.yaml.dist successfully copied"
) else (
    call :print_info "settings.yaml already exists in build\config, skipping copy"
)

:: Copy template folder
call :print_step "Copying template folder"
if exist build\config\template rmdir /s /q build\config\template
xcopy /q /E /I /y config\template build\config\template > nul
if !errorlevel! neq 0 (
    call :print_error "Failed to copy template folder"
    call :pause_and_exit 1
)
call :print_success "Template folder successfully copied"

:: README not copied to build (signature risk)

call :print_header "Build Process Completed"
call :print_success "Artifacts are in the build directory"
goto :eof

:clean_release_artifacts
call :print_header "Cleaning Release Artifacts"
if not exist build mkdir build

for %%d in (
    "build\build"
    "build\dumps"
    "build\ghidra_projects"
    "build\lab_tools"
    "build\logs"
    "build\screenshots"
    "build\tmp"
    "build\uber_dumps"
    "build\d2txt"
    "build\d2rb_resources"
) do (
    if exist %%~d (
        call :print_step "Removing stale folder %%~d"
        rmdir /s /q %%~d
        if exist %%~d (
            call :print_error "Failed to delete stale folder %%~d"
            exit /b 1
        )
    )
)

for %%f in (
    "build\*.exe"
    "build\*.log"
    "build\*.json"
    "build\*.go"
    "build\*.bin"
    "build\*.pdb"
) do (
    if exist %%~f (
        call :print_step "Removing stale files %%~f"
        del /q %%~f >nul 2>&1
        if exist %%~f (
            call :print_warning "Some stale files matching %%~f could not be removed. Close running binaries if this is a release build."
        )
    )
)

call :print_success "Release artifact cleanup completed"
goto :eof

:build_native_tools
call :print_header "Building Native Tools"

call :print_step "Building rmod.dll"
pushd tools\rmod
cargo build --release
if !errorlevel! neq 0 (
    popd
    call :print_error "cargo build --release failed for rmod.dll"
    exit /b 1
)
popd
copy /y tools\rmod\target\release\rmod.dll tools\rmod.dll >nul
if !errorlevel! neq 0 (
    call :print_error "Failed to copy tools\rmod.dll"
    exit /b 1
)
if "%LAB_TOOLS%"=="1" (
    call :print_step "Building lab-only rmod_sniffer.dll"
    pushd tools\rmod
    cargo build --release --features sniffer
    if !errorlevel! neq 0 (
        popd
        call :print_error "cargo build --release --features sniffer failed"
        exit /b 1
    )
    popd
    copy /y tools\rmod\target\release\rmod.dll tools\rmod_sniffer.dll >nul
    if !errorlevel! neq 0 (
        call :print_error "Failed to copy tools\rmod_sniffer.dll"
        exit /b 1
    )

    call :print_step "Building lab-only offset_resolver.exe"
    go build -trimpath -ldflags "-s -w" -o tools\offset_resolver.exe ./tools/offset_resolver
    if !errorlevel! neq 0 (
        call :print_error "Failed to build tools\offset_resolver.exe"
        exit /b 1
    )
)

call :print_success "Native tools built"
goto :eof

:copy_runtime_tools
call :print_header "Handling Runtime Tools"
if exist build\tools (
    call :print_step "Removing existing runtime tools folder"
    rmdir /s /q build\tools
    if exist build\tools (
        call :print_error "Failed to delete build\tools"
        call :check_folder_permissions "build\tools"
        exit /b 1
    )
)
mkdir build\tools

for %%f in (
    "handle64.exe"
    "mapsvc.exe"
    "rmod.dll"
) do (
    call :print_step "Copying runtime tool %%~f"
    copy /y "tools\%%~f" "build\tools\%%~f" >nul
    if !errorlevel! neq 0 (
        call :print_error "Failed to copy tools\%%~f"
        exit /b 1
    )
)

if "%LAB_TOOLS%"=="1" (
    call :print_step "Creating separate lab_tools folder"
    mkdir build\lab_tools
    for %%f in (
        "rmod_sniffer.dll"
        "offset_resolver.exe"
    ) do (
        copy /y "tools\%%~f" "build\lab_tools\%%~f" >nul
        if !errorlevel! neq 0 (
            call :print_error "Failed to copy lab tool tools\%%~f"
            exit /b 1
        )
    )
)

call :print_success "Runtime tools copied"
goto :eof

:audit_obfuscated_binary
call :print_header "Auditing Obfuscated Binary"
where python >nul 2>&1
if !errorlevel! neq 0 (
    call :print_warning "Python is not in PATH; skipping audit_binary_leaks.py"
    exit /b 0
)
python tools\audit_binary_leaks.py %~1
if !errorlevel! neq 0 (
    call :print_error "Binary leak audit failed for %~1"
    exit /b 1
)
call :print_success "Binary leak audit passed"
goto :eof

:validate_release_tools
call :print_header "Validating Release Tools"
for %%f in (
    "build\tools\handle64.exe"
    "build\tools\mapsvc.exe"
    "build\tools\rmod.dll"
) do (
    if not exist %%~f (
        call :print_error "Missing required release tool %%~f"
        exit /b 1
    )
)
if exist build\tools\rmod_sniffer.dll (
    call :print_error "Production runtime tools contain rmod_sniffer.dll"
    exit /b 1
)
if exist build\tools\offset_resolver.exe (
    call :print_error "Production runtime tools contain offset_resolver.exe"
    exit /b 1
)
call :print_success "Release tools validated"
goto :eof

:: Function to pause and exit with error code
:pause_and_exit
echo.
powershell -Command "Write-Host 'Press any key to exit...' -ForegroundColor Yellow"
pause > nul
exit %1

:: Function to get user input
:get_user_input
setlocal enabledelayedexpansion
call :print_prompt "%~1"
set /p "user_input="
endlocal & set "%~2=%user_input%"
goto :eof

:: Function to print a colored prompt
:print_prompt
powershell -Command "Write-Host '%~1' -ForegroundColor Yellow -NoNewline"
goto :eof

:: Function to print a header
:print_header
echo.
powershell -Command "Write-Host '=== %~1 ===' -ForegroundColor Magenta"
echo.
goto :eof

:: Function to print a step
:print_step
powershell -Command "Write-Host '  - %~1' -ForegroundColor Cyan"
goto :eof

:: Function to print a success message
:print_success
powershell -Command "Write-Host '    SUCCESS: %~1' -ForegroundColor Green"
goto :eof

:: Function to print an error message
:print_error
powershell -Command "Write-Host '    ERROR: %~1' -ForegroundColor Red"
goto :eof

:: Function to print an info message
:print_info
powershell -Command "Write-Host '    INFO: %~1' -ForegroundColor Yellow"
goto :eof

:: Function to print a warning message
:print_warning
powershell -Command "Write-Host '    WARNING: %~1' -ForegroundColor Yellow"
goto :eof

:: Function to check folder permissions
:check_folder_permissions
dir "%~1\*" >nul 2>&1
if !errorlevel! neq 0 (
    call :print_error "Cannot access directory: %~1"
) else (
    call :print_info "Directory %~1 is accessible"
)
goto :eof

:: Function to validate environment
:validate_environment
call :print_header "Validating Environment"

:: Check for required source files and folders
if not exist config (
    call :print_error "Config directory is missing"
    exit /b 1
)

if not exist config\settings.yaml.dist (
    call :print_error "settings.yaml.dist is missing from config directory"
    exit /b 1
)

if not exist config\Settings.json (
    call :print_error "Settings.json is missing from config directory"
    exit /b 1
)

if not exist tools (
    call :print_error "Tools directory is missing"
    exit /b 1
)

:: Check for required tools
if not exist tools\handle64.exe (
    call :print_error "handle64.exe is missing from tools directory"
    exit /b 1
)

if not exist tools\mapsvc.exe (
    call :print_error "mapsvc.exe is missing from tools directory"
    exit /b 1
)

:: Check for required build dependencies
call :print_step "Checking build dependencies"
go version >nul 2>&1
if !errorlevel! neq 0 (
    call :print_error "Go is not installed or not in PATH"
    exit /b 1
)

:: Verify write permissions in current directory
call :print_step "Checking write permissions"
echo. > test_write.tmp 2>nul
if !errorlevel! neq 0 (
    call :print_error "No write permissions in current directory"
    exit /b 1
)
del test_write.tmp >nul 2>&1

call :print_success "Environment validation completed"
goto :eof
