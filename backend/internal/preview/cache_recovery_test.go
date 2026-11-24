package preview

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func TestEmptyOrTruncatedPreviewCacheRegenerates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 240, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 240; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
		}
	}
	png.Encode(f, img)
	f.Close()
	stat, _ := os.Stat(path)
	previous := service
	service = NewPreviewGenerator(1, dir)
	defer func() { service = previous }()
	file := iteminfo.ExtendedFileInfo{RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "fixture.png", Type: "image/png", Size: stat.Size(), ModTime: stat.ModTime(), HasPreview: true}}}
	sum := md5.Sum([]byte(fmt.Sprintf("%s:%d:%s", path, stat.Size(), stat.ModTime().Format(time.RFC3339Nano))))
	key := CacheKey(hex.EncodeToString(sum[:]), "small", 0)
	for _, bad := range [][]byte{nil, []byte("truncated")} {
		service.fileCache.Store(context.Background(), key, bad)
		result, err := GetPreviewForFile(context.Background(), file, "small", "", 0)
		if err != nil || len(result) < minPreviewSize {
			t.Fatal("preview stayed poisoned", len(result), err)
		}
	}
}
