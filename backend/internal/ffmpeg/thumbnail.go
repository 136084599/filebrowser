package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	ffexec "github.com/gtsteffaniak/go-ffmpeg/exec"
)

// VideoThumbnail scales inside FFmpeg, avoiding a full-resolution JPEG round trip.
// The initial thumbnail uses an early keyframe without a separate duration probe.
// Explicit percentages (hover/scrubbing) retain precise seeking and cached duration.
func (s *Service) VideoThumbnail(ctx context.Context, w io.Writer, path string, percentage, width, height int) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("ffmpeg service not available")
	}
	if width < 1 || height < 1 {
		return fmt.Errorf("invalid thumbnail dimensions")
	}
	if _, _, err := resolveLocalMediaPath(path); err != nil {
		return err
	}
	seek := "1.000"
	fast := percentage <= 0
	if !fast {
		if percentage >= 100 {
			percentage = 99
		}
		duration, err := s.GetMediaDuration(ctx, path)
		if err != nil {
			return err
		}
		seek = fmt.Sprintf("%.3f", duration*float64(percentage)/100)
	}
	if err := s.Acquire(ctx); err != nil {
		return err
	}
	defer s.Release()
	run := func(position string, keyframe bool) ([]byte, error) {
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "2"}
		if keyframe {
			args = append(args, "-skip_frame", "nokey", "-noaccurate_seek")
		}
		args = append(args, "-ss", position, "-i", path, "-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", "1", "-filter_threads", "1", "-vf", fmt.Sprintf("scale='min(iw,%d)':'min(ih,%d)':force_original_aspect_ratio=decrease", width, height), "-threads", "1", "-q:v", "4", "-vcodec", "mjpeg", "-f", "image2pipe", "pipe:1")
		cmd := ffexec.CommandContext(ctx, s.FFmpegPath(), args...)
		var output, stderr bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("thumbnail extraction: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		if output.Len() < 100 {
			return nil, fmt.Errorf("video produced no thumbnail frame")
		}
		return output.Bytes(), nil
	}
	data, err := run(seek, fast)
	if err != nil && fast && ctx.Err() == nil {
		data, err = run("0", false)
	}
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
