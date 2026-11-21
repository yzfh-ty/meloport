package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"meloport/internal/cloud115"
)

const cloudLoginTTL = 5 * time.Minute

type cloudAuthorizer interface {
	Begin(context.Context, string) (cloud115.DeviceCode, error)
	Poll(context.Context, cloud115.DeviceCode) (int, error)
	Exchange(context.Context, cloud115.DeviceCode) (cloud115.Token, error)
}

// cloudLogin holds at most one pending scan. The device data never leaves the
// process, apart from its scan target encoded in the requested PNG.
type cloudLogin struct {
	mu      sync.Mutex
	auth    cloudAuthorizer
	client  func(string) (cloudDriveClient, error)
	session *cloudLoginSession
}

type cloudLoginSession struct {
	id, status, failure string
	device              cloud115.DeviceCode
	png                 []byte
	expires, nextPoll   time.Time
	cancel              context.CancelFunc
}

type cloudLoginView struct {
	SessionID string    `json:"session_id"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	QRCode    string    `json:"qrcode,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func (s *cloudLoginSession) view() cloudLoginView {
	result := cloudLoginView{SessionID: s.id, Status: s.status, ExpiresAt: s.expires, Error: s.failure}
	if s.status == "waiting" || s.status == "scanned" {
		result.QRCode = "/api/cloud115/login/qr?session_id=" + s.id
	}
	return result
}

func (s *cloudLoginSession) finish(status string) {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.status, s.device, s.png = status, cloud115.DeviceCode{}, nil
}

// sessionFor checks the opaque ID with the login mutex held. Access tokens are
// handled by cloudDrive and have a separate expiry from this scan session.
func (l *cloudLogin) sessionFor(id string) *cloudLoginSession {
	s := l.session
	if s == nil || id != s.id {
		return nil
	}
	if (s.status == "waiting" || s.status == "scanned") && !time.Now().Before(s.expires) {
		s.finish("expired")
	}
	return s
}

func (c *webController) cloudLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !c.cloudReady(w) {
		return
	}
	l := c.cloud.login
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		webError(w, http.StatusInternalServerError, "cannot create 115 login session")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	s := &cloudLoginSession{id: hex.EncodeToString(nonce[:]), status: "starting", cancel: cancel}
	l.mu.Lock()
	if l.session != nil {
		l.session.finish("cancelled")
	}
	l.session = s
	l.mu.Unlock()
	device, err := l.auth.Begin(ctx, c.cloudBoot.ClientID)
	var png []byte
	if err == nil {
		png, err = qrcode.Encode(device.QrCode, qrcode.Medium, 256)
		if err != nil {
			// Encoder diagnostics may contain parts of the device code.
			err = errors.New("115 returned a QR code that cannot be displayed")
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session != s || s.status != "starting" {
		webError(w, http.StatusConflict, "115 login session was replaced or cancelled")
		return
	}
	if err != nil || ctx.Err() != nil {
		s.finish("error")
		if err == nil {
			err = ctx.Err()
		}
		writeCloudError(w, err)
		return
	}
	s.status, s.device, s.png, s.cancel = "waiting", device, png, nil
	s.expires = time.Now().Add(cloudLoginTTL).UTC()
	webJSON(w, http.StatusOK, s.view())
}

func (c *webController) cloudLoginQR(w http.ResponseWriter, r *http.Request) {
	if !c.cloudReady(w) {
		return
	}
	l := c.cloud.login
	l.mu.Lock()
	s := l.sessionFor(r.URL.Query().Get("session_id"))
	var png []byte
	if s != nil {
		png = s.png
	}
	l.mu.Unlock()
	if len(png) == 0 {
		webError(w, http.StatusNotFound, "115 login QR code is unavailable")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	_, _ = w.Write(png)
}

func (c *webController) cloudLoginStatus(w http.ResponseWriter, r *http.Request) {
	if !c.cloudReady(w) {
		return
	}
	l := c.cloud.login
	l.mu.Lock()
	s := l.sessionFor(r.URL.Query().Get("session_id"))
	if s == nil {
		l.mu.Unlock()
		webError(w, http.StatusNotFound, "115 login session was not found")
		return
	}
	if (s.status != "waiting" && s.status != "scanned") || s.cancel != nil || time.Now().Before(s.nextPoll) {
		view := s.view()
		l.mu.Unlock()
		webJSON(w, http.StatusOK, view)
		return
	}
	deadline := s.expires
	if max := time.Now().Add(90 * time.Second); max.Before(deadline) {
		deadline = max
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	device := s.device
	s.cancel, s.nextPoll = cancel, time.Now().Add(2*time.Second)
	l.mu.Unlock()
	status, err := l.auth.Poll(ctx, device)
	var client cloudDriveClient
	var account *cloudAccountView
	var expires time.Time
	if err == nil && status == 2 {
		client, account, expires, err = c.completeCloudLogin(ctx, device)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session != s || (s.status != "waiting" && s.status != "scanned") {
		webJSON(w, http.StatusOK, s.view())
		return
	}
	s.cancel = nil
	if !time.Now().Before(s.expires) {
		s.finish("expired")
	} else if err != nil {
		if status != 2 {
			writeCloudError(w, err)
			return
		}
		s.finish("error")
		s.failure = err.Error()
	} else {
		switch status {
		case 1:
			s.status = "scanned"
		case 2:
			if ctx.Err() != nil {
				s.finish("error")
				s.failure = "115 authorization confirmation timed out; scan a new code"
			} else {
				c.cloud.drive.setSession(client, expires, account)
				s.finish("authorized")
			}
		case -1:
			s.finish("expired")
		case -2:
			s.finish("cancelled")
		}
	}
	webJSON(w, http.StatusOK, s.view())
}

func (c *webController) completeCloudLogin(ctx context.Context, device cloud115.DeviceCode) (cloudDriveClient, *cloudAccountView, time.Time, error) {
	l := c.cloud.login
	token, err := l.auth.Exchange(ctx, device)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	expires := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	client, err := l.client(token.AccessToken)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	account, _, err := verifyCloudAccess(ctx, client, c.cloudBoot.TargetCID)
	if err != nil {
		err = errors.New("115 login succeeded, but this account cannot read the configured target directory")
		return nil, nil, time.Time{}, err
	}
	return client, account, expires, nil
}

func (c *webController) cloudLoginCancel(w http.ResponseWriter, r *http.Request) {
	if !c.cloudReady(w) {
		return
	}
	l := c.cloud.login
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessionFor(r.URL.Query().Get("session_id"))
	if s != nil && (s.status == "waiting" || s.status == "scanned") {
		s.finish("cancelled")
	}
	webJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

func (c *webController) cloudLogout(w http.ResponseWriter, r *http.Request) {
	if !c.cloudReady(w) {
		return
	}
	l := c.cloud.login
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session != nil {
		l.session.finish("cancelled")
		l.session = nil
	}
	c.cloud.drive.setSession(nil, time.Time{}, nil)
	webJSON(w, http.StatusOK, map[string]bool{"signed_out": true})
}
