package settings

import (
	"reflect"
	"testing"
)

func TestLegacyConfigPreservesDefaults(t *testing.T) {
	config := map[string]interface{}{"server": map[string]interface{}{"database": "data/database.db", "port": 80, "sources": []interface{}{map[string]interface{}{"path": "/missing", "config": map[string]interface{}{"disableIndexing": false}}}}, "userDefaults": map[string]interface{}{"locale": "cn", "disableOnlyOfficeExt": ".md .txt", "disableOfficePreviewExt": "", "fileLoading": map[string]interface{}{"maxConcurrentUpload": 10, "uploadChunkSizeMb": 10}, "preview": map[string]interface{}{"highQuality": true, "image": true, "defaultMediaPlayer": false}, "permissions": map[string]interface{}{"admin": false, "download": false, "create": false}}}
	if err := convertLegacyConfig(config); err != nil {
		t.Fatal(err)
	}
	server := config["server"].(map[string]interface{})
	db := server["database"].(map[string]interface{})
	if db["migrateFrom"] != "data/database.db" {
		t.Fatal("old database path was discarded")
	}
	if config["http"].(map[string]interface{})["port"] != 80 {
		t.Fatal("port changed")
	}
	ud := config["userDefaults"].(map[string]interface{})
	if !reflect.DeepEqual(ud["fileLoading"], map[string]interface{}{"maxConcurrentUpload": 10, "uploadChunkSizeMb": 10}) {
		t.Fatal("upload defaults changed")
	}
	if ud["preview"].(map[string]interface{})["highQuality"] != true {
		t.Fatal("preview default lost")
	}
	if ud["fileViewer"].(map[string]interface{})["disableOnlyOfficeExt"] != ".md .txt" {
		t.Fatal("empty deprecated alias replaced canonical setting")
	}
	if ud["ui"].(map[string]interface{})["locale"] != "cn" {
		t.Fatal("locale changed")
	}
}
func TestLegacyConfigRejectsAmbiguousHTTPSettings(t *testing.T) {
	if err := convertLegacyConfig(map[string]interface{}{"server": map[string]interface{}{"port": 80}, "http": map[string]interface{}{"port": 8080}}); err == nil {
		t.Fatal("conflicting ports silently accepted")
	}
}
