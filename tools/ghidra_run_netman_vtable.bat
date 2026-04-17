@echo off
REM Headless Ghidra runner for NetMan vtable decomp (Audit F1).

set "JAVA_HOME=C:\Users\Administrator\Downloads\OpenJDK25U-jdk_x64_windows_hotspot_25.0.2_10\jdk-25.0.2+10"
set "PATH=%JAVA_HOME%\bin;%PATH%"
set "GHIDRA=C:\Users\Administrator\Downloads\ghidra_12.0.4_PUBLIC_20260303\ghidra_12.0.4_PUBLIC"
set "REPO=C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding"
set "PROJECT_DIR=%REPO%\logs\ghidra_proj"
set "PROJECT_NAME=D2R_RE"

set "D2R_DUMP_BIN=%REPO%\logs\d2r_exec.bin"
set "D2R_TARGETS=0x00007ff79d8b6970,0x00007ff79d8b6a60,0x00007ff79d8b67f0,0x00007ff79d8b6830,0x00007ff79d8b6df0,0x00007ff79d8b6ca0,0x00007ff79d8b6b50,0x00007ff79d8b6f20,0x00007ff79d8b6ff0,0x00007ff79d8b7010,0x00007ff79d8b5d50,0x00007ff79d8e0920,0x00007ff79d8e08f0,0x00007ff79d8e0a40,0x00007ff79d8e0a10,0x00007ff79d8b89d0"
set "D2R_REPORT=%REPO%\logs\ghidra_netman_vtable_report.txt"

if not exist "%PROJECT_DIR%" mkdir "%PROJECT_DIR%"

set "GHIDRA_INSTALL_DIR=%GHIDRA%"
python -m pyghidra.ghidra_launch --install-dir "%GHIDRA%" ghidra.app.util.headless.AnalyzeHeadless ^
    "%PROJECT_DIR%" %PROJECT_NAME% ^
    -process "section_id_hot.bin" ^
    -noanalysis ^
    -postScript ghidra_decompile_walk.py ^
    -scriptPath "%REPO%\tools"

echo.
echo === DONE ===
echo Report: %D2R_REPORT%
