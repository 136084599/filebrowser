package settings

// Compatibility for the original v1 configuration. Conversion happens in memory;
// the user's YAML and Bolt database are never rewritten.
import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	storm "github.com/asdine/storm/v3"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	bolt "go.etcd.io/bbolt"
)

func mapValue(parent map[string]interface{}, key string) map[string]interface{} {
	if m, ok := parent[key].(map[string]interface{}); ok {
		return m
	}
	m := map[string]interface{}{}
	parent[key] = m
	return m
}

func moveLegacyValue(from map[string]interface{}, key string, to map[string]interface{}, newKey string) error {
	value, ok := from[key]
	if !ok {
		return nil
	}
	if _, exists := to[newKey]; exists {
		return fmt.Errorf("both legacy %s and v2 %s are set; remove the duplicate", key, newKey)
	}
	to[newKey] = value
	delete(from, key)
	return nil
}

func convertLegacyConfig(config map[string]interface{}) error {
	server := mapValue(config, "server")
	if legacyPath, ok := server["database"].(string); ok && legacyPath != "" {
		sqlite := filepath.Join(filepath.Dir(legacyPath), "filebrowser.sqlite")
		server["database"] = map[string]interface{}{"path": sqlite, "migrateFrom": legacyPath}
	}
	for _, key := range []string{"port", "listen", "baseURL", "socket", "tlsKey", "tlsCert", "externalUrl", "internalUrl", "disableWebDAV", "disableRateLimit"} {
		if _, ok := server[key]; ok {
			if err := moveLegacyValue(server, key, mapValue(config, "http"), key); err != nil {
				return err
			}
		}
	}
	if ud, ok := config["userDefaults"].(map[string]interface{}); ok {
		mapping := map[string]string{
			"editorQuickSave": "fileViewer.editorQuickSave", "hideSidebarFileActions": "sidebar.hideFileActions", "disableQuickToggles": "sidebar.disableQuickToggles", "disableSearchOptions": "search.disableOptions", "stickySidebar": "sidebar.sticky", "darkMode": "ui.darkMode", "locale": "ui.locale", "viewMode": "listing.viewMode", "singleClick": "listing.singleClick", "showHidden": "listing.showHidden", "dateFormat": "listing.dateFormat", "gallerySize": "listing.gallerySize", "themeColor": "ui.themeColor", "quickDownload": "listing.quickDownload", "disablePreviewExt": "preview.disablePreviewExt", "disableViewingExt": "fileViewer.disableViewingExt", "lockPassword": "account.lockPassword", "disableSettings": "account.disableSettings", "disableUpdateNotifications": "account.disableUpdateNotifications", "deleteWithoutConfirming": "listing.deleteWithoutConfirming", "customTheme": "ui.customTheme", "showSelectMultiple": "listing.showSelectMultiple", "debugOffice": "fileViewer.debugOffice", "loginMethod": "account.loginMethod", "hideFileExt": "listing.hideFileExt", "showCopyPath": "listing.showCopyPath", "preferEditorForMarkdown": "fileViewer.preferEditorForMarkdown",
		}
		for key, dot := range mapping {
			if _, ok := ud[key]; ok {
				parts := strings.Split(dot, ".")
				if err := moveLegacyValue(ud, key, mapValue(ud, parts[0]), parts[1]); err != nil {
					return err
				}
			}
		}
		// Prefer the canonical OnlyOffice extension setting over its deprecated alias.
		if value, ok := ud["disableOnlyOfficeExt"]; ok {
			mapValue(ud, "fileViewer")["disableOnlyOfficeExt"] = value
			delete(ud, "disableOnlyOfficeExt")
			delete(ud, "disableOfficePreviewExt")
		} else if _, ok := ud["disableOfficePreviewExt"]; ok {
			if err := moveLegacyValue(ud, "disableOfficePreviewExt", mapValue(ud, "fileViewer"), "disableOnlyOfficeExt"); err != nil {
				return err
			}
		}
		if preview, ok := ud["preview"].(map[string]interface{}); ok {
			if err := moveLegacyValue(preview, "disableHideSidebar", mapValue(ud, "sidebar"), "disableHideOnPreview"); err != nil {
				return err
			}
			if err := moveLegacyValue(preview, "autoplayMedia", mapValue(ud, "fileViewer"), "autoplayMedia"); err != nil {
				return err
			}
			if value, ok := preview["defaultMediaPlayer"]; ok {
				if value != false {
					return fmt.Errorf("v2 removed defaultMediaPlayer:true; retain the old configuration and choose a supported player before upgrading")
				}
				delete(preview, "defaultMediaPlayer")
			}
		}
		if permissions, ok := ud["permissions"].(map[string]interface{}); ok {
			accountPermissions := mapValue(mapValue(ud, "account"), "permissions")
			for _, key := range []string{"admin", "api", "share", "realtime"} {
				if err := moveLegacyValue(permissions, key, accountPermissions, key); err != nil {
					return err
				}
			}
			if sources, ok := server["sources"].([]interface{}); ok {
				for _, rawSource := range sources {
					if source, ok := rawSource.(map[string]interface{}); ok {
						defaults := mapValue(mapValue(source, "config"), "defaultPermissions")
						for _, key := range []string{"view", "download", "modify", "create", "delete"} {
							if v, ok := permissions[key]; ok {
								defaults[key] = v
							}
						}
						if _, ok := defaults["view"]; !ok {
							defaults["view"] = true
						}
					}
				}
			}
			delete(ud, "permissions")
		}
	}
	if sources, ok := server["sources"].([]interface{}); ok {
		for _, rawSource := range sources {
			source, ok := rawSource.(map[string]interface{})
			if !ok {
				continue
			}
			cfg := mapValue(source, "config")
			if v, exists := cfg["disableIndexing"]; exists {
				if v == true {
					cfg["rules"] = appendRules(cfg["rules"], map[string]interface{}{"folderPath": "/", "viewable": true})
				}
				delete(cfg, "disableIndexing")
			}
			delete(cfg, "indexingIntervalMinutes")
			if old, ok := cfg["conditionals"].(map[string]interface{}); ok {
				if rules, ok := old["rules"].([]interface{}); ok {
					for _, rule := range rules {
						cfg["rules"] = appendRules(cfg["rules"], rule)
					}
				}
				global := map[string]interface{}{"folderPath": "/"}
				for _, key := range []string{"ignoreHidden", "ignoreZeroSizeFolders", "ignoreSymlinks"} {
					if v, ok := old[key]; ok && v == true {
						global[key] = v
					}
				}
				if len(global) > 1 {
					cfg["rules"] = appendRules(cfg["rules"], global)
				}
				delete(cfg, "conditionals")
			}
		}
	}
	return restoreLegacySourceRoots(server)
}

