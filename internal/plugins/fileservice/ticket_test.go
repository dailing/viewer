package fileservice

import (
	"strings"
	"testing"
	"time"
)

func TestTicketIssueVerifyRoundtrip(t *testing.T) {
	issuer := newTicketIssuer()
	now := time.Now()
	signed, expiry := issuer.issue("/tmp/example.png", now)
	if expiry.Sub(now) != ticketTTL {
		t.Fatalf("expiry = %v, want %v after now", expiry, ticketTTL)
	}
	path, err := issuer.verify(signed, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify() error = %v", err)
	}
	if path != "/tmp/example.png" {
		t.Fatalf("verify() path = %q, want /tmp/example.png", path)
	}
}

func TestTicketVerifyRejectsTampering(t *testing.T) {
	issuer := newTicketIssuer()
	now := time.Now()
	signed, _ := issuer.issue("/tmp/example.png", now)

	other := newTicketIssuer()
	if _, err := other.verify(signed, now.Add(time.Minute)); err == nil {
		t.Fatal("verify() with a different secret unexpectedly succeeded")
	}

	// Same signature, different payload (swap the encoded path segment).
	parts := strings.Split(signed, ".")
	swapped := parts[0] + "x." + parts[1] + "." + parts[2]
	if _, err := issuer.verify(swapped, now.Add(time.Minute)); err == nil {
		t.Fatal("verify() with tampered payload unexpectedly succeeded")
	}

	for _, malformed := range []string{"", "a", "a.b", "a.b.c.d", "..", "!.b.c"} {
		if _, err := issuer.verify(malformed, now.Add(time.Minute)); err == nil {
			t.Fatalf("verify(%q) unexpectedly succeeded", malformed)
		}
	}
}

func TestTicketVerifyRejectsExpired(t *testing.T) {
	issuer := newTicketIssuer()
	now := time.Now()
	signed, expiry := issuer.issue("/tmp/example.png", now)
	if _, err := issuer.verify(signed, expiry); err == nil {
		t.Fatal("verify() at expiry unexpectedly succeeded")
	}
	if _, err := issuer.verify(signed, expiry.Add(time.Second)); err == nil {
		t.Fatal("verify() past expiry unexpectedly succeeded")
	}
}

func TestTicketMIME(t *testing.T) {
	for path, want := range map[string]string{
		"/a/b.png":  "image/png",
		"/a/B.JPG":  "image/jpeg",
		"/a/b.svg":  "image/svg+xml",
		"/a/b.txt":  "",
		"/a/b":      "",
		"/a/b.webp": "image/webp",
	} {
		if got := ticketMIME(path); got != want {
			t.Fatalf("ticketMIME(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFitsInlineReply(t *testing.T) {
	small := map[string]any{"content": strings.Repeat("a", 1024)}
	if !fitsInlineReply(small) {
		t.Fatal("fitsInlineReply(small) = false, want true")
	}
	// ~1 MiB of base64 pushes the marshalled reply past the frame cap.
	big := map[string]any{"content": strings.Repeat("a", maxInlineReplyBytes)}
	if fitsInlineReply(big) {
		t.Fatal("fitsInlineReply(big) = true, want false")
	}
}
