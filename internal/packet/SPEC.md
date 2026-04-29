# D2R Clientâ†’Server Packet Specification

Dokumentacja wszystkich poznanych opcodÃ³w wysyÅ‚anych przez klienta D2R przez `send_packet @ D2R+0x146600`.

Status kaÅ¼dego pakietu:
- âœ… **VERIFIED** â€” implementacja w `internal/packet/` przetestowana w grze
- ðŸ”¬ **SNIFFED** â€” zÅ‚apane z bufora poll'a, format wydedukowany, nie wysÅ‚ane
- âš ï¸ **CONFLICT** â€” istniejÄ…cy builder ma inny opcode niÅ¼ sniff, wymaga porÃ³wnania
- â“ **UNKNOWN** â€” opcode widziany w grze ale brak interpretacji
- ðŸ”’ **OFFLINE-BLOCKED** â€” nie zÅ‚apane bo wymaga online (chat, party, â€¦)

Å¹rÃ³dÅ‚o sniff: `cmd/bufpoll` polluje bufor pakietÃ³w D2R co 5ms na dwÃ³ch znanych adresach (`+0x19ED886`, `+0x1F51330`). Sekcyjne logi w `logs/sec_*.log`.

---

## Tabela master

| OP   | Nazwa                       | Status | Builder Go                       | Format                                                          |
|------|-----------------------------|--------|----------------------------------|-----------------------------------------------------------------|
| 0x01 | Walk to location            | âœ…     | `NewWalk(pos)`                   | `01 <X:u16> <Y:u16>`                                            |
| 0x03 | Run to location             | âœ…     | `NewRun(pos)`                    | `03 <X:u16> <Y:u16>`                                            |
| 0x04 | Move to entity              | ðŸ”¬     | â€”                                | `04 <type:u32> <gid:u32>` (+coords?)                            |
| 0x05 | Cast skill (left) at loc    | âœ…     | `NewCastLeftSkillLocation(pos)`  | `05 <X:u16> <Y:u16>`                                            |
| 0x06 | Cast skill (left) on entity | âœ…     | `CastSkillEntityLeft`            | `06 01000000 <gid:u32>`                                         |
| 0x0C | Cast skill (right) at loc   | âœ…     | `CastSkillLocation`              | `0C <X:u16> <Y:u16>`                                            |
| 0x0D | Cast skill (right) on entity| âœ…     | `CastSkillEntityRight`           | `0D 01000000 <gid:u32>`                                         |
| 0x13 | Entity interact             | âœ…     | `NewEntityInteract`              | `13 <type:u32> <gid:u32>`                                       |
| 0x16 | Item pickup                 | âœ…     | `PickupItem`                     | `16 <gid:u32>`                                                  |
| 0x17 | Item drop ground            | ðŸ”¬     | â€”                                | `17 <gid:u32> ...`                                              |
| 0x18 | Item move bufferâ†’inv        | ðŸ”¬     | â€”                                | `18 <gid:u32> <slot:u8> ...` (cube/stash/inv)                   |
| 0x19 | Item move invâ†’buffer        | ðŸ”¬     | â€”                                | `19 <gid:u32> <slot:u8> ...`                                    |
| 0x20 | **Cube transmute**          | ðŸ”¬     | â€”                                | `20 <p_gid:u32> 04 <ingredient_gids:u32 each> ...`              |
| 0x26 | Item right-click action     | ðŸ”¬âš ï¸  | â€”                                | `26 00 <gid:u32> <flags:u16> 84000000 00000702` (sub=0x84)      |
| 0x27 | Identify item (scroll)      | âš ï¸     | `NewIdentifyItem(item, scroll)`  | `27 <itemGID:u32> <scrollGID:u32>` â€” koliduje z sniff 0x26      |
| 0x2F | NPC chat init               | âœ…     | `NewNPCChatInit`                 | `2F <type:u32> <gid:u32>`                                       |
| 0x30 | NPC chat terminate          | âœ…     | `NewNPCChatTerminate`            | `30 <type:u32> <gid:u32>`                                       |
| 0x32 | **Generic interact dispatcher** | ðŸ”¬ | â€”                                | `32 <a_gid:u32> <b_gid:u32> <type:u32> <subaction:u8+>` âš        |
| 0x33 | **NPC sell item**           | ðŸ”¬     | â€”                                | `33 <p_gid:u32> <item_gid:u32> 0e <flags>`                      |
| 0x34 | NPC menu close ack          | ðŸ”¬âš ï¸  | `NewCainIdentifyAll` (0x34)      | sniff: `34 02` (2 bajty); existing builder ma inny format       |
| 0x35 | **Repair all**              | verified | `NewRepairAll`                   | `35 04 00 00 <npc_gid:u32> <live_cost:u32> FF FF FF FF`        |
| 0x38 | Short ack                   | ðŸ”¬     | â€”                                | `38 01000000 4e`                                                |
| 0x3A | Allocate stat point         | âœ…     | `AllocateStat`                   | `3A <statID:u16> 0000`                                          |
| 0x3B | Learn skill                 | âœ…     | `LearnSkill`                     | `3B <skillID:u16> 0000`                                         |
| 0x3C | Select skill (mouse btn)    | âœ…     | `SkillSelection`                 | `3C <skillID:u16> 00 <btn:u8> FFFFFFFF`                         |
| 0x40 | Entrance interaction        | âœ…     | `EntranceInteraction`            | `40 <gid:u32>`                                                  |
| 0x41 | TP interaction              | âœ…     | `TpInteraction`                  | `41 <gid:u32> FFFFFFFF`                                         |
| 0x46 | Cube item op                | ðŸ”¬     | â€”                                | `46 <gid:u32> 02 ... 0009 ...`                                  |
| 0x49 | Waypoint travel             | âœ…     | `NewWaypointTravel`              | `49 <wp_gid:u32> <dest:u8> 000000`                              |
| 0x4D | Entity interact result      | ðŸ”¬     | â€”                                | `4D <gid:u32> d6000000 0000 04 ...`                             |
| 0x52 | **Hire / revive merc**      | ðŸ”¬     | â€”                                | `52 <type:u32> <merc_gid:u32> <cost:u32> ...`                   |
| 0x54 | Cube item op2               | ðŸ”¬     | â€”                                | `54 <gid:u32> 0400 0000 ...`                                    |
| 0x5C | **Cain identify item**      | ðŸ”¬âš ï¸  | â€”                                | `5C <item_gid:u32> FFFFFFFF` (per-item, NOT identify-all)        |
| 0x60 | Weapon swap                 | âœ…     | `NewWeaponSwap`                  | `60` (single byte)                                              |

