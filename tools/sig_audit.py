#!/usr/bin/env python3
"""Quick binary signature audit. Reads a Go binary and checks for known
bot/D2R related substrings + lists github.com/ prefixes."""
import sys

PATH = sys.argv[1] if len(sys.argv) > 1 else 'build/dev_test_final.exe'

with open(PATH, 'rb') as f:
    data = f.read()

terms = [
    b'Diablo II', b'D2RPath', b'D2LoDPath', b'Diablo II Resurrected',
    b'Lord of Destruction', b'koolo', b'Koolo', b'Audyt', b'kwader',
    b'hectorgimenez', b'd2go', b'Saved Games', b'amsi', b'etwp',
    b'NtRead', b'helper', b'rebrand', b'audit',
]
print(f'=== Binary signature audit: {PATH} ({len(data):,} bytes) ===')
for t in terms:
    cnt = data.count(t)
    flag = '!!' if cnt > 0 else 'OK'
    print(f'  [{flag}] {t.decode():32s}: {cnt}')

print()
print('=== "helper" contexts (top 15) ===')
i = 0
seen = 0
while seen < 15:
    p = data.find(b'helper', i)
    if p < 0: break
    ctx = data[max(0, p-30):p+40]
    s = ''.join(chr(b) if 32 <= b < 127 else '.' for b in ctx)
    print(f'  @0x{p:x}: {s}')
    i = p + 1
    seen += 1

print()
print('=== unique github.com/owner prefixes ===')
stop_chars = bytes([0x2F, 0x00, 0x20, 0x22, 0x3A, 0x5C])  # / NUL space " : \
ghs = set()
i = 0
while True:
    p = data.find(b'github.com/', i)
    if p < 0: break
    end = p + 11
    while end < len(data) and data[end] not in stop_chars:
        end += 1
    ghs.add(data[p:end].decode('latin-1'))
    i = p + 1
print(f'total unique: {len(ghs)}')
for g in sorted(ghs):
    print(f'  {g}')
