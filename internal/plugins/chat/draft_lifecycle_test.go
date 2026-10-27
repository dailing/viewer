package chat

import (
	"strings"
	"testing"
)

func TestDraftLineValidationAndDeleteCleanup(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	now := nowMillis()
	for _, chat := range []*Chat{
		{ID: "chat-draft", Name: "Draft", Root: t.TempDir(), CreatedAt: now, UpdatedAt: now},
		{ID: "chat-other", Name: "Other", Root: t.TempDir(), CreatedAt: now, UpdatedAt: now},
	} {
		if err := p.store.saveChat(chat); err != nil {
			t.Fatal(err)
		}
	}
	branch := &Branch{ID: "draft-branch", ChatID: "chat-draft", Name: "Draft branch", State: branchStateOpen, CreatedAt: now, UpdatedAt: now}
	foreign := &Branch{ID: "foreign-branch", ChatID: "chat-other", Name: "Foreign", State: branchStateOpen, CreatedAt: now, UpdatedAt: now}
	for _, value := range []*Branch{branch, foreign} {
		if err := p.store.createBranch(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.draftLine("chat-draft", ""); err != nil {
		t.Fatalf("mainline rejected: %v", err)
	}
	if err := p.draftLine("chat-draft", branch.ID); err != nil {
		t.Fatalf("active branch rejected: %v", err)
	}
	if err := p.draftLine("chat-draft", foreign.ID); err == nil {
		t.Fatal("cross-chat branch accepted")
	}
	branch.State = branchStateMerged
	branch.ArchivedAt = &now
	if err := p.store.saveBranch(branch); err != nil {
		t.Fatal(err)
	}
	if err := p.draftLine("chat-draft", branch.ID); err == nil {
		t.Fatal("merged branch accepted a draft")
	}
	branch.State = branchStateOpen
	branch.ArchivedAt = nil
	if err := p.store.saveBranch(branch); err != nil {
		t.Fatal(err)
	}
	if err := p.store.saveDraft(&Draft{ChatID: "chat-draft", BranchID: branch.ID, Text: "unsent", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := p.deleteEmptyBranch(branch); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := p.store.db.Model(&Draft{}).Where("chat_id = ? AND branch_id = ?", "chat-draft", branch.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("deleted branch retained %d drafts", count)
	}

	if err := p.store.saveDraft(&Draft{ChatID: "chat-other", BranchID: "", Text: "orphan", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := p.store.deleteChat("chat-other"); err != nil {
		t.Fatal(err)
	}
	if err := p.store.db.Model(&Draft{}).Where("chat_id = ?", "chat-other").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("deleted chat retained %d drafts", count)
	}
}

func TestMergeRejectsUnsentSourceDraft(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	now := nowMillis()
	chat := &Chat{ID: "chat-merge-draft", Name: "Merge draft", Root: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := p.store.saveChat(chat); err != nil {
		t.Fatal(err)
	}
	branch := &Branch{ID: "merge-draft-branch", ChatID: chat.ID, Name: "Source", State: branchStateOpen, CreatedAt: now, UpdatedAt: now}
	if err := p.store.createBranch(branch); err != nil {
		t.Fatal(err)
	}
	if err := p.store.saveDraft(&Draft{ChatID: chat.ID, BranchID: branch.ID, Text: "do not strand me", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	_, err = p.mergeLines(chat.ID, []string{branch.ID}, "", nil, "", false)
	if err == nil || !strings.Contains(err.Error(), "unsent draft") {
		t.Fatalf("merge error = %v", err)
	}
	stored, err := p.store.branch(branch.ID)
	if err != nil || stored == nil || stored.State != branchStateOpen {
		t.Fatalf("rejected merge changed source: %#v, %v", stored, err)
	}
}
