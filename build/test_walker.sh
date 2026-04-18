#!/bin/bash
# Single-phase walker test — combines CLAUDE_MODE + MODE2 + SNAPSHOT_ENABLE in
# the same app.exe instance so single_supervisor.initClaudePresenter runs
# SnapshotInit. Avoids the Phase 1 / Phase 2 re-injection race that
# auto_claude.sh runs into.
set -e
cd "$(dirname "$0")"
cmd //c "taskkill /F /IM app.exe" 2>/dev/null || true
cmd //c "taskkill /F /IM D2R.exe" 2>/dev/null || true
sleep 2
./tools/zombie_killer.exe 2>&1 | head -1

CLAUDE_MODE=1 MODE2=1 SKIP_ANTIDEBUG=1 STEALTH_READ=0 SNAPSHOT_ENABLE=1 CLAUDE_READ_TRACE=1 ./app.exe &
sleep 3
PID=$(tasklist 2>/dev/null | grep -i '^app.exe' | awk '{print $2}' | head -1)
PORT=$(netstat -ano 2>/dev/null | grep "$PID" | grep LISTENING | awk '{print $2}' | sed 's/.*://' | head -1)
echo "app pid=$PID port=$PORT"
curl -s -m 10 "http://127.0.0.1:$PORT/start?characterName=Blizzard&claudeMode=true" > /dev/null 2>&1 &

# Wait for area != 0 (up to 6 min)
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
echo "IN_GAME area=$AREA port=$PORT — walker will start scanning"
echo "Sleep 60s to observe stability; D2R zombie if walker trips Arxan"
sleep 60
D2R_ALIVE=$(tasklist 2>/dev/null | grep -c '^D2R.exe' || echo 0)
APP_ALIVE=$(tasklist 2>/dev/null | grep -c '^app.exe' || echo 0)
echo "END D2R_alive=$D2R_ALIVE app_alive=$APP_ALIVE"
