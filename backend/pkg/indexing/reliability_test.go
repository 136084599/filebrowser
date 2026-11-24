package indexing

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestCachedZeroDirectorySizeOverridesLaggingDatabase(t *testing.T) {
	idx, _, cleanup := setupTestIndex(t)
	defer cleanup()
	row := &iteminfo.FileInfo{Path: "/zero/", ItemInfo: iteminfo.ItemInfo{Name: "zero", Type: "directory", Size: 999}}
	if err := idx.db.InsertItem(idx.Name, row.Path, row); err != nil {
		t.Fatal(err)
	}
	idx.SetFolderSize(row.Path, 0)
	got, ok := idx.GetReducedMetadata(row.Path, true)
	if !ok || got.Size != 0 {
		t.Fatal("empty folder resurrected stale DB size", got)
	}
}

func TestQuickScanUpdatesAncestorSizesAndDiscoversNewFolders(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.MkdirAll(filepath.Join(source, "素材", "下层"), 0755)
	os.WriteFile(filepath.Join(source, "root.bin"), make([]byte, 100), 0600)
	os.WriteFile(filepath.Join(source, "素材", "a.bin"), make([]byte, 500), 0600)
	os.WriteFile(filepath.Join(source, "素材", "下层", "b.bin"), make([]byte, 1000), 0600)
	db, _, err := dbsql.NewIndexDB("regression_scan", "OFF", 1000, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	name := t.Name()
	db.DeleteItem(name, "/", true)
	idx := &Index{Source: settings.Source{Name: name, Path: source, Config: settings.SourceConfig{UseLogicalSize: true}}, db: db, folderSizes: map[string]uint64{}, folderSizesUnsynced: map[string]struct{}{}, scanUpdatedPaths: map[string]bool{}}
	r := &Scanner{scanPath: "/", idx: idx}
	c := &Scanner{scanPath: "/素材/", idx: idx}
	idx.scanners = map[string]*Scanner{"/": r, "/素材/": c}
	r.prepareScanState()
	r.runFullScanRoot()
	c.prepareScanState()
	c.runFullScanChild()
	if size, err := idx.indexedRootSize(); err != nil || size != 1600 {
		t.Fatal("initial root size", size, err)
	}
	os.MkdirAll(filepath.Join(source, "素材", "新目录"), 0755)
	os.WriteFile(filepath.Join(source, "素材", "新目录", "c.bin"), make([]byte, 2000), 0600)
	c.lastScanned = time.Now().Add(-time.Hour)
	c.prepareScanState()
	c.runQuickScanChild()
	idx.PostScan()
	if size, _ := idx.GetFolderSize("/素材/"); size != 3500 {
		t.Fatal("quick scan did not update source parent", size)
	}
	if size, err := idx.indexedRootSize(); err != nil || size != 3600 {
		t.Fatal("root total stale", size, err)
	}
	if _, err := db.Exec("UPDATE index_items SET last_updated=? WHERE source=? AND is_dir=0", time.Now().Add(-72*time.Hour).Unix(), name); err != nil {
		t.Fatal(err)
	}
	c.lastScanned = time.Now().Add(time.Hour)
	c.prepareScanState()
	c.runQuickScanChild()
	if item, err := db.GetItem(name, "/素材/下层/b.bin"); err != nil || item == nil || item.Size != 1000 {
		t.Fatal("unchanged old entry removed", item, err)
	}
	if err := idx.RefreshDirectory("/素材/下层/", false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(source, "素材", "下层", "b.bin"), make([]byte, 3000), 0600)
	if err := idx.RefreshDirectory("/素材/下层/", false); err != nil {
		t.Fatal(err)
	}
	if size, _ := idx.GetFolderSize("/素材/"); size != 5500 {
		t.Fatal("upload/edit did not update ancestors", size)
	}
	if err := os.Rename(source, source+"-offline"); err != nil {
		t.Fatal(err)
	}
	c.executeScan()
	if item, err := db.GetItem(name, "/素材/下层/b.bin"); err != nil || item == nil || item.Size != 3000 {
		t.Fatal("offline source destroyed indexed files", item, err)
	}
	if idx.scanners["/素材/"] == nil {
		t.Fatal("offline source discarded scanner")
	}
}
