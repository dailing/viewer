package fileservice

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const uploadTicketTTL = 2 * time.Minute

type uploadTicketClaims struct {
	Path      string
	Overwrite bool
}

type uploadTicketIssuer struct{ secret []byte }

func newUploadTicketIssuer() *uploadTicketIssuer {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("fileservice: upload ticket secret: " + err.Error())
	}
	return &uploadTicketIssuer{secret: secret}
}

func (t *uploadTicketIssuer) issue(path string, overwrite bool, now time.Time) (string, time.Time) {
	expiry := now.Add(uploadTicketTTL)
	payload := base64.RawURLEncoding.EncodeToString([]byte(path)) + "." + strconv.FormatInt(expiry.Unix(), 36) + "." + strconv.FormatBool(overwrite)
	mac := hmac.New(sha256.New, t.secret)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), expiry
}

func (t *uploadTicketIssuer) verify(ticket string, now time.Time) (uploadTicketClaims, error) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 4 {
		return uploadTicketClaims{}, errInvalidTicket
	}
	payload := strings.Join(parts[:3], ".")
	signature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return uploadTicketClaims{}, errInvalidTicket
	}
	mac := hmac.New(sha256.New, t.secret)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return uploadTicketClaims{}, errInvalidTicket
	}
	path, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(path) == 0 || !filepath.IsAbs(string(path)) {
		return uploadTicketClaims{}, errInvalidTicket
	}
	seconds, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil || !now.Before(time.Unix(seconds, 0)) {
		return uploadTicketClaims{}, errInvalidTicket
	}
	overwrite, err := strconv.ParseBool(parts[2])
	if err != nil {
		return uploadTicketClaims{}, errInvalidTicket
	}
	return uploadTicketClaims{Path: string(path), Overwrite: overwrite}, nil
}

func validUploadName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && filepath.Base(name) == name
}

func prepareUploadTarget(dir, name string, overwrite bool) (string, string, error) {
	if !validUploadName(name) {
		return "", "invalid_request", errors.New("name must be a basename without path separators")
	}
	resolved, ok := resolveRequestPath(dir)
	if !ok {
		return "", "invalid_request", errors.New("invalid directory")
	}
	info, err := os.Stat(resolved)
	if os.IsNotExist(err) {
		return "", "not_found", fmt.Errorf("directory not found: %s", resolved)
	}
	if err != nil {
		return "", "read_error", err
	}
	if !info.IsDir() {
		return "", "not_directory", fmt.Errorf("path is not a directory: %s", resolved)
	}
	target := filepath.Join(resolved, name)
	if info, err = os.Lstat(target); err == nil {
		if !overwrite || info.IsDir() {
			return "", "already_exists", fmt.Errorf("target already exists: %s", target)
		}
	} else if !os.IsNotExist(err) {
		return "", "read_error", err
	}
	return target, "", nil
}

func (p *Plugin) uploadTicket(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required fields: dir, name")
		return
	}
	dir, _ := value["dir"].(string)
	name, _ := value["name"].(string)
	overwrite, _ := value["overwrite"].(bool)
	target, code, err := prepareUploadTarget(dir, name, overwrite)
	if err != nil {
		p.replyError(frame, code, err.Error())
		return
	}
	signed, expiry := p.uploadTickets.issue(target, overwrite, time.Now())
	p.reply(frame, map[string]any{
		"path": target, "ticket": signed, "overwrite": overwrite,
		"url": "/api/files/upload?ticket=" + url.QueryEscape(signed), "expires_at": expiry.Unix(),
	})
}

func (p *Plugin) uploadResolve(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: ticket")
		return
	}
	signed, _ := value["ticket"].(string)
	claims, err := p.uploadTickets.verify(signed, time.Now())
	if err != nil {
		p.replyError(frame, "invalid_ticket", errInvalidTicket.Error())
		return
	}
	p.reply(frame, map[string]any{"path": claims.Path, "overwrite": claims.Overwrite})
}
