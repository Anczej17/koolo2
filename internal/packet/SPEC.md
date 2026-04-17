# D2R Client→Server Packet Specification

Dokumentacja wszystkich poznanych opcodów wysyłanych przez klienta D2R przez `send_packet @ D2R+0x146600`.

Status każdego pakietu:
- ✅ **VERIFIED** — implementacja w `internal/packet/` przetestowana w grze
- 🔬 **SNIFFED** — złapane z bufora poll'a, format wydedukowany, nie wysłane
- ⚠️ **CONFLICT** — istniejący builder ma inny opcode niż sniff, wymaga porównania
- ❓ **UNKNOWN** — opcode widziany w grze ale brak interpretacji
- 🔒 **OFFLINE-BLOCKED** — nie złapane bo wymaga online (chat, party, …)

Źródło sniff: `cmd/bufpoll` polluje bufor pakietów D2R co 5ms na dwóch znanych adresach (`+0x19ED886`, `+0x1F51330`). Sekcyjne logi w `logs/sec_*.log`.

---

## Tabela master

| OP   | Nazwa                       | Status | Builder Go                       | Format                                                          |
|------|-----------------------------|--------|----------------------------------|-----------------------------------------------------------------|
| 0x01 | Walk to location            | ✅     | `NewWalk(pos)`                   | `01 <X:u16> <Y:u16>`                                            |
| 0x03 | Run to location             | ✅     | `NewRun(pos)`                    | `03 <X:u16> <Y:u16>`                                            |
| 0x04 | Move to entity              | 🔬     | —                                | `04 <type:u32> <gid:u32>` (+coords?)                            |
| 0x05 | Cast skill (left) at loc    | ✅     | `NewCastLeftSkillLocation(pos)`  | `05 <X:u16> <Y:u16>`                                            |
| 0x06 | Cast skill (left) on entity | ✅     | `CastSkillEntityLeft`            | `06 01000000 <gid:u32>`                                         |
| 0x0C | Cast skill (right) at loc   | ✅     | `CastSkillLocation`              | `0C <X:u16> <Y:u16>`                                            |
| 0x0D | Cast skill (right) on entity| ✅     | `CastSkillEntityRight`           | `0D 01000000 <gid:u32>`                                         |
| 0x13 | Entity interact             | ✅     | `NewEntityInteract`              | `13 <type:u32> <gid:u32>`                                       |
| 0x16 | Item pickup                 | ✅     | `PickupItem`                     | `16 <gid:u32>`                                                  |
| 0x17 | Item drop ground            | 🔬     | —                                | `17 <gid:u32> ...`                                              |
| 0x18 | Item move buffer→inv        | 🔬     | —                                | `18 <gid:u32> <slot:u8> ...` (cube/stash/inv)                   |
| 0x19 | Item move inv→buffer        | 🔬     | —                                | `19 <gid:u32> <slot:u8> ...`                                    |
| 0x20 | **Cube transmute**          | 🔬     | —                                | `20 <p_gid:u32> 04 <ingredient_gids:u32 each> ...`              |
| 0x26 | Item right-click action     | 🔬⚠️  | —                                | `26 00 <gid:u32> <flags:u16> 84000000 00000702` (sub=0x84)      |
| 0x27 | Identify item (scroll)      | ⚠️     | `NewIdentifyItem(item, scroll)`  | `27 <itemGID:u32> <scrollGID:u32>` — koliduje z sniff 0x26      |
| 0x2F | NPC chat init               | ✅     | `NewNPCChatInit`                 | `2F <type:u32> <gid:u32>`                                       |
| 0x30 | NPC chat terminate          | ✅     | `NewNPCChatTerminate`            | `30 <type:u32> <gid:u32>`                                       |
| 0x32 | **Generic interact dispatcher** | 🔬 | —                                | `32 <a_gid:u32> <b_gid:u32> <type:u32> <subaction:u8+>` ⚠       |
| 0x33 | **NPC sell item**           | 🔬     | —                                | `33 <p_gid:u32> <item_gid:u32> 0e <flags>`                      |
| 0x34 | NPC menu close ack          | 🔬⚠️  | `NewCainIdentifyAll` (0x34)      | sniff: `34 02` (2 bajty); existing builder ma inny format       |
| 0x35 | **Repair all**              | ✅     | `NewRepairAll`                   | `35 <npc_gid:u32> <player_gid:u32> <flags:u8*6>`                |
| 0x38 | Short ack                   | 🔬     | —                                | `38 01000000 4e`                                                |
| 0x3A | Allocate stat point         | ✅     | `AllocateStat`                   | `3A <statID:u16> 0000`                                          |
| 0x3B | Learn skill                 | ✅     | `LearnSkill`                     | `3B <skillID:u16> 0000`                                         |
| 0x3C | Select skill (mouse btn)    | ✅     | `SkillSelection`                 | `3C <skillID:u16> 00 <btn:u8> FFFFFFFF`                         |
| 0x40 | Entrance interaction        | ✅     | `EntranceInteraction`            | `40 <gid:u32>`                                                  |
| 0x41 | TP interaction              | ✅     | `TpInteraction`                  | `41 <gid:u32> FFFFFFFF`                                         |
| 0x46 | Cube item op                | 🔬     | —                                | `46 <gid:u32> 02 ... 0009 ...`                                  |
| 0x49 | Waypoint travel             | ✅     | `NewWaypointTravel`              | `49 <wp_gid:u32> <dest:u8> 000000`                              |
| 0x4D | Entity interact result      | 🔬     | —                                | `4D <gid:u32> d6000000 0000 04 ...`                             |
| 0x52 | **Hire / revive merc**      | 🔬     | —                                | `52 <type:u32> <merc_gid:u32> <cost:u32> ...`                   |
| 0x54 | Cube item op2               | 🔬     | —                                | `54 <gid:u32> 0400 0000 ...`                                    |
| 0x5C | **Cain identify item**      | 🔬⚠️  | —                                | `5C <item_gid:u32> FFFFFFFF` (per-item, NOT identify-all)        |
| 0x60 | Weapon swap                 | ✅     | `NewWeaponSwap`                  | `60` (single byte)                                              |

