package loop

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Loop struct {
	ResumeAfterIteration int    `json:"-"`
	ID                   string `gorm:"primaryKey" json:"id"`
	ChatID               string `gorm:"index" json:"chat_id"`
	RoleID               string `json:"role_id"`
	RoleName             string `json:"role_name"`
	BranchID             string `json:"branch_id"`
	NewBranch            bool   `json:"new_branch"`
	FromTurnID           string `json:"from_turn_id"`
	Goal                 string `json:"goal"`
	Criteria             string `json:"criteria"`
	Root                 string `json:"root"`
	ProgressPath         string `json:"progress_path"`
	State                string `gorm:"index" json:"state"`
	Reason               string `json:"reason"`
	MaxIterations        int    `json:"max_iterations"`
	DurationSeconds      int    `json:"duration_seconds"`
	TurnTimeoutSeconds   int    `json:"turn_timeout_seconds"`
	MinIntervalSeconds   int    `json:"min_interval_seconds"`
	JudgeEvery           int    `json:"judge_every"`
	Deadline             int64  `json:"deadline"`
	Iteration            int    `json:"iteration"`
	Failures             int    `json:"failures"`
	NoProgress           int    `json:"no_progress"`
	Feedback             string `json:"feedback"`
	UserFeedback         string `json:"user_feedback"`
	Revision             int64  `json:"revision"`
	CreatedAt            int64  `gorm:"autoCreateTime:milli" json:"created_at"`
	UpdatedAt            int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}
type Iteration struct {
	GateRevision    int64  `json:"-"`
	ID              string `gorm:"primaryKey" json:"id"`
	LoopID          string `gorm:"uniqueIndex:loop_iteration" json:"loop_id"`
	Number          int    `gorm:"uniqueIndex:loop_iteration" json:"number"`
	State           string `json:"state"`
	Prompt          string `json:"-"`
	DispatchID      string `json:"dispatch_id"`
	TurnID          string `json:"turn_id"`
	StartedAt       int64  `json:"started_at"`
	EndedAt         int64  `json:"ended_at"`
	StopReason      string `json:"stop_reason"`
	Result          string `json:"result"`
	Evidence        string `json:"-"`
	Checkpoint      string `json:"-"`
	CheckpointError string `json:"checkpoint_error"`
	Verdict         string `json:"verdict"`
	Feedback        string `json:"feedback"`
	JudgeAttempts   int    `json:"judge_attempts"`
	CancelReason    string `json:"cancel_reason"`
	CreatedAt       int64  `gorm:"autoCreateTime:milli" json:"created_at"`
}

func openDB(dir string) (*gorm.DB, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", filepath.ToSlash(filepath.Join(dir, "loop.sqlite3")))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	if err = db.AutoMigrate(&Loop{}, &Iteration{}); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}
func now() int64 { return time.Now().UnixMilli() }
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func terminal(s string) bool { return s == "completed" || s == "stopped" || s == "limited" }
