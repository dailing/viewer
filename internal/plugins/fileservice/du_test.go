package fileservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirectorySizeIncrementalChunksAndDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	for index := 0; index < 300; index++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.bin", index)), []byte{1}, 0o644); err != nil {
			t.Fatal(err)
		}
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
	var batches []directorySizeResult
	result, err := directorySizeIncremental(context.Background(), root, 64, func(partial directorySizeResult) {
		batches = append(batches, partial)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Size != 307 || result.Files != 301 || result.Directories != 2 || result.Errors != 0 || !result.Complete || !result.Done {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(batches) < 5 {
		t.Fatalf("progress batches = %d, want chunked updates", len(batches))
	}
	for _, batch := range batches {
		if batch.Done || batch.Complete {
			t.Fatalf("partial batch marked final: %+v", batch)
		}
	}
}

func TestDirectorySizeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := directorySize(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestDirectorySizerDeduplicatesAndPublishesCompletion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := make(chan directorySizeResult, 10)
	sizer := newDirectorySizer(func(_ string, value any) error {
		events <- value.(directorySizeResult)
		return nil
	})
	defer sizer.close()
	if status := sizer.enqueue(root); status != "queued" {
		t.Fatalf("first status = %q", status)
	}
	if status := sizer.enqueue(root); status != "queued" && status != "running" {
		t.Fatalf("duplicate status = %q", status)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if !event.Done {
				continue
			}
			if event.Cancelled || event.Size != 2 || event.Files != 1 || !event.Complete {
				t.Fatalf("unexpected completion: %+v", event)
			}
			goto completed
		case <-deadline:
			t.Fatal("directory sizing did not complete")
		}
	}
completed:
	select {
	case event := <-events:
		t.Fatalf("duplicate job published another event: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestDirectorySizerCancelsQueuedPath(t *testing.T) {
	root := t.TempDir()
	events := make(chan directorySizeResult, 1)
	sizer := newDirectorySizer(func(_ string, value any) error {
		events <- value.(directorySizeResult)
		return nil
	})
	defer sizer.close()
	sizer.enqueue(root)
	if !sizer.cancelPath(root) {
		t.Fatal("queued path was not registered")
	}
	select {
	case event := <-events:
		if !event.Done || !event.Cancelled || event.Path != root {
			t.Fatalf("unexpected cancellation: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation was not published")
	}
}