🔒 **Online-blocked, nie złapane:**
- Chat (`Enter <text> Enter`) — wymaga online, bez serwera packet nie idzie
- Party invite/accept — wymaga drugiego gracza
- Trade invite/accept — wymaga drugiego gracza

---

## Konflikty do weryfikacji

### 0x26 vs 0x27 — Identify item przez scroll
- **Sniff (0x26):** `26 00 <gid> 04 0000 84000000 00000702 ffffffff...`
- **Existing (0x27):** `27 <item_gid:u32> <scroll_gid:u32>` (9 bajtów)

**Hipoteza:** sniff złapał inny pakiet (np. item highlight / hover state). Bufor mógł nadpisać szybki pakiet 0x27 zanim 5ms tick złapał. Test: wyślij oba i sprawdź który zadziała.

### 0x34 vs 0x5C — Identify all (Cain)
- **Sniff (0x5C, per-item):** `5C <item_gid:u32> FFFFFFFF` × N (jeden pakiet per item)
- **Existing (0x34, single):** `34 <cain_gid:u32>` (5 bajtów)
- **Sniff złapał też `34 02`** (2 bajty) — może to ack/menu close, nie żądanie.

**Hipoteza A:** Cain identify-all wewnętrznie wysyła N×0x5C, jeden per item. 0x34 to coś innego.
**Hipoteza B:** Istniejące 0x34 działa na koolo, 0x5C to per-item path z innego flow (np. ID via Cain hover w UI).

Test: w grze użyj `NewCainIdentifyAll` na żywo, sprawdź czy serwer odpowiada.

---

## Uniwersalny dispatcher 0x32

**Najważniejsze odkrycie sesji.** Opcode `0x32` to NIE jest pojedyncza akcja — to dispatcher entity↔entity, gdzie typ i sub-action determinują semantykę.

Format ogólny:
```
32 <a_gid:u32> <b_gid:u32> <unit_type:u32> <flags...>
```

Zaobserwowane formy:

| Akcja            | unit_type | flags          | przykład                                                          |
|------------------|-----------|----------------|-------------------------------------------------------------------|
| Use potion (belt)| 0x4E (78) | `0800 0300 0100 0000 0302` | `32 e8030000 83010000 4e000000 0800 0300 0100 0000 0302` |
| NPC buy item     | 0x0E (14) | `0900 0600 0400 ...`       | `32 c2010000 d2010000 0e000000 0900 0600 0400 0000 03`   |
| Gamble buy       | 0x06 (6)  | `0900 0100 0500 ...`       | `32 18f60000 7d020000 06000000 0900 0100 0500 0300 0001` |

