package fileservice

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const (
	DirectorySizeProgressChannel = "file:_:du:progress"
	directoryReadBatch           = 128
	directoryWorkerPace          = 2 * time.Millisecond
	directoryProgressInterval    = 100 * time.Millisecond
)

type directorySizeResult struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Files       int64  `json:"files"`
	Directories int64  `json:"directories"`
	Errors      int64  `json:"errors"`
	Complete    bool   `json:"complete"`
	Done        bool   `json:"done"`
	Cancelled   bool   `json:"cancelled,omitempty"`
	Error       string `json:"error,omitempty"`
}

// directorySizeIncremental reads at most batch entries per syscall. It uses an
// explicit queue and Lstat semantics, so directory symlinks are never followed.
func directorySizeIncremental(ctx context.Context, path string, batch int, onBatch func(directorySizeResult)) (directorySizeResult, error) {
	result := directorySizeResult{Path: path}
	info, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("path is not a directory: " + path)
	}
	if batch <= 0 {
		batch = directoryReadBatch
	}
	pending := []string{path}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		current := pending[0]
		pending = pending[1:]
		directory, openErr := os.Open(current)
		if openErr != nil {
			if current == path {
				return result, openErr
			}
			result.Errors++
			continue
		}
		result.Directories++
		for {
			entries, readErr := directory.ReadDir(batch)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					_ = directory.Close()
					return result, err
				}
				// DirEntry uses Lstat semantics. Symlinks are not dereferenced or
				// counted as their targets, including links to directories.
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				childPath := filepath.Join(current, entry.Name())
				if entry.IsDir() {
					pending = append(pending, childPath)
					continue
				}
				entryInfo, infoErr := entry.Info()
				if infoErr != nil {
					result.Errors++
					continue
				}
				if entryInfo.Mode().IsRegular() {
					result.Size += entryInfo.Size()
					result.Files++
				}
			}
			if len(entries) > 0 && onBatch != nil {
				onBatch(result)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				result.Errors++
				break
			}
		}
		if closeErr := directory.Close(); closeErr != nil {
			result.Errors++
		}
	}
	result.Complete = result.Errors == 0
	result.Done = true
	return result, nil
}

func directorySize(ctx context.Context, path string) (directorySizeResult, error) {
	return directorySizeIncremental(ctx, path, directoryReadBatch, nil)
}

type directorySizeJob struct {
	path    string
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

type directorySizePublisher func(channel string, value any) error

// directorySizer is one deliberately paced background worker. A single queue
// bounds filesystem concurrency regardless of how many directories are shown.
type directorySizer struct {
	publish   directorySizePublisher
	mu        sync.Mutex
	jobs      map[string]*directorySizeJob
	queue     []*directorySizeJob
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newDirectorySizer(publish directorySizePublisher) *directorySizer {
	sizer := &directorySizer{
		publish: publish,
		jobs:    make(map[string]*directorySizeJob),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go sizer.run()
	return sizer
}

func (s *directorySizer) enqueue(path string) string {
	s.mu.Lock()
	if existing := s.jobs[path]; existing != nil {
		status := "queued"
		if existing.started {
			status = "running"
		}
		s.mu.Unlock()
		return status
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &directorySizeJob{path: path, ctx: ctx, cancel: cancel}
	s.jobs[path] = job
	s.queue = append(s.queue, job)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return "queued"
}

func (s *directorySizer) cancelPath(path string) bool {
	s.mu.Lock()
	job := s.jobs[path]
	if job != nil {
		delete(s.jobs, path)
		job.cancel()
	}
	s.mu.Unlock()
	if job == nil {
		return false
	}
	_ = s.publish(DirectorySizeProgressChannel, directorySizeResult{Path: path, Done: true, Cancelled: true})
	return true
}

func (s *directorySizer) current(job *directorySizeJob) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[job.path] == job
}

func (s *directorySizer) finish(job *directorySizeJob, result directorySizeResult) {
	s.mu.Lock()
	if s.jobs[job.path] != job {
		s.mu.Unlock()
		return
	}
	delete(s.jobs, job.path)
	s.mu.Unlock()
	_ = s.publish(DirectorySizeProgressChannel, result)
}

func (s *directorySizer) next() *directorySizeJob {
	for {
		s.mu.Lock()
		for len(s.queue) > 0 {
			job := s.queue[0]
			s.queue = s.queue[1:]
			if s.jobs[job.path] != job || job.ctx.Err() != nil {
				continue
			}
			job.started = true
			s.mu.Unlock()
			return job
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-s.stop:
			return nil
		}
	}
}

func (s *directorySizer) run() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		job := s.next()
		if job == nil {
			return
		}
		lastProgress := time.Time{}
		result, err := directorySizeIncremental(job.ctx, job.path, directoryReadBatch, func(partial directorySizeResult) {
			now := time.Now()
			if now.Sub(lastProgress) >= directoryProgressInterval && s.current(job) {
				_ = s.publish(DirectorySizeProgressChannel, partial)
				lastProgress = now
			}
			select {
			case <-time.After(directoryWorkerPace):
			case <-job.ctx.Done():
			}
		})
		if err != nil {
			if job.ctx.Err() != nil {
				continue
			}
			result.Error = err.Error()
			result.Complete = false
		}
		result.Done = true
		s.finish(job, result)
	}
}

func (s *directorySizer) close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		s.mu.Lock()
		for _, job := range s.jobs {
			job.cancel()
		}
		s.mu.Unlock()
		<-s.done
	})
}

// du only enqueues work and acknowledges immediately. Progress and completion
// are delivered on DirectorySizeProgressChannel, never as a long-held RPC.
func (p *Plugin) du(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	status := p.sizer.enqueue(path)
	p.reply(frame, map[string]any{"path": path, "status": status})
}

func (p *Plugin) duCancel(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	p.sizer.cancelPath(path)
	p.reply(frame, map[string]any{"path": path, "status": "cancelled"})
}