func appendRules(value interface{}, rule interface{}) []interface{} {
	rules, _ := value.([]interface{})
	return append(rules, rule)
}

// A v1 './' source can move when the replacement executable is unpacked into a
// new directory. Recover the recorded absolute root only when unambiguous.
func restoreLegacySourceRoots(server map[string]interface{}) error {
	db, ok := server["database"].(map[string]interface{})
	if !ok {
		return nil
	}
	legacyPath, _ := db["migrateFrom"].(string)
	if legacyPath == "" || legacyPath == "default" {
		return nil
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return nil
	}
	old, err := storm.Open(legacyPath, storm.BoltOptions(0600, &bolt.Options{ReadOnly: true, Timeout: 3 * time.Second}))
	if err != nil {
		return fmt.Errorf("cannot read legacy database (stop the old FileBrowser before upgrading): %w", err)
	}
	defer old.Close()
	var list []users.User
	if err := old.All(&list); err != nil && err != storm.ErrNotFound {
		return err
	}
	roots := map[string]bool{}
	for _, user := range list {
		for _, scope := range user.FrontendScopes {
			if filepath.IsAbs(scope.Name) {
				roots[scope.Name] = true
			}
		}
	}
	sources, _ := server["sources"].([]interface{})
	if len(sources) == 1 && len(roots) == 1 {
		source, ok := sources[0].(map[string]interface{})
		if !ok {
			return nil
		}
		p, _ := source["path"].(string)
		if p == "." || p == "./" || p == ".\\" {
			for root := range roots {
				stat, err := os.Stat(root)
				if err != nil || !stat.IsDir() {
					return fmt.Errorf("original source root %q is unavailable; refusing to change user scopes", root)
				}
				source["path"] = root
			}
		}
	}
	return nil
}
