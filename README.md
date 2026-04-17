This fork is dedicated to completing the leveling feature and improving the application.
All classes are now supported for leveling.

Major changes include:

* Optional usage of packets
* Leveling sequencer + editor (customizable leveling)
* Complete movement overhaul
* Torch-System (Ubers)
* Terror zones rework
* Shopping
* Dropper
* Auto-Mule
* Barb improvements
* Improved Runeword maker (rerolling)
* Pickit editor
* Discord integration overhaul
* Buff rework
* Many qol features like mass-profile-management, improved class-selection, auto-char-creation, mass-auto-starting, etc.

Please know that this project is still in development, so you may encounter issues. You are invited to add possible fixes or new features via pull requests.

Important information:

As written more detailed below you need to install:

- Go (1.24, not 1.25!)
- Garble (0.14.2, not 0.15.x)

Make sure the game is set to English to prevent any language-related issues.

Use better_build.bat to build the application.

- Choose your class and then pick the corresponding leveling build.
- Choose "leveling" OR "leveling_sequence" as enabled run under "Run Settings". Don't enable them both at the same time as this will lead to faulty behaviour. We will remove "leveling" soon, so the sequencer will be the default.
- Start your char with lvl 1 as the autoconfig currently works at lvl 1 only. Alternatively you can check leveling.go and leveling_sequence.go respectively for detailed manual config. This handling will be enhanced later.

---
<div style="background-color: #FFFACD; padding: 10px; border-radius: 5px; text-align: center">
  <h2 style="margin: 0;">Warning: Use at your own risk.</h2>
</div>
<p align="center">
  <img src="assets/app.webp" alt="Application" width="150">
</p>
<h3 align="center">Application</h3>

---

App is a tool for Diablo II: Resurrected (Expansion). Built for informational and educational purposes
only, it's not intended for online usage. Feel free to contribute opening pull requests with new features or fixes.
App reads game memory and interacts with the game window. As good as it can.

## Disclaimer
Can I get banned for using App? Yes, you can get banned. I'm not responsible for any ban or any other consequence that may arise from it.

## Features
- Blizzard Sorceress, Nova Sorceress, FoH, Berserk Barbarian Hork (Travincal), Mosaic are currently supported. Hammerdin, Javazon and Winddruid are WIP
- Supported runs: Countess, Andariel, Ancient Tunnels, Summoner, Mephisto, Council, Eldritch-Shenk, Endugu, Drifter Cavern, Pindleskin, Nihlathak,
  Tristram, Lower Kurast and Superchests, Stony Tomb, The Pit, Arachnid Lair, Baal, Duriel, Tal Rasha Tombs, Diablo, Cows, Treshsocket
- Multi window support (run multiple instances at the same time)
- Integration for Discord and Telegram
- "Companion mode" one leader will be creating games and the rest will join the game... (not working currently)
- Pickit based on NIP files
- Auto potion for health and mana (also mercenary)
- Chicken when low health
- Inventory slot locking
- Revive mercenary
- CTA buff and class buffs
- Auto repair
- Skip on immune
- Auto leveling sorceress and paladin (WIP) this feature is not finished.
- Auto gambling
- Auto cubing and crafting (WIP)
- Terror Zones (WIP)
- Classic is not supported

## Requirements
- Diablo II: Resurrected (1280x720 required, windowed mode, ensure accessibility large fonts disabled)
- **Diablo II: LOD 1.13c** (IMPORTANT: It will **NOT** work without it, this step is not optional)

## Quick Start
### Preparing the character
- App will read game keybindings in order to use the skills, doesn't matter what key is used, but the skills for the build must be set.
- For blizzard sorceress, set the **left** skill to Glacial Spike or Ice Blast, and for Hammerdin to Blessed Hammer.
- Foh set keybind for FOH and holybolt on left skill, conviction on right skill
- Berserk barb set berserk as left skill. Also to use FindItem you need higher goldfind on secondary weapons slot. Alibaba + anything will work.
- Buy TP and ID tomes and one stack of keys and keep them in the inventory, additionally set the TP tome to a key binding, this is **required**.
- Horadric Cube can be stashed or kept in inventory, App will use it to cube recipes if enabled.
- Keep the charms in the inventory, App can be configured to lock specific inventory slots.

### Running the tool
- If you haven't done yet, install **Diablo II: LOD 1.13c** (required)
- Download the latest release (recommended for most users), or alternatively you can [build it from source](#development-environment)
- Extract the zip file in a directory of your choice.
- Run the executable.
- Follow the setup wizard, it will guide you through the process of setting up the application. You will need to configure some directories and character settings.
- If you want to back up/restore your configuration, you can find the configuration files in the `config` directory.

## Pickit rules
Item pickit is based on NIP files, you can find them in the `config/{character}/pickit` directory.

All the .nip files contained in the pickit directory will be loaded, so you can have multiple pickit files.

There are some considerations to take into account:
- If item fully matches the pickit rule before being identified, it will be picked up and stashed unidentified.
- If item doesn't match the full rule, will be identified and checked again, if fully matches a rule it will be stashed otherwise sold to vendor.
- If there is an error on the NIP file or App can not understand it, the application will not start.
- Pickit rules can not be changed in runtime (yet), you will need to restart App to apply changes.

## Development environment
**Note:** This is only required if you want to build the project from source.

Setting the development environment is pretty straightforward, but the following dependencies are **required** to build the project.

### Dependencies
- [Download Go 1.24](https://go.dev/dl/) <ins>**not the version 1.25**</ins> 
- [Install git](https://gitforwindows.org/)

### Building from source

First, we open the terminal and install [Garble](https://github.com/burrowers/garble) using the following command:
```shell
go install mvdan.cc/garble@v0.14.2
```

Next, run the following commands in project root directory:
```shell
better_build.bat
```
This will produce the "build" directory with the executable file and all the required assets.

### Updating with latest changes
In order to fetch latest `main` branch changes run the following commands in project root directory:
```shell
git pull
better_build.bat
```
**Note**: If you use `build.bat`, the `build` directory **will be deleted**, so if you customized any file(s) in there, make sure to backup it before running `build.bat`.