ðŸ”’ **Online-blocked, nie zÅ‚apane:**
- Chat (`Enter <text> Enter`) â€” wymaga online, bez serwera packet nie idzie
- Party invite/accept â€” wymaga drugiego gracza
- Trade invite/accept â€” wymaga drugiego gracza

---

## Konflikty do weryfikacji

### 0x26 vs 0x27 â€” Identify item przez scroll
- **Sniff (0x26):** `26 00 <gid> 04 0000 84000000 00000702 ffffffff...`
- **Existing (0x27):** `27 <item_gid:u32> <scroll_gid:u32>` (9 bajtÃ³w)

**Hipoteza:** sniff zÅ‚apaÅ‚ inny pakiet (np. item highlight / hover state). Bufor mÃ³gÅ‚ nadpisaÄ‡ szybki pakiet 0x27 zanim 5ms tick zÅ‚apaÅ‚. Test: wyÅ›lij oba i sprawdÅº ktÃ³ry zadziaÅ‚a.

### 0x34 vs 0x5C â€” Identify all (Cain)
- **Sniff (0x5C, per-item):** `5C <item_gid:u32> FFFFFFFF` Ã— N (jeden pakiet per item)
- **Existing (0x34, single):** `34 <cain_gid:u32>` (5 bajtÃ³w)
- **Sniff zÅ‚apaÅ‚ teÅ¼ `34 02`** (2 bajty) â€” moÅ¼e to ack/menu close, nie Å¼Ä…danie.

