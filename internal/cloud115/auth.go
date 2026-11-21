package cloud115

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxAuthResponseBytes = 64 << 10
	pollAuthTimeout      = 45 * time.Second
)

// DeviceCode is a short-lived PKCE login session. None of its fields may be
// serialized directly into a management response or a persisted configuration.
type DeviceCode struct {
	UID      string `json:"-"`
	Time     int64  `json:"-"`
	Sign     string `json:"-"`
	QrCode   string `json:"-"`
	Verifier string `json:"-"`
}

// Token is kept in process memory by the caller. JSON deliberately exposes none
// of its contents.
type Token struct {
	AccessToken string `json:"-"`
	ExpiresIn   int64  `json:"-"`
}

// AuthClient implements the 115 device-code PKCE flow against fixed official
// origins. It has no cookie jar and never follows redirects with login data.
type AuthClient struct {
	passportURL string
	qrcodeURL   string
	http        *http.Client
	timeout     time.Duration
}

func NewAuthClient(timeout time.Duration) *AuthClient {
	if timeout <= 0 || timeout > time.Minute {
		timeout = 15 * time.Second
	}
	return &AuthClient{
		passportURL: "https://passportapi.115.com",
		qrcodeURL:   "https://qrcodeapi.115.com",
		timeout:     timeout,
		http: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *AuthClient) Begin(ctx context.Context, clientID string) (DeviceCode, error) {
	if !directoryID.MatchString(clientID) || clientID == "0" {
		return DeviceCode{}, errors.New("115 login requires a numeric application ID")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return DeviceCode{}, errors.New("cannot create 115 login verifier")
	}
	verifier := base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(verifier))
	// 115 specifies ordinary padded base64 rather than base64url for this
	// challenge, and the literal method name is sha256.
	form := url.Values{
		"client_id":             {clientID},
		"code_challenge":        {base64.StdEncoding.EncodeToString(digest[:])},
		"code_challenge_method": {"sha256"},
	}
	var data struct {
		UID    string `json:"uid"`
		Time   int64  `json:"time"`
		Sign   string `json:"sign"`
		QrCode string `json:"qrcode"`
	}
	if err := c.request(ctx, c.timeout, "begin login", http.MethodPost, c.passportURL+"/open/authDeviceCode", form, &data); err != nil {
		return DeviceCode{}, err
	}
	if !validLoginText(data.UID, 256) || data.Time <= 0 || !validLoginText(data.Sign, 512) || !validLoginText(data.QrCode, 4096) {
		return DeviceCode{}, errors.New("115 login returned an invalid device code")
	}
	return DeviceCode{UID: data.UID, Time: data.Time, Sign: data.Sign, QrCode: data.QrCode, Verifier: verifier}, nil
}

// Poll returns 0 while waiting, 1 after scanning, 2 after approval, -1 after
// expiry, or -2 when the user cancels. The caller owns polling and session TTL.
func (c *AuthClient) Poll(ctx context.Context, device DeviceCode) (int, error) {
	if !validLoginText(device.UID, 256) || device.Time <= 0 || !validLoginText(device.Sign, 512) {
		return 0, errors.New("invalid 115 login session")
	}
	query := url.Values{"uid": {device.UID}, "time": {strconv.FormatInt(device.Time, 10)}, "sign": {device.Sign}}
	var data map[string]json.RawMessage
	if err := c.request(ctx, pollAuthTimeout, "poll login", http.MethodGet, c.qrcodeURL+"/get/status/", query, &data); err != nil {
		return 0, err
	}
	// An idle 115 long poll returns a successful empty object after about 30s.
	if len(data) == 0 {
		return 0, nil
	}
	var status *int
	if json.Unmarshal(data["status"], &status) != nil || status == nil || *status < -2 || *status > 2 {
		return 0, errors.New("115 login returned an invalid scan status")
	}
	return *status, nil
}

func (c *AuthClient) Exchange(ctx context.Context, device DeviceCode) (Token, error) {
	if !validLoginText(device.UID, 256) || len(device.Verifier) < 43 || len(device.Verifier) > 128 || !validLoginText(device.Verifier, 128) {
		return Token{}, errors.New("invalid 115 login session")
	}
	form := url.Values{"uid": {device.UID}, "code_verifier": {device.Verifier}}
	var data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := c.request(ctx, c.timeout, "complete login", http.MethodPost, c.passportURL+"/open/deviceCodeToToken", form, &data); err != nil {
		return Token{}, err
	}
	if len(data.AccessToken) > 8192 || !bearerToken.MatchString(data.AccessToken) || data.ExpiresIn <= 0 || data.ExpiresIn > int64((365*24*time.Hour)/time.Second) {
		return Token{}, errors.New("115 login returned invalid credentials")
	}
	return Token{AccessToken: data.AccessToken, ExpiresIn: data.ExpiresIn}, nil
}

func validLoginText(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n\t")
}

func (c *AuthClient) request(ctx context.Context, timeout time.Duration, operation, method, address string, values url.Values, target any) error {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if method == http.MethodGet {
		address += "?" + values.Encode()
	} else {
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(requestCtx, method, address, body)
	if err != nil {
		return fmt.Errorf("cannot construct 115 %s request", operation)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Meloport")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("115 %s connection, TLS or timeout failure", operation)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("115 %s HTTP %d", operation, resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot read 115 %s response", operation)
	}
	if len(bodyBytes) > maxAuthResponseBytes {
		return fmt.Errorf("115 %s response exceeds 64 KiB", operation)
	}
	var envelope struct {
		Code  int             `json:"code"`
		Errno int             `json:"errno"`
		Error string          `json:"error"`
		State json.RawMessage `json:"state"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return fmt.Errorf("115 %s response is invalid", operation)
	}
	if envelope.Code != 0 || envelope.Errno != 0 || envelope.Error != "" || string(envelope.State) == "false" || string(envelope.State) == "0" {
		return fmt.Errorf("115 %s rejected request", operation)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, target) != nil {
		return fmt.Errorf("115 %s returned invalid data", operation)
	}
	return nil
}
