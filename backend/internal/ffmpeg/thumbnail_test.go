package ffmpeg

import (
	"bytes"
	"context"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealVideoThumbnailSizesAndShortClipFallback(t *testing.T) {
	binary := os.Getenv("FILEBROWSER_TEST_FFMPEG")
	if binary == "" {
		t.Skip("requires test FFmpeg")
	}
	previous := global
	defer func() { global = previous }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Initialize(ctx, InitOptions{FFmpegPath: binary, MaxConcurrent: 2, SkipHWTests: true, CacheDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, dimensions := range []string{"180x320", "128x72"} {
		video := filepath.Join(t.TempDir(), "short.mp4")
		cmd := exec.CommandContext(ctx, binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size="+dimensions+":rate=8", "-t", "0.25", "-c:v", "libx264", "-threads", "2", "-pix_fmt", "yuv420p", video)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
		for _, percentage := range []int{0, 50} {
			var data bytes.Buffer
			if err := Get().VideoThumbnail(ctx, &data, video, percentage, 256, 256); err != nil {
				t.Fatal(err)
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(data.Bytes()))
			if err != nil || cfg.Width > 256 || cfg.Height > 256 {
				t.Fatal("thumbnail dimensions", cfg, err)
			}
			if dimensions == "128x72" && (cfg.Width != 128 || cfg.Height != 72) {
				t.Fatal("small video was enlarged", cfg)
			}
			if dimensions == "180x320" && cfg.Width >= cfg.Height {
				t.Fatal("portrait aspect ratio lost", cfg)
			}
		}
	}
}
