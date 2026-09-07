package app

import (
	"os"
	"path/filepath"

	"socksrevivepc/internal/config"
	"socksrevivepc/internal/engine"
)

type App struct {
	Root     string
	Store    *config.Store
	Engine   *engine.Manager
	Settings config.Settings
}

func New() (*App, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"configs", "logs", filepath.Join("tools", "dnstt"), filepath.Join("tools", "xray"), filepath.Join("tools", "wintun")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	store, err := config.NewStore(filepath.Join(root, "profiles"))
	if err != nil {
		return nil, err
	}
	settings, err := config.LoadSettings(root)
	if err != nil {
		return nil, err
	}
	// Make helper tools (openvpn, xray, dnstt) resolvable by bare name even
	// when their installers did not register a PATH entry.
	setupToolPaths(root)
	mgr := engine.NewManager(root)
	if err := removeBundledExamples(store); err != nil {
		return nil, err
	}
	app := &App{Root: root, Store: store, Engine: mgr, Settings: settings}
	return app, nil
}

// SaveSettings persists the global app settings (for example the kill switch
// state) to configs/settings.json.
func (a *App) SaveSettings() error {
	return config.SaveSettings(a.Root, a.Settings)
}

func removeBundledExamples(store *config.Store) error {
	list, err := store.List()
	if err != nil {
		return err
	}
	exampleNames := map[string]bool{
		"Example - Direct SSH":    true,
		"Example - Payload + SSL": true,
		"Example - Xray Core":     true,
	}
	for _, p := range list {
		if exampleNames[p.Name] {
			if err := store.Delete(p.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
