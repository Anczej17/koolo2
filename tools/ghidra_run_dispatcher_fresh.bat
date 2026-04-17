@echo off
REM Fresh Ghidra project run for the wndproc dispatcher chain.
REM Avoids conflicts with the existing D2R_RE project which has stale memory
REM blocks from prior B/E loads using smaller dumps.

set "JAVA_HOME=C:\Users\Administrator\Downloads\OpenJDK25U-jdk_x64_windows_hotspot_25.0.2_10\jdk-25.0.2+10"
set "PATH=%JAVA_HOME%\bin;%PATH%"
set "GHIDRA=C:\Users\Administrator\Downloads\ghidra_12.0.4_PUBLIC_20260303\ghidra_12.0.4_PUBLIC"
set "REPO=C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding"
set "PROJECT_DIR=%REPO%\logs\ghidra_proj_fresh"
set "PROJECT_NAME=D2R_FRESH"

set "D2R_DUMP_BIN=%REPO%\logs\d2r_exec_merged.bin"
set "D2R_TARGETS=0x7ff79df7a7f0,0x7ff79df7a920,0x7ff79df7a9e0,0x7ff79df7b310,0x7ff79df793b0"
set "D2R_REPORT=%REPO%\logs\ghidra_dispatcher_fresh.txt"

if exist "%PROJECT_DIR%" rmdir /S /Q "%PROJECT_DIR%"
mkdir "%PROJECT_DIR%"

set "GHIDRA_INSTALL_DIR=%GHIDRA%"

REM Step 1: import a dummy 1-byte file as the program (we never analyze it).
REM Step 2: post-script loads the merged dump into memory blocks and decompiles.
echo dummy > "%PROJECT_DIR%\dummy.bin"

python -m pyghidra.ghidra_launch --install-dir "%GHIDRA%" ghidra.app.util.headless.AnalyzeHeadless ^
    "%PROJECT_DIR%" %PROJECT_NAME% ^
    -import "%PROJECT_DIR%\dummy.bin" ^
    -loader BinaryLoader ^
    -loader-language x86:LE:64:default ^
    -noanalysis ^
    -postScript ghidra_decompile_walk.py ^
    -scriptPath "%REPO%\tools"

echo.
echo === DONE ===
echo Report: %D2R_REPORT%
