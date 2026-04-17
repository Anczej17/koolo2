# Full Packet Migration Specification

## Existing Packet Patterns (from codebase audit)

All packets use:
- Opcode = byte[0]
- Little-Endian for all multi-byte fields
- binary.LittleEndian.PutUint16/PutUint32
- Coordinates: uint16 (cast/movement) or uint32 (pickup)
- GID/UnitID: always uint32 LE

## New Packets Needed

### 0x01 — Walk to Location (5 bytes)
```
[0x01][X:u16 LE][Y:u16 LE]
```
Same layout as 0x0C (CastSkillLocation).

### 0x03 — Run to Location (5 bytes)
```
[0x03][X:u16 LE][Y:u16 LE]
```
Same layout as walk.

### 0x05 — Left Skill at Location (5 bytes)
```
[0x05][X:u16 LE][Y:u16 LE]
```
Same layout as 0x0C but for left-click skill.

### 0x13 — Entity/NPC Interact (9 bytes)
```
[0x13][EntityType:u32 LE][EntityGID:u32 LE]
```
EntityType: 1=Unit(NPC), 2=Object. Same layout as CastSkillEntityRight.
NOTE: 0x41 (TpInteraction) already handles some objects. 0x13 is the general interact.

### 0x60 — Weapon Swap (1 byte)
```
[0x60]
```
Just the opcode. Simplest packet.

### 0x49 — Waypoint Travel (3 bytes)
```
[0x49][WaypointID:u16 LE]
```
WaypointID is the destination waypoint enum.

### 0x27 — Identify Item (5 bytes)
```
[0x27][ItemGID:u32 LE]
```

### 0x35 — Repair (5 bytes)  
```
[0x35][NPCGID:u32 LE]
```
Repair all items at NPC.

## Call Sites Summary

### Tier 1 — Weapon Swap (13 sites)
- buff.go:636 (pressSwapWeapons) — CENTRAL
- step/swap_weapon.go:38 — STEP LEVEL
- 5x berserk/whirlwind/warcry barb PressKey('W')
- item.go:304,315
- autoequip.go:243,275
- run/helpers.go:35
- single_supervisor.go:393

### Tier 1 — NPC Interact (1 impl, 50+ callers)
- step/interact_npc.go:73-79 — SINGLE POINT TO PATCH

### Tier 1 — Walk/Run (3 sites)
- pather/utils.go:25,685 — CENTRAL MOVEMENT
- run_walk.go:51 — PROBE (keep HID)

### Tier 2 — Skill Selection (30 sites)
- 0x3C already implemented
- character/*.go bypass step layer
- Need to route through step.SelectRightSkill/SelectLeftSkill

### Tier 3 — Town
- Waypoint, Identify, Repair — separate packets needed

## Config Flags (existing)
- PacketCasting.UseForTeleport
- PacketCasting.UseForSkillSelection
- PacketCasting.UseForEntitySkills
- PacketCasting.UseForItemPickup
- PacketCasting.UseForEntranceInteraction
- PacketCasting.UseForTpInteraction

## Config Flags (new, needed)
- PacketCasting.UseForMovement (walk/run)
- PacketCasting.UseForWeaponSwap
- PacketCasting.UseForNPCInteraction
- PacketCasting.UseForWaypoint
- PacketCasting.UseForIdentify
- PacketCasting.UseForRepair
