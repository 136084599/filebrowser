package sql

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/sqlitebusy"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func TestPersistentIndexRetainsOldUnchangedRowsAndUsesJournal(t *testing.T) {
	dir := t.TempDir()
	restore := pushTestIndexConfig(t, dir, testIndexSQLConfig("quickCheck"))
	defer restore()
	db, _, err := NewIndexDB("reliable", "OFF", 1000, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	file := &iteminfo.FileInfo{Path: "/unchanged.bin", ItemInfo: iteminfo.ItemInfo{Name: "unchanged.bin", Size: 99, ModTime: time.Now(), Type: "application/octet-stream"}}
	if err := db.InsertItem("src", file.Path, file); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE index_items SET last_updated = ?", time.Now().Add(-72*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var mode string
	var synchronous int
	db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	db.QueryRow("PRAGMA synchronous").Scan(&synchronous)
	if mode != "delete" || synchronous != 2 {
		t.Fatal("unsafe persistent index", mode, synchronous)
	}
	if err := db.handleIntegrityError(sqlitebusy.ErrBusy); err == nil {
		t.Fatal("busy check must be reported")
	}
	db.Close()
	db, fresh, err := NewIndexDB("reliable", "OFF", 1000, 8, false)
	if err != nil || fresh {
		t.Fatal("healthy old cache was replaced", fresh, err)
	}
	defer db.Close()
	got, err := db.GetItem("src", file.Path)
	if err != nil || got == nil || got.Size != 99 {
		t.Fatal("old unchanged record lost", got, err)
	}
}

func TestDamagedIndexIsPreservedBeforeRebuild(t *testing.T) {
	dir := t.TempDir()
	restore := pushTestIndexConfig(t, dir, testIndexSQLConfig("quickCheck"))
	defer restore()
	folder := filepath.Join(dir, "sql")
	os.MkdirAll(folder, 0700)
	original := []byte("this deliberately is not a SQLite database")
	path := filepath.Join(folder, "index_damaged.db")
	os.WriteFile(path, original, 0600)
	db, fresh, err := NewIndexDB("damaged", "OFF", 1000, 8, false)
	if err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	defer db.Close()
	backups, _ := filepath.Glob(path + ".corrupt-*")
	if len(backups) != 1 {
		t.Fatal("damaged original not preserved", backups)
	}
	got, _ := os.ReadFile(backups[0])
	if !bytes.Equal(got, original) {
		t.Fatal("backup content changed")
	}
}

func TestFailedOpenDoesNotDeletePersistentIndex(t *testing.T) {
	dir := t.TempDir()
	restore := pushTestIndexConfig(t, dir, testIndexSQLConfig("quickCheck"))
	defer restore()
	db, _, err := NewIndexDB("keep", "OFF", 1000, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	path := db.Path()
	db.Close()
	before, _ := os.ReadFile(path)
	_, err = NewTempDB("keep", &TempDBConfig{PersistentFile: filepath.Base(path), JournalMode: "invalid journal mode"})
	if err == nil {
		t.Fatal("expected configuration failure")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal(fmt.Errorf("persistent cache removed after open failure: %v", err))
	}
}

func TestFolderSizeBatchPersistsEveryParameterGroup(t *testing.T) {
	dir := t.TempDir()
	restore := pushTestIndexConfig(t, dir, testIndexSQLConfig("quickCheck"))
	defer restore()
	db, _, err := NewIndexDB("size_args", "OFF", 1000, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, path := range []string{"/", "/one/", "/one/empty/"} {
		row := &iteminfo.FileInfo{Path: path, ItemInfo: iteminfo.ItemInfo{Name: path, Type: "directory", Size: 999}}
		if err := db.InsertItem("size-source", path, row); err != nil {
			t.Fatal(err)
		}
	}
	wanted := map[string]uint64{"/": 1234, "/one/": 1000, "/one/empty/": 0}
	updated, err := db.UpdateFolderSizesIfChanged("size-source", wanted)
	if err != nil || updated != 3 {
		t.Fatal("batch did not persist folders", updated, err)
	}
	for path, size := range wanted {
		row, err := db.GetItem("size-source", path)
		if err != nil || row == nil || uint64(row.Size) != size {
			t.Fatal(path, row, err)
		}
	}
	updated, err = db.UpdateFolderSizesIfChanged("size-source", wanted)
	if err != nil || updated != 0 {
		t.Fatal("unchanged batch rewrote rows", updated, err)
	}
}
