package fileservice

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUploadTicketRoundtripAndSeparation(t *testing.T) {
	issuer := newUploadTicketIssuer()
	now := time.Now()
	signed, expiry := issuer.issue("/tmp/upload.bin", true, now)
	claims, err := issuer.verify(signed, now.Add(time.Minute))
	if err != nil || claims.Path != "/tmp/upload.bin" || !claims.Overwrite {
		t.Fatalf("claims = %+v, err = %v", claims, err)
	}
	if _, err := issuer.verify(signed, expiry); err == nil {
		t.Fatal("expired upload ticket accepted")
	}
	if _, err := newTicketIssuer().verify(signed, now); err == nil {
		t.Fatal("upload ticket accepted as a read ticket")
	}
	readTicket, _ := newTicketIssuer().issue("/tmp/upload.bin", now)
	if _, err := issuer.verify(readTicket, now); err == nil {
		t.Fatal("read ticket accepted as an upload ticket")
	}
	tampered := signed[:len(signed)-1] + "x"
	if _, err := issuer.verify(tampered, now); err == nil {
		t.Fatal("tampered upload ticket accepted")
	}
}

func TestUploadNameValidation(t *testing.T) {
	for _, valid := range []string{"a.txt", "中文 名.md", ".hidden"} {
		if !validUploadName(valid) {
			t.Errorf("valid name %q rejected", valid)
		}
	}
	for _, invalid := range []string{"", ".", "..", "a/b", `a\\b`, "/tmp/a", "a\x00b"} {
		if validUploadName(invalid) {
			t.Errorf("invalid name %q accepted", invalid)
		}
	}
}

func TestPrepareUploadTargetCollisionAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code, err := prepareUploadTarget(dir, "a.txt", false); err == nil || code != "already_exists" {
		t.Fatalf("collision code = %q, err = %v", code, err)
	}
	if got, _, err := prepareUploadTarget(dir, "a.txt", true); err != nil || got != target {
		t.Fatalf("overwrite target = %q, err = %v", got, err)
	}
	if _, code, err := prepareUploadTarget(dir, "missing/a.txt", false); err == nil || code != "invalid_request" {
		t.Fatalf("path-like name code = %q, err = %v", code, err)
	}
	if _, code, err := prepareUploadTarget(target, "a.txt", false); err == nil || code != "not_directory" {
		t.Fatalf("file directory code = %q, err = %v", code, err)
	}
}
