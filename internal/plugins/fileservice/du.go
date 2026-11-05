package fileservice

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

type directorySizeResult struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Files       int64  `json:"files"`
	Directories int64  `json:"directories"`
	Errors      int64  `json:"errors"`
	Complete    bool   `json:"complete"`
}

type walkDirFunc func(string, fs.WalkDirFunc) error

// directorySize performs the on-demand recursive walk. filepath.WalkDir uses
// Lstat semantics and never follows directory symlinks.
func directorySize(ctx context.Context, path string) (directorySizeResult, error) {
	return directorySizeWithWalker(ctx, path, filepath.WalkDir)
}

func directorySizeWithWalker(ctx context.Context, path string, walk walkDirFunc) (directorySizeResult, error) {
	result := directorySizeResult{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, errors.New("path is not a directory: " + path)
	}
	err = walk(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			result.Errors++
			return nil
		}
		if entry.IsDir() {
			result.Directories++
			return nil
		}
		// Symlinks are not dereferenced or counted as their targets.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			result.Errors++
			return nil
		}
		if entryInfo.Mode().IsRegular() {
			result.Size += entryInfo.Size()
			result.Files++
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Complete = result.Errors == 0
	return result, nil
}

// du runs normal requests asynchronously so its subscription remains free to
// receive the protocol's best-effort _cancel frame for the same correlation.
func (p *Plugin) du(frame busclient.Frame) {
	value, _ := pluginrpc.Object(frame)
	corr, _ := value["_corr"].(string)
	if pluginrpc.Cancelled(frame) {
		if cancel, ok := p.duJobs.LoadAndDelete(corr); ok {
			cancel.(context.CancelFunc)()
		}
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if corr != "" {
		p.duJobs.Store(corr, cancel)
	}
	go func() {
		defer cancel()
		if corr != "" {
			defer p.duJobs.Delete(corr)
		}
		result, err := directorySize(ctx, path)
		if ctx.Err() != nil {
			return
		}
		if os.IsNotExist(err) {
			p.replyError(frame, "not_found", "directory not found: "+path)
			return
		}
		if err != nil {
			p.replyError(frame, "read_error", err.Error())
			return
		}
		p.reply(frame, result)
	}()
}

func cancelDirectoryJobs(jobs *sync.Map) {
	jobs.Range(func(_, value any) bool {
		value.(context.CancelFunc)()
		return true
	})
}
