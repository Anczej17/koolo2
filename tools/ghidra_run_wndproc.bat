@echo off
REM Headless Ghidra runner for wndproc / click chain RE.

set "JAVA_HOME=C:\Users\Administrator\Downloads\OpenJDK25U-jdk_x64_windows_hotspot_25.0.2_10\jdk-25.0.2+10"
set "PATH=%JAVA_HOME%\bin;%PATH%"
set "GHIDRA=C:\Users\Administrator\Downloads\ghidra_12.0.4_PUBLIC_20260303\ghidra_12.0.4_PUBLIC"
set "REPO=C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding"
set "PROJECT_DIR=%REPO%\logs\ghidra_proj"
set "PROJECT_NAME=D2R_RE"

set "D2R_DUMP_BIN=%REPO%\logs\d2r_exec.bin"
REM Targets:
REM   0x7ff79df7b310 — D2R input dispatcher (looks like wndproc body)
REM   0x7ff79df7a7f0 — central button-action handler  (down/up)
REM   0x7ff79df7a920 — keyboard handler ?
REM   0x7ff79df7a9e0 — keyboard handler ?
REM   0x7ff79df795d0 — generic event emitter (used pervasively)
REM   0x7ff79df793b0 — WM msg -> button bit classifier
set "D2R_TARGETS=0x7ff79df7b310,0x7ff79df7a7f0,0x7ff79df795d0,0x7ff79df793b0"
set "D2R_REPORT=%REPO%\logs\ghidra_wndproc_report.txt"

if not exist "%PROJECT_DIR%" mkdir "%PROJECT_DIR%"

set "GHIDRA_INSTALL_DIR=%GHIDRA%"
python -m pyghidra.ghidra_launch --install-dir "%GHIDRA%" ghidra.app.util.headless.AnalyzeHeadless ^
    "%PROJECT_DIR%" %PROJECT_NAME% ^
    -process "section_id_hot.bin" ^
    -noanalysis ^
    -postScript ghidra_re_wndproc.py ^
    -scriptPath "%REPO%\tools"

echo.
echo === DONE ===
echo Report: %D2R_REPORT%
