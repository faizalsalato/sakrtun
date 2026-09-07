package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Settings holds app-wide preferences that are not part of a tunnel profile.
type Settings struct {
	// KillSwitch blocks internet access whenever the VPN is not connected.
	KillSwitch bool `json:"kill_switch"`
}

// DefaultSettings returns the zero-value settings used when no settings file
// exists yet.
func DefaultSettings() Settings {
	return Settings{}
}

func settingsPath(root string) string {
	return filepath.Join(root, "configs", "settings.json")
}

// LoadSettings reads the global settings file. A missing file is not an error:
// the defaults are returned instead.
func LoadSettings(root string) (Settings, error) {
	b, err := os.ReadFile(settingsPath(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return DefaultSettings(), err
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return DefaultSettings(), err
	}
	return s, nil
}

// SaveSettings persists the global settings file atomically.
func SaveSettings(root string, s Settings) error {
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(settingsPath(root), b, 0o644)
}
