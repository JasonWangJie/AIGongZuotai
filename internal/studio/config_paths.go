package studio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Resolve existing ancestors too: build or asset directories may not exist yet.
func resolveConfigPath(name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", err
	}
	base, err := resolveConfigPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, filepath.Base(abs)), nil
}
func validateAssetSeparation(webDir, assetDir string) error {
	if assetDir == "" {
		assetDir = ".runtime/assets"
	}
	web, err := resolveConfigPath(webDir)
	if err != nil {
		return errors.New("cannot resolve APP_WEB_DIR")
	}
	assets, err := resolveConfigPath(assetDir)
	if err != nil {
		return errors.New("cannot resolve ASSET_DIR")
	}
	// Case folding is conservative on case-sensitive systems and correct on Windows.
	web, assets = strings.ToLower(filepath.Clean(web)), strings.ToLower(filepath.Clean(assets))
	separator := string(filepath.Separator)
	if web == assets || strings.HasPrefix(assets, web+separator) || strings.HasPrefix(web, assets+separator) {
		return errors.New("ASSET_DIR and APP_WEB_DIR must be separate directory trees; private media cannot be served as public static files")
	}
	return nil
}
