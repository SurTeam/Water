package goconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Save updates known fields while retaining other versions' configuration keys.
// Rename publishes a complete file, so readers cannot observe a partial write.
func Save(path string, cfg AppConfig) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	root := map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return err
		}
		if root == nil {
			return fmt.Errorf("settings must be a JSON object")
		}
	}
	target := root
	if value, exists := root["overrides"]; exists && value != nil {
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("settings overrides must be an object")
		}
	}
	if overrides, ok := root["overrides"].(map[string]any); ok {
		target = overrides
	}
	encoded, err := json.Marshal(cfg.Normalized())
	if err != nil {
		return err
	}
	var known map[string]any
	if err = json.Unmarshal(encoded, &known); err != nil {
		return err
	}
	mergeConfig(target, known)
	if ui, ok := target["ui"].(map[string]any); ok {
		if _, legacy := ui["ui_font_size"]; legacy {
			ui["ui_font_size"] = cfg.UI.UIFontSize
		}
	}
	data, err = json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".water-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return os.Rename(f.Name(), path)
}

func mergeConfig(dst, src map[string]any) {
	for k, v := range src {
		object, ok := v.(map[string]any)
		if existing, exists := dst[k].(map[string]any); ok && exists {
			mergeConfig(existing, object)
		} else {
			dst[k] = v
		}
	}
}
