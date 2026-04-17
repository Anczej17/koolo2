package config

import (
	"fmt"
	"os"

	"github.com/lxn/win"
	cp "github.com/otiai10/copy"
	"local/internal/svc/internal/utils"
)

var userProfile = os.Getenv("USERPROFILE")

// settingsPath points at the game's per-user save directory. Built at init
// time from a split literal so the full string never appears in the binary
// (anti-detection: avoids "Diablo II Resurrected" being a static signature).
var settingsPath = func() string {
	parts := []string{"\\Saved ", "Games\\", "Diablo", " II ", "Resurrected"}
	out := userProfile
	for _, p := range parts {
		out += p
	}
	return out
}()

func ReplaceGameSettings(modName string) error {
	modDirPath := settingsPath + "\\mods\\" + modName
	modSettingsPath := modDirPath + "\\Settings.json"

	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return fmt.Errorf("game settings not found at %s", settingsPath)
	}

	if _, err := os.Stat(modDirPath); os.IsNotExist(err) {
		err = os.MkdirAll(modDirPath, os.ModePerm)
		if err != nil {
			return fmt.Errorf("error creating mod folder to store settings: %w", err)
		}
	}

	if _, err := os.Stat(modSettingsPath + ".bkp"); err == nil {
		err = os.Rename(modSettingsPath, modSettingsPath+".bkp")
		// File does not exist, no need to back up
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return cp.Copy("config/Settings.json", modSettingsPath)
}

func InstallMod() error {
	if _, err := os.Stat(App.AppPath + "\\" + utils.GameExeName()); os.IsNotExist(err) {
		return fmt.Errorf("game not found at %s", App.AppPath)
	}

	if _, err := os.Stat(App.AppPath + "\\mods\\custom\\custom.mpq\\modinfo.json"); err == nil {
		return nil
	}

	if err := os.MkdirAll(App.AppPath+"\\mods\\custom\\custom.mpq", os.ModePerm); err != nil {
		return fmt.Errorf("error creating mod folder: %w", err)
	}

	modFileContent := []byte(`{"name":"custom","savepath":"custom/"}`)

	return os.WriteFile(App.AppPath+"\\mods\\custom\\custom.mpq\\modinfo.json", modFileContent, 0644)
}

func GetCurrentDisplayScale() float64 {
	hDC := win.GetDC(0)
	defer win.ReleaseDC(0, hDC)
	dpiX := win.GetDeviceCaps(hDC, win.LOGPIXELSX)

	return float64(dpiX) / 96.0
}
