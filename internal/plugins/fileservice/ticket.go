// File tickets: the control-plane half of the framework §6.2 by-reference
// data plane. `file:_:ticket` issues a short-lived HMAC-signed ticket binding
// one absolute path; the gateway's `/api/files/raw` HTTP endpoint resolves it
// back through `file:_:ticket:resolve` and streams the bytes itself, so file
// content never crosses the bus as base64 (reply frames above the 1 MiB frame
// cap are dropped by the kernel, surfacing as client-side RPC timeouts).
package fileservice

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

// ticketTTL bounds the replay window; previews re-issue on every load, so a
// few minutes is ample.
const ticketTTL = 10 * time.Minute

var errInvalidTicket = errors.New("invalid or expired ticket")

// ticketIssuer signs and verifies tickets with a random per-process secret.
// Restarting the service invalidates outstanding tickets — acceptable because
// tickets are issued on demand and consumed immediately.
type ticketIssuer struct {
	secret []byte
}

func newTicketIssuer() *ticketIssuer {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("fileservice: ticket secret: " + err.Error())
	}
	return &ticketIssuer{secret: secret}
}

// issue returns "base64url(path).base36(expiry).base64url(hmac)".
func (t *ticketIssuer) issue(path string, now time.Time) (string, time.Time) {
	expiry := now.Add(ticketTTL)
	payload := base64.RawURLEncoding.EncodeToString([]byte(path)) + "." + strconv.FormatInt(expiry.Unix(), 36)
	mac := hmac.New(sha256.New, t.secret)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), expiry
}

func (t *ticketIssuer) verify(ticket string, now time.Time) (string, error) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 {
		return "", errInvalidTicket
	}
	payload := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errInvalidTicket
	}
	mac := hmac.New(sha256.New, t.secret)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", errInvalidTicket
	}
	rawPath, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(rawPath) == 0 {
		return "", errInvalidTicket
	}
	secs, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil || !now.Before(time.Unix(secs, 0)) {
		return "", errInvalidTicket
	}
	return string(rawPath), nil
}

// ticketMIME maps image extensions to explicit MIME types; "" means "let the
// HTTP layer sniff". SVG in particular must be served as image/svg+xml for
// <img> rendering, which content sniffing would get wrong.
var ticketMIMEByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
}

func ticketMIME(path string) string {
	return ticketMIMEByExt[strings.ToLower(filepath.Ext(path))]
}

// statTicketFile rejects directories and missing files uniformly.
func statTicketFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	return info, nil
}

func (p *Plugin) ticket(frame busclient.Frame) {
	// The subscription prefix-matches file:_:ticket:resolve too; leave those
	// frames to the resolve handler.
	if frame.Channel != "file:_:ticket" {
		return
	}
	if pluginrpc.Cancelled(frame) {
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	info, err := statTicketFile(path)
	if err != nil {
		p.replyError(frame, "not_found", "no such file: "+path)
		return
	}
	signed, expiry := p.tickets.issue(path, time.Now())
	p.reply(frame, map[string]any{
		"path":       path,
		"ticket":     signed,
		"url":        "/api/files/raw?ticket=" + url.QueryEscape(signed),
		"expires_at": expiry.Unix(),
		"size":       info.Size(),
		"mime":       ticketMIME(path),
	})
}

func (p *Plugin) ticketResolve(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: ticket")
		return
	}
	signed, ok := value["ticket"].(string)
	if !ok || signed == "" {
		p.replyError(frame, "invalid_request", "missing required field: ticket")
		return
	}
	path, err := p.tickets.verify(signed, time.Now())
	if err != nil {
		p.replyError(frame, "invalid_ticket", errInvalidTicket.Error())
		return
	}
	info, err := statTicketFile(path)
	if err != nil {
		p.replyError(frame, "not_found", "no such file: "+path)
		return
	}
	p.reply(frame, map[string]any{
		"path":  path,
		"size":  info.Size(),
		"mtime": info.ModTime().Unix(),
		"mime":  ticketMIME(path),
	})
}
