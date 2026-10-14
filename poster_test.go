package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPosterFailureDoesNotFailHLS(t *testing.T) {
	a := testApplication(t)
	a.ffmpegBin = "fake"
	queueVideo(t, a, "no-poster")
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
		if filepath.Ext(args[len(args)-1]) == ".jpg" {
			return errors.New("no decodable poster frame")
		}
		return fakeHLS(ctx, bin, args)
	}
	startTestWorkers(t, a)
	m := waitStatus(t, a, "no-poster", "ready")
	if m.HLS == "" || m.Poster != "" || m.Error != "" {
		t.Fatalf("poster failure broke playback: %+v", m)
	}
}
