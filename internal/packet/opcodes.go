package packet

// D2R client→server packet opcodes.
//
// Wszystkie opcody znane na 2026-04-07. Patrz internal/packet/SPEC.md
// po pełną dokumentację formatów, statusów weryfikacji i konfliktów.
//
// Statusy:
//   verified — implementacja w internal/packet/ przetestowana w grze
//   sniffed  — złapane przez bufpoll, builder do dorobienia
//   conflict — istniejący builder ma inny opcode niż sniff (TODO weryfikacja)
//   blocked  — wymaga online (chat/party/trade) lub niezweryfikowane

const (
	// === Movement ===
	// 0x01 (walk) and 0x03 (run) are NOT sendable via packet.
	// Proven impossible by AUDIT_C_playerunit_diff + AUDIT_D_input_poll +
	// MOVEMENT_INPROCESS_SPEC: send_fn only carries opcodes >= 0x67, and
	// sending 0x01/0x03 through it results in the client ignoring the
	// packet entirely (no PlayerUnit state change). Use real_click_worker
	// @ RVA 0xBA95D0 via CmdCallFn instead.

	// === Skills ===
	OpCastSkillLeftLoc      = 0x05 // verified — [05][X:u16][Y:u16]
	OpCastSkillLeftEntity   = 0x06 // verified — [06][01000000][gid:u32]
	OpCastSkillRightLoc     = 0x0C // verified — [0C][X:u16][Y:u16]
	OpCastSkillRightEntity  = 0x0D // verified — [0D][01000000][gid:u32]

	// === Entity / world interaction ===
	OpEntityInteract     = 0x13 // verified — [13][type:u32][gid:u32]
	OpMoveToEntity       = 0x04 // sniffed  — [04][type:u32][gid:u32](+coords?)
	OpEntranceInteract     = 0x40 // verified — [40][gid:u32]
	OpTpInteract           = 0x41 // verified — [41][gid:u32][FFFFFFFF]
	OpTpConfirmTravel      = 0x43 // sniffed 2026-04-15 (paired post-0x41) — [43][01000000][01000000] (9B fixed)
	OpWaypointTravel       = 0x49 // verified — [49][wp_gid:u32][dest:u8][000000]
	OpTpDestinationSelect  = 0x4B // sniffed 2026-04-15 — [4B][dest:u8][000000] (5B); paired with 0x43 to confirm

	// === Inventory / items ===
	OpPickupItem    = 0x16 // verified — [16][gid:u32]
	OpItemDrop      = 0x17 // sniffed  — drop on ground
	OpItemMoveTo    = 0x18 // sniffed  — buffer→inv (cube/stash/inv)
	OpItemMoveFrom  = 0x19 // CORRECTED 2026-04-15 (Discord plaintext) — [19][itemGID:u32][source:u32][gridPos:u32][pad:u32] = 17B; pickup to cursor buffer (precedes sell/stash move)
	OpCubeTransmuteLegacy = 0x20 // D2 LOD format — DO NOT USE in D2R
	OpCubeTransmute      = 0x54 // D2R verified — [54][cubeGID:u32][const fields][footer]

	// === Item right-click actions ===
	OpIdentifyTome       = 0x26 // D2R verified — [26][00][tomeGID:u32][00ff][const][0207][footer]
	OpIdentifyItemLegacy = 0x27 // D2 LOD format — DO NOT USE in D2R (crashes or rejected)

	// === NPC dialog ===
	// 0x2F CRASHES send_fn from main thread — open dialog via HID click instead.
	// RE-VERIFIED 2026-04-20 against live buf=1 mirror: 13B form authoritative.
	OpNPCChatInit      = 0x2F // live 02_npc_chat_clean.json — [2F][npcGID:u32][npcGID:u32][npcPos:u32] = 13B (prior 5B Discord claim contradicted by sniffer)
	OpNPCChatTerminate = 0x30 // live 02_npc_chat_clean.json — 13B clean close; 22B post-trade variant (NewNPCChatTerminatePostTrade)

	// === Generic interact dispatcher (potion / buy / gamble / use) ===
	// 0x32 shares layout with 0x33 for the NPC-buy subform. Potion/gamble
	// subforms use different sizes and trailing flags — see NewUsePotion /
	// NewGambleBuy for those specific encodings.
	// RE-VERIFIED 2026-04-20: NPC buy constant bytes 13-16 are `09 00 08 00`
	// (same as 0x33 NPCSell), NOT `09 00 06 00` as stale code comment claimed.
	OpInteractDispatch = 0x32 // live 08_npc_with_trade.json entry#44 — NPC buy: [32][price:u32][itemGID:u32][npcGID:u32][09 00 08 00][slot:u16][seq:u16][term:u8] (22B)
	OpNPCSellItem      = 0x33 // live 2026-04-14 — [33][price:u32][itemGID:u32][npcGID:u32][09000000][slot:u16][seq:u16][term:u8][00] (22B, dual)

	// === NPC services ===
	OpNPCMenuClose       = 0x34 // sniffed — [34][02] (menu close ack, not same as 0x30)
	OpRepairAll          = 0x35 // CORRECTED 2026-04-15 (Discord plaintext capture) — [35][04][00 00][npcGID:u32][cost:u32][FFFFFFFF] = 16B
	OpNPCDialogResponse  = 0x38 // live 2026-04-14 — [38][option:u32][npcGID:u32] (9B, dual buffer)
	OpCubeOpA            = 0x46 // sniffed — cube item op variant 1
	OpEntityActionResult = 0x4D // CORRECTED 2026-04-15 (Discord plaintext capture) — [4D][npcGID:u32] = 5B (was wrongly 24B)
	OpHireMerc           = 0x52 // sniffed — [52][type:u32][merc_gid:u32][cost:u32][...]
	// OpCubeOpB was 0x54 — now consolidated into OpCubeTransmute above
	OpCainIdentifyItem   = 0x5C // sniffed/conflict (per-item, vs existing 0x34 all) — [5C][item_gid:u32][FFFFFFFF]

	// === Character ===
	OpAllocateStat = 0x3A // verified — [3A][stat_id:u16][0000]
	OpLearnSkill   = 0x3B // verified — [3B][skill_id:u16][0000]
	OpSelectSkill  = 0x3C // verified — [3C][skill_id:u16][00][btn:u8][FFFFFFFF]
	OpWeaponSwapLegacy = 0x60 // D2 LOD format — CRASHES D2R, DO NOT USE
	OpWeaponSwap       = 0x50 // D2R verified — [50][fromL:u32][fromR:u32][toL:u32][toR:u32][FF*8][00*3]
)

// UnitType values used by 0x32 dispatcher (and related opcodes).
const (
	UnitTypePlayer        = 0x00
	UnitTypeMonsterHostile = 0x01
	UnitTypeObject        = 0x02
	UnitTypeMissile       = 0x03
	UnitTypeItem          = 0x04
	UnitTypeTile          = 0x05
	UnitTypeGambledItem   = 0x06 // used by 0x32 gamble subform (NewGambleBuy)
	UnitTypeBeltPotion    = 0x4E // used by 0x32 belt-potion subform (NewUsePotion)
	// Note: 0x32 NPC buy subform does NOT use a unit-type byte at +9. The
	// bufpoll 2026-04-14 capture shows that offset is the merchant's GID.
)

// SubAction codes seen in 0x26 (item right-click).
const (
	SubActionScrollIdentify = 0x84 // sniffed in scroll-on-item flow
)
