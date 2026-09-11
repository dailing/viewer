// Package loop implements persistent goal iteration over the chat bus API.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

var Manifest = busclient.Manifest{ID: "viewer.loop", Version: "0.1.0", Slots: map[string]any{
	"loop:_:create": map[string]any{}, "loop:_:list": map[string]any{}, "loop:_:get": map[string]any{},
	"loop:_:recover": map[string]any{}, "loop:_:start": map[string]any{}, "loop:_:pause": map[string]any{}, "loop:_:resume": map[string]any{}, "loop:_:stop": map[string]any{},
}, Emits: map[string]any{"loop:_:changed": map[string]any{}}}

type rpcFunc func(context.Context, string, any, time.Duration) (any, error)
type Plugin struct {
	db         *gorm.DB
	client     *busclient.Client
	call       rpcFunc
	ctx        context.Context
	cancel     context.CancelFunc
	owner      string
	mu         sync.Mutex
	leaseMu    sync.Mutex
	fence      int64
	leaseUntil int64
	judging    map[string]bool
	wg         sync.WaitGroup
	wake       chan struct{}
}

func New(dir string) (*Plugin, error) {
	db, err := openDB(dir)
	if err != nil {
		return nil, err
	}
	return &Plugin{db: db, owner: newID(), judging: map[string]bool{}, wake: make(chan struct{}, 1)}, nil
}
func (p *Plugin) Start(ctx context.Context, ws string, managed bool) error {
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.client = busclient.New(ws, Manifest, busclient.WithManaged(managed))
	p.call = func(ctx context.Context, ch string, v any, timeout time.Duration) (any, error) {
		return p.client.Request(ctx, ch, v, timeout)
	}
	for channel := range Manifest.Slots {
		ch := channel
		if _, err := p.client.Subscribe(ch, func(f busclient.Frame) { go p.handle(ch, f) }); err != nil {
			return err
		}
	}
	if _, err := p.client.Subscribe("chat:_:turn", func(busclient.Frame) { p.signal() }); err != nil {
		return err
	}
	if err := p.client.Connect(ctx); err != nil {
		return err
	}
	p.renewLease()
	p.wg.Add(2)
	go p.leaseWorker()
	go p.worker()
	return nil
}
func (p *Plugin) Close(ctx context.Context) error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	if p.call != nil {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = p.automation(c, "release-lease", nil)
		cancel()
	}
	var err error
	if p.client != nil {
		err = p.client.Close()
	}
	db, e := p.db.DB()
	if e == nil {
		e = db.Close()
	}
	return errors.Join(err, e)
}
func (p *Plugin) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *Plugin) request(ctx context.Context, ch string, v any, out any) error {
	result, err := p.call(ctx, ch, v, 5*time.Second)
	if err != nil {
		return err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
func (p *Plugin) lease() (int64, bool) {
	p.leaseMu.Lock()
	defer p.leaseMu.Unlock()
	return p.fence, p.fence > 0 && p.leaseUntil > now()+1000
}
func (p *Plugin) automation(ctx context.Context, op string, fields map[string]any) (map[string]any, error) {
	fence, ok := p.lease()
	if !ok {
		return nil, errors.New("scheduler ownership unavailable; retry shortly")
	}
	v := map[string]any{"op": op, "owner": p.owner, "fence": fence}
	for k, x := range fields {
		v[k] = x
	}
	var out map[string]any
	err := p.request(ctx, "chat:_:automation", v, &out)
	return out, err
}
func (p *Plugin) gate(op string, l *Loop, rev int64) (gate, error) {
	v, err := p.automation(p.ctx, op, map[string]any{"chat_id": l.ChatID, "branch_id": l.BranchID, "automation_id": l.ID, "revision": rev})
	var g gate
	if err == nil {
		b, _ := json.Marshal(v)
		err = json.Unmarshal(b, &g)
	}
	return g, err
}

type gate struct {
	AutomationID string `json:"automation_id"`
	Paused       bool   `json:"paused"`
	Revision     int64  `json:"revision"`
	Feedback     string `json:"feedback"`
}

func (p *Plugin) renewLease() {
	var l struct {
		Fence     int64 `json:"fence"`
		ExpiresAt int64 `json:"expires_at"`
	}
	err := p.request(p.ctx, "chat:_:automation", map[string]any{"op": "lease", "owner": p.owner}, &l)
	p.leaseMu.Lock()
	defer p.leaseMu.Unlock()
	if err != nil {
		p.fence = 0
		p.leaseUntil = 0
		return
	}
	p.fence = l.Fence
	p.leaseUntil = l.ExpiresAt
}
func (p *Plugin) leaseWorker() {
	defer p.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			p.renewLease()
		}
	}
}
func (p *Plugin) worker() {
	defer p.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
		p.tick()
	}
}
func (p *Plugin) save(l *Loop) error {
	if _, ok := p.lease(); !ok {
		return errors.New("scheduler ownership lost")
	}
	l.UpdatedAt = now()
	l.Revision++
	if err := p.db.Save(l).Error; err != nil {
		return err
	}
	if p.client != nil {
		_ = p.client.Publish(p.ctx, "loop:_:changed", map[string]any{"id": l.ID, "chat_id": l.ChatID, "revision": l.Revision})
	}
	return nil
}
func (p *Plugin) handle(ch string, f busclient.Frame) {
	if pluginrpc.Cancelled(f) {
		return
	}
	v, ok := pluginrpc.Object(f)
	if !ok {
		_ = pluginrpc.RespondError(p.client, f, "bad_request", "object required")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result, err := p.command(ch, v)
	if err != nil {
		_ = pluginrpc.RespondError(p.client, f, "loop_error", err.Error())
	} else {
		_ = pluginrpc.Respond(p.client, f, result)
	}
	p.signal()
}
func (p *Plugin) command(ch string, v map[string]any) (any, error) {
	if ch == "loop:_:list" {
		var rows []Loop
		q := p.db.Omit("criteria", "feedback", "user_feedback").Order("created_at DESC").Limit(20)
		if id, _ := v["chat_id"].(string); id != "" {
			q = q.Where("chat_id = ?", id)
		}
		if before, ok := v["before_created_at"].(float64); ok && before > 0 {
			q = q.Where("created_at < ?", int64(before))
		}
		err := q.Find(&rows).Error
		return map[string]any{"loops": rows}, err
	}
	if ch == "loop:_:create" {
		return p.create(v)
	}
	id, _ := v["id"].(string)
	var l Loop
	if err := p.db.First(&l, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if ch == "loop:_:get" {
		var rows []Iteration
		q := p.db.Omit("prompt", "evidence", "checkpoint", "result").Where("loop_id = ?", id)
		if before, ok := v["before_iteration"].(float64); ok && before > 0 {
			q = q.Where("number < ?", int(before))
		}
		err := q.Order("number DESC").Limit(21).Find(&rows).Error
		more := len(rows) > 20
		if more {
			rows = rows[:20]
		}
		return map[string]any{"loop": l, "iterations": rows, "has_more": more}, err
	}
	if _, ok := p.lease(); !ok {
		return nil, errors.New("scheduler ownership unavailable")
	}
	if terminal(l.State) {
		return nil, errors.New("run has ended; create a new loop")
	}
	switch ch {
	case "loop:_:recover":
		if v["confirmed_stopped"] != true || l.Iteration == 0 {
			return nil, errors.New("confirm the previous task has stopped before resolving recovery")
		}
		var it Iteration
		if err := p.db.First(&it, "id = ?", iterationKey(l.ID, l.Iteration)).Error; err != nil {
			return nil, err
		}
		var st dispatchStatus
		if err := p.status(&it, false, &st); err != nil {
			return nil, err
		}
		if st.Status != "unknown" {
			return nil, errors.New("current iteration is not in unknown recovery state")
		}
		fence, _ := p.lease()
		if err := p.request(p.ctx, "chat:_:dispatch-status", map[string]any{"idempotency_key": it.ID, "op": "resolve", "confirmed_stopped": true, "owner": p.owner, "fence": fence}, nil); err != nil {
			return nil, err
		}
		l.Reason = "已确认旧轮结束，标记为中断"
	case "loop:_:start", "loop:_:resume":
		if l.State != "draft" && l.State != "paused" {
			return nil, errors.New("only draft or paused runs can resume")
		}
		if l.Iteration > 0 {
			var i Iteration
			if err := p.db.First(&i, "id = ?", iterationKey(l.ID, l.Iteration)).Error; err != nil {
				return nil, err
			}
			if i.State != "done" {
				var st dispatchStatus
				if err := p.status(&i, false, &st); err != nil {
					return nil, err
				}
				if st.Status == "unknown" {
					return nil, errors.New("previous turn execution is unknown; verify it has ended before continuing")
				}
			}
		}
		if extra, ok := v["extend_seconds"].(float64); ok && extra > 0 && extra <= 86400 {
			if l.Deadline < now() {
				l.Deadline = now()
			}
			l.Deadline += int64(extra * 1000)
		}
		if l.Deadline == 0 {
			l.Deadline = now() + int64(l.DurationSeconds)*1000
		}
		if l.Deadline <= now() {
			return nil, errors.New("deadline expired; explicitly extend the limit to resume")
		}

		if err := p.provision(&l); err != nil {
			return nil, err
		}
		g, err := p.gate("get", &l, 0)
		if err != nil {
			return nil, err
		}
		// User feedback becomes part of the immutable next-iteration prompt.
		l.UserFeedback = g.Feedback
		if _, err = p.gate("resume", &l, g.Revision); err != nil {
			return nil, err
		}
		l.ResumeAfterIteration = l.Iteration
		l.State = "running"
		l.Reason = ""
		l.Failures = 0
		l.NoProgress = 0
		if err := p.db.Model(&Iteration{}).Where("loop_id = ? AND state IN ?", l.ID, []string{"ready", "judging"}).Updates(map[string]any{"judge_attempts": 0, "state": "ready"}).Error; err != nil {
			return nil, err
		}
	case "loop:_:pause":
		if l.State == "draft" {
			l.State = "paused"
		} else {
			if _, err := p.gate("pause", &l, 0); err != nil {
				return nil, err
			}
			l.State = "paused"
		}
		l.Reason = "用户暂停；当前轮结束后不再续轮"
	case "loop:_:stop":
		if l.State != "draft" {
			if _, err := p.gate("pause", &l, 0); err != nil {
				return nil, err
			}
		}
		l.State = "stopping"
		l.Reason = "用户停止"
	default:
		return nil, errors.New("unknown loop operation")
	}
	return l, p.save(&l)
}
func (p *Plugin) create(v map[string]any) (any, error) {
	var l Loop
	b, _ := json.Marshal(v)
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	// Whitelist creation fields: clients cannot forge state, paths or counters.
	l = Loop{ID: l.ID, ChatID: l.ChatID, RoleID: l.RoleID, BranchID: l.BranchID, NewBranch: l.NewBranch, FromTurnID: l.FromTurnID, Goal: strings.TrimSpace(l.Goal), Criteria: strings.TrimSpace(l.Criteria), MaxIterations: l.MaxIterations, DurationSeconds: l.DurationSeconds, TurnTimeoutSeconds: l.TurnTimeoutSeconds, JudgeEvery: l.JudgeEvery, MinIntervalSeconds: l.MinIntervalSeconds}
	if l.ID == "" {
		l.ID = newID()
	}
	if len(l.ID) > 64 || strings.ContainsAny(l.ID, "/\\.\x00") {
		return nil, errors.New("invalid id")
	}
	var existing Loop
	if err := p.db.First(&existing, "id = ?", l.ID).Error; err == nil {
		if existing.ChatID != l.ChatID || existing.RoleID != l.RoleID || existing.Goal != l.Goal || existing.Criteria != l.Criteria {
			return nil, errors.New("loop id reused with different task; create a new loop")
		}
		if existing.State == "draft" {
			if err := p.provision(&existing); err != nil {
				return existing, err
			}
		}
		return existing, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if l.Goal == "" || l.Criteria == "" || l.ChatID == "" || l.RoleID == "" {
		return nil, errors.New("goal, criteria, chat_id and explicit role_id are required")
	}
	if len(l.Goal) > 8192 || len(l.Criteria) > 8192 {
		return nil, errors.New("goal and criteria must each fit within 8 KiB")
	}
	if l.MaxIterations == 0 {
		l.MaxIterations = 20
	}
	if l.DurationSeconds == 0 {
		l.DurationSeconds = 3600
	}
	if l.TurnTimeoutSeconds == 0 {
		l.TurnTimeoutSeconds = 900
	}
	if l.JudgeEvery == 0 {
		l.JudgeEvery = 3
	}
	if l.MaxIterations < 1 || l.MaxIterations > 1000 || l.DurationSeconds < 1 || l.DurationSeconds > 86400 || l.TurnTimeoutSeconds < 1 || l.TurnTimeoutSeconds > 86400 || l.JudgeEvery < 1 || l.JudgeEvery > 100 || l.MinIntervalSeconds < 0 || l.MinIntervalSeconds > 86400 {
		return nil, errors.New("invalid iteration, time or interval limit")
	}
	if _, specified := v["new_branch"]; !specified {
		l.NewBranch = true
	}
	fields := map[string]any{"chat_id": l.ChatID, "role_id": l.RoleID}
	if !l.NewBranch {
		fields["branch_id"] = l.BranchID
	}
	info, err := p.automation(p.ctx, "describe", fields)
	if err != nil {
		return nil, err
	}
	l.Root, _ = info["root"].(string)
	l.RoleName, _ = info["role_name"].(string)
	l.ProgressPath = filepath.Join(l.Root, ".loop", l.ID, "progress.md")
	l.State = "draft"
	l.CreatedAt = now()
	l.UpdatedAt = l.CreatedAt
	if l.NewBranch {
		l.BranchID = ""
	}
	if err := p.db.Create(&l).Error; err != nil {
		return nil, err
	}
	if err := p.provision(&l); err != nil {
		return l, err
	}
	return l, p.save(&l)
}
func (p *Plugin) provision(l *Loop) error {
	if l.NewBranch && l.BranchID == "" {
		var b struct {
			ID string `json:"id"`
		}
		if err := p.request(p.ctx, "chat:_:branches:create", map[string]any{"chat_id": l.ChatID, "name": "loop: " + clip(l.Goal, 60), "from_turn_id": l.FromTurnID, "idempotency_key": "loop/" + l.ID}, &b); err != nil {
			return err
		}
		l.BranchID = b.ID
		if err := p.save(l); err != nil {
			return err
		}
	}
	if _, err := p.automation(p.ctx, "describe", map[string]any{"chat_id": l.ChatID, "role_id": l.RoleID, "branch_id": l.BranchID}); err != nil {
		return err
	}
	_, err := p.gate("claim", l, 0)
	return err
}
func iterationKey(id string, n int) string { return fmt.Sprintf("loop/%s/iteration/%d", id, n) }
func (p *Plugin) logError(id string, err error) {
	slog.Warn("loop scheduler", "loop_id", id, "error", err)
}
