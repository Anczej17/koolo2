#!/bin/bash
# Full-packet stash test for Rogue Encampment.
#   1. Walk to bank (packet movement; useForMovement=true)
#   2. Open stash (0x41 packet, Action=0, bank GID=0x11)
#   3. Move Jewel inv(5,2) → stash(6,9)  via 0x54 packet
#   4. Verify Jewel in stash, D2R alive, no fatal exception
#   5. Move Jewel stash(6,9) → inv(5,2)  via 0x54 packet
#   6. Final state report
#
# Usage: ./test_stash_packet.sh [PORT]
#        PORT defaults to value in /tmp/bot_port.txt
set -u
PORT="${1:-$(cat /tmp/bot_port.txt 2>/dev/null || echo 0)}"
CHAR=Blizzard
[ "$PORT" = "0" ] && { echo "ERROR: no PORT given and /tmp/bot_port.txt missing"; exit 1; }
URL="http://127.0.0.1:$PORT"

q_state() {
    curl -s --max-time 3 "$URL/debug/gamestate?character=$CHAR" | python -c "
import json,sys
try:
    d=json.loads(sys.stdin.read())
    print(f\"pos=({d['player_pos']['x']},{d['player_pos']['y']}) area={d.get('area_name','?')} in_town={d.get('in_town')}\")
except Exception as e: print('state-err:',e)
" 2>&1
}

q_jewel() {
    curl -s --max-time 5 "$URL/debug/inventory?character=$CHAR" | python -c "
import json,sys
try:
    d=json.loads(sys.stdin.read())
    js=[i for i in d['items'] if i['name']=='Jewel']
    if not js: print('  Jewel: NOT FOUND in any location')
    for i in js: print(f'  Jewel: loc={i[\"location\"]} pos=({i[\"x\"]},{i[\"y\"]}) gid=0x{i[\"gid\"]:X}')
except Exception as e: print('  inv-err:',e)
" 2>&1
}

q_crash() {
    curl -s --max-time 3 "$URL/debug/crash-info?character=$CHAR" | python -c "
import json,sys
try:
    d=json.loads(sys.stdin.read())
    av=d.get('av',0); so=d.get('so',0); fx=d.get('fixups',0); v=d.get('valid',False); cd=d.get('code','-')
    print(f'  crash: av={av} so={so} fixups={fx} valid={v} code={cd}')
except Exception as e: print('  crash-err:',e)
" 2>&1
}

q_d2r() {
    if tasklist //FI 'IMAGENAME eq D2R.exe' //FO CSV 2>&1 | grep -q '\"D2R.exe\"'; then
        echo "D2R: ALIVE"
    else
        echo "D2R: DEAD"
    fi
}

echo "=== Bot port=$PORT char=$CHAR ==="
echo ""
echo "--- Step 0: initial state ---"
q_state
q_jewel
q_crash
q_d2r
echo ""

echo "--- Step 1: walk to bank (4466, 4629) via packet ---"
RESP=$(curl -s --max-time 30 "$URL/debug/movetocoords?character=$CHAR&x=4466&y=4629" 2>&1)
echo "  movetocoords: $RESP"
sleep 1
q_state
echo ""

echo "--- Step 2: open stash (0x41 Action=0 bank GID=0x11) ---"
RESP=$(curl -s --max-time 5 "$URL/debug/sendpacket?character=$CHAR&hex=411100000000000000FFFFFFFF&path=game" 2>&1)
echo "  0x41: $RESP"
sleep 1
echo ""

echo "--- Step 3: move Jewel inv(5,2) -> stash(6,9) via 0x54 ---"
# 54 | 56000000 | 00000000 | 05 00 02 00 | 04000000 | 06 00 09  (20B)
RESP=$(curl -s --max-time 10 "$URL/debug/sendpacket?character=$CHAR&hex=5456000000000000000500020004000000060009&path=game" 2>&1)
echo "  0x54 inv->stash: $RESP"
sleep 2
echo ""

echo "--- Step 4: verify Jewel moved to stash ---"
q_d2r
q_jewel
q_crash
echo ""

echo "--- Step 5: move Jewel stash(6,9) -> inv(5,2) via 0x54 ---"
# 54 | 56000000 | 04000000 | 06 00 09 00 | 00000000 | 05 00 02  (20B)
RESP=$(curl -s --max-time 10 "$URL/debug/sendpacket?character=$CHAR&hex=5456000000040000000600090000000000050002&path=game" 2>&1)
echo "  0x54 stash->inv: $RESP"
sleep 2
echo ""

echo "--- Step 6: final state ---"
q_d2r
q_state
q_jewel
q_crash
echo ""
echo "=== TEST DONE ==="