Pole `unit_type` prawdopodobnie odpowiada [enum z gry](https://github.com/PhrozenKeep/D2RModding/wiki/D2R-Unit-Types):
- 0 = Player
- 1 = Monster (NPC hostile)
- 2 = Object  
- 3 = Missile
- 4 = Item (`0x4E`?)
- 5 = Tile
- 6 = Gambled item slot? (custom)
- 14 = Trading NPC (Charsi/Akara/...)

⚠️ Wartości 0x4E i 0x0E mogą być **nie unit_type a item_type / merchant_subtype**. Wymaga RE.

Dla par (`32`/`33`):
- `0x32` = "interact / buy / use"
- `0x33` = "sell" (same payload structure, different semantic)

---

## Identyfikacja pakietów per sekcja

| Sekcja              | Log                          | Złapane (NEW vs walk baseline)                       |
|---------------------|------------------------------|------------------------------------------------------|
| 1. Idle (baseline)  | section_walk_packets.log     | 0x01, 0x03                                           |
| 2a. Walk            | section_walk_packets.log     | 0x01, 0x03                                           |
| 2b. LMB skill loc   | sec_lmb_skill.log            | 0x05, 0x0C                                           |
| 3. RMB skill loc    | sec_rmb_skill.log            | 0x0D                                                 |
| 4. LMB on entity    | sec_lmb_entity.log           | 0x06                                                 |
| 5. RMB on entity    | sec_rmb_entity.log           | 0x0D                                                 |
| 6. Weapon swap      | sec_swap.log                 | 0x60 (crashed live, see project memory)              |
| 7. NPC interact     | sec_npc.log                  | 0x13, 0x2F, 0x30                                     |
| 8. Inventory drag   | sec_inv.log                  | 0x18, 0x19                                           |
| 9. Waypoint         | sec_wp.log                   | 0x49                                                 |
| 10. TP / portal     | sec_tp.log                   | 0x41                                                 |
| 11. Chat            | sec_chat.log                 | 🔒 offline — empty                                   |
| 12. Stash drag      | sec_stash.log                | 0x18, 0x19 (same as inv)                             |
| 13. **Cube + transmute** | sec_cube.log            | **0x20**, 0x18, 0x19, 0x26, 0x46, 0x54               |
| 14. Potion belt     | sec_potion.log               | 0x32 (potion subform), 0x04, 0x26, 0x2F, 0x38, 0x4D  |
| 15a. Identify scroll| sec_id_tome.log              | 0x26 (sub=0x84) — ⚠ konflikt z istniejącym 0x27      |
| 15b. **Cain identify**| sec_id_cain.log            | **0x5C**, 0x34, 0x16, 0x4D                           |
| 16. NPC buy         | sec_npc_buy.log              | 0x32 (buy subform)                                   |
| 17. **NPC sell**    | sec_npc_sell.log             | **0x33**                                             |
| 18. NPC repair      | sec_npc_repair.log           | 0x35 ✓ (potwierdza istniejący builder)               |
| 19. Gamble          | sec_gamble.log               | 0x32 (gamble subform)                                |
| 20. Stat/skill alloc| —                            | nie testowane (bez wolnych pkt) — istniejące działa  |
| 21. Party           | —                            | 🔒 offline                                           |
| 22. **Resurrect merc**| sec_merc.log                | **0x52**, 0x2F, 0x30, 0x4D                           |
| 23. Run/walk toggle | sec_run_toggle.log           | brak — toggle jest client-side (flag w 0x01/0x03)    |

---

## Następne kroki

1. **Rozszerzyć existing builders** o nowe opcody: 0x20 (transmute), 0x32 (dispatcher), 0x33 (sell), 0x52 (merc), 0x5C (Cain ID per-item)
2. **Zweryfikować konflikty** 0x26/0x27 i 0x34/0x5C przez wysłanie pakietów na żywo
3. **Online sniff** dla chat/party (sekcja 11, 21) — wymaga połączenia
4. **HID fallback** — każdy packet sender musi mieć wariant przez klawiaturę/myszkę jako safety net
5. **GUI toggle per akcja** — checkbox "use packet / use HID" dla każdej operacji w panelu botów