**Hipoteza A:** Cain identify-all wewnÄ™trznie wysyÅ‚a NÃ—0x5C, jeden per item. 0x34 to coÅ› innego.
**Hipoteza B:** IstniejÄ…ce 0x34 dziaÅ‚a na koolo, 0x5C to per-item path z innego flow (np. ID via Cain hover w UI).

Test: w grze uÅ¼yj `NewCainIdentifyAll` na Å¼ywo, sprawdÅº czy serwer odpowiada.

---

## Uniwersalny dispatcher 0x32

**NajwaÅ¼niejsze odkrycie sesji.** Opcode `0x32` to NIE jest pojedyncza akcja â€” to dispatcher entityâ†”entity, gdzie typ i sub-action determinujÄ… semantykÄ™.

Format ogÃ³lny:
```
32 <a_gid:u32> <b_gid:u32> <unit_type:u32> <flags...>
```

Zaobserwowane formy:

| Akcja            | unit_type | flags          | przykÅ‚ad                                                          |
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

âš ï¸ WartoÅ›ci 0x4E i 0x0E mogÄ… byÄ‡ **nie unit_type a item_type / merchant_subtype**. Wymaga RE.

Dla par (`32`/`33`):
- `0x32` = "interact / buy / use"
- `0x33` = "sell" (same payload structure, different semantic)

---

## Identyfikacja pakietÃ³w per sekcja

| Sekcja              | Log                          | ZÅ‚apane (NEW vs walk baseline)                       |
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
| 11. Chat            | sec_chat.log                 | ðŸ”’ offline â€” empty                                   |
| 12. Stash drag      | sec_stash.log                | 0x18, 0x19 (same as inv)                             |
| 13. **Cube + transmute** | sec_cube.log            | **0x20**, 0x18, 0x19, 0x26, 0x46, 0x54               |
| 14. Potion belt     | sec_potion.log               | 0x32 (potion subform), 0x04, 0x26, 0x2F, 0x38, 0x4D  |
| 15a. Identify scroll| sec_id_tome.log              | 0x26 (sub=0x84) â€” âš  konflikt z istniejÄ…cym 0x27      |
| 15b. **Cain identify**| sec_id_cain.log            | **0x5C**, 0x34, 0x16, 0x4D                           |
| 16. NPC buy         | sec_npc_buy.log              | 0x32 (buy subform)                                   |
| 17. **NPC sell**    | sec_npc_sell.log             | **0x33**                                             |
| 18. NPC repair      | sec_npc_repair.log           | 0x35 âœ“ (potwierdza istniejÄ…cy builder)               |
| 19. Gamble          | sec_gamble.log               | 0x32 (gamble subform)                                |
| 20. Stat/skill alloc| â€”                            | nie testowane (bez wolnych pkt) â€” istniejÄ…ce dziaÅ‚a  |
| 21. Party           | â€”                            | ðŸ”’ offline                                           |
| 22. **Resurrect merc**| sec_merc.log                | **0x52**, 0x2F, 0x30, 0x4D                           |
| 23. Run/walk toggle | sec_run_toggle.log           | brak â€” toggle jest client-side (flag w 0x01/0x03)    |

---

## NastÄ™pne kroki

1. **RozszerzyÄ‡ existing builders** o nowe opcody: 0x20 (transmute), 0x32 (dispatcher), 0x33 (sell), 0x52 (merc), 0x5C (Cain ID per-item)
2. **ZweryfikowaÄ‡ konflikty** 0x26/0x27 i 0x34/0x5C przez wysÅ‚anie pakietÃ³w na Å¼ywo
3. **Online sniff** dla chat/party (sekcja 11, 21) â€” wymaga poÅ‚Ä…czenia
4. **Runtime policy (historical note)** â€” dawniej zakÅ‚adano obowiÄ…zkowy HID safety net dla kaÅ¼dego packet sendera; obecny kierunek projektu to packet-first runtime, gdzie fallback nie moÅ¼e maskowaÄ‡ packet failures w Å›cieÅ¼kach juÅ¼ zmigrowanych
5. **GUI toggle per akcja** â€” checkbox "use packet / use HID" dla kaÅ¼dej operacji w panelu botÃ³w
