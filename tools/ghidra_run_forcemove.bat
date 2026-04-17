@echo off
REM Headless Ghidra runner for ForceMove RE.

set "JAVA_HOME=C:\Users\Administrator\Downloads\OpenJDK25U-jdk_x64_windows_hotspot_25.0.2_10\jdk-25.0.2+10"
set "PATH=%JAVA_HOME%\bin;%PATH%"
set "GHIDRA=C:\Users\Administrator\Downloads\ghidra_12.0.4_PUBLIC_20260303\ghidra_12.0.4_PUBLIC"
set "REPO=C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding"
set "PROJECT_DIR=%REPO%\logs\ghidra_proj"
set "PROJECT_NAME=D2R_RE"

set "D2R_DUMP_BIN=%REPO%\logs\d2r_exec.bin"
set "D2R_REPORT=%REPO%\logs\ghidra_forcemove_report.txt"

set "GHIDRA_INSTALL_DIR=%GHIDRA%"
python -m pyghidra.ghidra_launch --install-dir "%GHIDRA%" ghidra.app.util.headless.AnalyzeHeadless ^
    "%PROJECT_DIR%" %PROJECT_NAME% ^
    -process "section_id_hot.bin" ^
    -noanalysis ^
    -postScript ghidra_re_forcemove.py ^
    -scriptPath "%REPO%\tools"

echo.
echo === DONE ===
echo Report: %D2R_REPORT%
