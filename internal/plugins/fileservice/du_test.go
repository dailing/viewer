package fileservice

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"viewer/sdk/go/busclient"
)

func TestDirectorySizeDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 3), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.bin"), make([]byte, 7), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "large.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	result, err := directorySize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Size != 10 || result.Files != 2 || result.Directories != 2 || result.Errors != 0 || !result.Complete {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDirectorySizeReturnsPartialErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	walk := func(path string, fn fs.WalkDirFunc) error {
		if err := fn(filepath.Join(path, "unreadable"), nil, errors.New("permission denied")); err != nil {
			return err
		}
		return filepath.WalkDir(path, fn)
	}
	result, err := directorySizeWithWalker(context.Background(), root, walk)
	if err != nil {
		t.Fatal(err)
	}
	if result.Size != 2 || result.Errors != 1 || result.Complete {
		t.Fatalf("unexpected partial result: %+v", result)
	}
}

func TestDirectorySizeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := directorySize(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestDirectoryRPCCancelFrameStopsJob(t *testing.T) {
	plugin := &Plugin{}
	ctx, cancel := context.WithCancel(context.Background())
	plugin.duJobs.Store("corr-1", cancel)
	plugin.du(busclient.Frame{Channel: "file:_:du", Value: map[string]any{"_corr": "corr-1", "_cancel": true}})
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("du cancellation frame did not stop the walk")
	}
}
