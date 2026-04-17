@echo off
REM Headless Ghidra runner for D2R identify decompile.
REM Imports the dump as raw binary, then runs the postScript which builds
REM the proper memory map and decompiles the identify builders.

set "JAVA_HOME=C:\Users\Administrator\Downloads\OpenJDK25U-jdk_x64_windows_hotspot_25.0.2_10\jdk-25.0.2+10"
set "PATH=%JAVA_HOME%\bin;%PATH%"
set "GHIDRA=C:\Users\Administrator\Downloads\ghidra_12.0.4_PUBLIC_20260303\ghidra_12.0.4_PUBLIC"
set "REPO=C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding"
set "PROJECT_DIR=%REPO%\logs\ghidra_proj"
set "PROJECT_NAME=D2R_RE"

set "D2R_DUMP_BIN=%REPO%\logs\section_id_hot.bin"
set "D2R_TARGETS=0x7ff79d8b81b0,0x7ff79d8b6b50"
set "D2R_REPORT=%REPO%\logs\ghidra_identify_report.txt"

if not exist "%PROJECT_DIR%" mkdir "%PROJECT_DIR%"

REM Import the real dump as raw binary at a safe base (away from D2R VA
REM range). The post-script will then create properly-mapped memory blocks
REM for each chunk at its true VA, leaving the flat-loaded block in place
REM (it doesn't overlap because base 0x100000 is far below D2R range).

REM Process the already-imported program. PyGhidra is now installed —
REM launch headless via python -m pyghidra.ghidra_launch so .py scripts
REM run as Python 3 (which my script needs).
set "GHIDRA_INSTALL_DIR=%GHIDRA%"
python -m pyghidra.ghidra_launch --install-dir "%GHIDRA%" ghidra.app.util.headless.AnalyzeHeadless ^
    "%PROJECT_DIR%" %PROJECT_NAME% ^
    -process "section_id_hot.bin" ^
    -noanalysis ^
    -postScript ghidra_decompile_identify.py ^
    -scriptPath "%REPO%\tools"

echo.
echo === DONE ===
echo Report should be at: %D2R_REPORT%
