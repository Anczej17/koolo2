#!/bin/bash
# Live validation: bot runs with ROP_READ=batch so hot path
# (GetRawPlayerUnits UnitTable + per-player field reads) routes through
# CMD_ROP_READ_BATCH instead of RPM. Stay in game for 60 s, check RPM
# counter delta to confirm the wiring.
set -e
cd "$(dirname "$0")"
cmd //c "taskkill /F /IM app.exe" 2>/dev/null || true
cmd //c "taskkill /F /IM D2R.exe" 2>/dev/null || true
sleep 2
./tools/zombie_killer.exe 2>&1 | head -1

CLAUDE_MODE=1 SKIP_ANTIDEBUG=1 STEALTH_READ=0 ROP_READ=batch CLAUDE_READ_TRACE=1 ./app.exe &
sleep 3
PID=$(tasklist 2>/dev/null | grep -i '^app.exe' | awk '{print $2}' | head -1)
PORT=$(netstat -ano 2>/dev/null | grep "$PID" | grep LISTENING | awk '{print $2}' | sed 's/.*://' | head -1)
echo "app pid=$PID port=$PORT"
curl -s -m 10 "http://127.0.0.1:$PORT/start?characterName=Blizzard&claudeMode=true" > /dev/null 2>&1 &

AREA=""
for i in $(seq 1 360); do
    AREA=$(curl -s -m 2 "http://127.0.0.1:$PORT/debug/gamestate?character=Blizzard" 2>&1 | grep -o '"area":[0-9]*' | cut -d: -f2)
    [ -n "$AREA" ] && [ "$AREA" != "0" ] && break
    sleep 1
done
if [ -z "$AREA" ] || [ "$AREA" = "0" ]; then echo "FAIL no area"; exit 1; fi
echo "IN_GAME area=$AREA port=$PORT — waiting for initClaudePresenter"

# Wait for presenter-initialised + READY log (otherwise batch hook isn't
# installed yet and every GetData runs on plain RPM).
READY=0
for i in $(seq 1 60); do
    if curl -s -m 2 "http://127.0.0.1:$PORT/debug/rop-dbg?character=Blizzard" 2>&1 | grep -q '"ok":true'; then
        READY=1; break
    fi
    sleep 1
done
if [ $READY = 0 ]; then echo "FAIL presenter not ready"; exit 1; fi
echo "PRESENTER READY — ROP_READ=batch active"

# Sample trace before
echo "=== read trace BEFORE ==="
curl -s -m 3 "http://127.0.0.1:$PORT/debug/read-trace-stats?character=Blizzard" 2>&1
echo

# Force a few GetData ticks by calling /debug/gamestate repeatedly — each
# call triggers RefreshGameData → GetData → GetRawPlayerUnits.
for k in $(seq 1 10); do
  curl -s -m 3 "http://127.0.0.1:$PORT/debug/gamestate?character=Blizzard" > /dev/null 2>&1
done

echo "=== read trace AFTER 10 ticks ==="
curl -s -m 3 "http://127.0.0.1:$PORT/debug/read-trace-stats?character=Blizzard" 2>&1
echo

echo "=== Sleep 30s observe stability ==="
sleep 30

echo "=== procs end ==="
tasklist 2>/dev/null | grep -iE "D2R|app.exe"
echo "=== END ==="
