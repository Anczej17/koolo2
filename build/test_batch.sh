#!/bin/bash
# Validate CMD_ROP_READ_BATCH without activating the walker — launches
# app.exe in Phase 1 Claude mode without SNAPSHOT_ENABLE so D2R stays
# stable (walker inert because not init'd). Then we probe
# /debug/rop-read-batch over HTTP.
set -e
cd "$(dirname "$0")"
cmd //c "taskkill /F /IM app.exe" 2>/dev/null || true
cmd //c "taskkill /F /IM D2R.exe" 2>/dev/null || true
sleep 2
./tools/zombie_killer.exe 2>&1 | head -1

CLAUDE_MODE=1 SKIP_ANTIDEBUG=1 STEALTH_READ=0 ./app.exe &
sleep 3
PID=$(tasklist 2>/dev/null | grep -i '^app.exe' | awk '{print $2}' | head -1)
PORT=$(netstat -ano 2>/dev/null | grep "$PID" | grep LISTENING | awk '{print $2}' | sed 's/.*://' | head -1)
echo "app pid=$PID port=$PORT"
curl -s -m 10 "http://127.0.0.1:$PORT/start?characterName=Blizzard&claudeMode=true" > /dev/null 2>&1 &

# Wait for area != 0
AREA=""
for i in $(seq 1 360); do
    AREA=$(curl -s -m 2 "http://127.0.0.1:$PORT/debug/gamestate?character=Blizzard" 2>&1 | grep -o '"area":[0-9]*' | cut -d: -f2)
    [ -n "$AREA" ] && [ "$AREA" != "0" ] && break
    sleep 1
done
if [ -z "$AREA" ] || [ "$AREA" = "0" ]; then
    echo "FAIL no area"
    exit 1
fi
echo "IN_GAME area=$AREA port=$PORT — running batch tests"
sleep 3

echo "=== batch 1 entry ==="
curl -s -m 5 "http://127.0.0.1:$PORT/debug/rop-read-batch?character=Blizzard&entries=0x7ff691f56600:8" ; echo

echo "=== batch 4 entries ==="
curl -s -m 5 "http://127.0.0.1:$PORT/debug/rop-read-batch?character=Blizzard&entries=0x7ff691f56600:8,0x7ff691f56608:8,0x7ff691f56610:8,0x7ff691f56618:8" ; echo

echo "=== batch 16 entries ==="
E=""
for i in $(seq 0 15); do
  OFF=$((0x146600 + i * 8))
  VA=$(printf "0x%x" $((0x7ff691e10000 + OFF)))
  if [ -z "$E" ]; then E="${VA}:8"; else E="${E},${VA}:8"; fi
done
curl -s -m 5 "http://127.0.0.1:$PORT/debug/rop-read-batch?character=Blizzard&entries=$E" ; echo

echo "=== procs ==="
tasklist 2>/dev/null | grep -iE "D2R|app.exe"
