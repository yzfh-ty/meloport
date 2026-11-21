package cloud115

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func authForTest(t *testing.T, handler http.HandlerFunc) *AuthClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewAuthClient(5 * time.Second)
	client.passportURL, client.qrcodeURL = server.URL, server.URL
	return client
}

func TestDeviceLoginPKCEAndRefresh(t *testing.T) {
	var challenge atomic.Value
	var status atomic.Int32
	client := authForTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept") != "application/json" {
			t.Error("login request leaked unrelated credentials or lacks Accept")
		}
		if r.Method == http.MethodPost && (r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil) {
			t.Error("login credentials must be in a POST form")
		}
		switch r.URL.Path {
		case "/open/authDeviceCode":
			if r.Method != http.MethodPost || r.Form.Get("client_id") != "100197303" || r.Form.Get("code_challenge_method") != "sha256" || len(r.Form) != 3 {
				t.Error("incorrect device-code request")
			}
			challenge.Store(r.Form.Get("code_challenge"))
			io.WriteString(w, `{"state":1,"code":0,"data":{"uid":"device-id","time":1770000000,"sign":"device-sign","qrcode":"https://115.com/scan/device-id"}}`)
		case "/get/status/":
			if r.Method != http.MethodGet || r.URL.Query().Get("uid") != "device-id" || r.URL.Query().Get("sign") != "device-sign" || r.URL.Query().Get("time") != "1770000000" || len(r.URL.Query()) != 3 {
				t.Error("incorrect poll request")
			}
			fmt.Fprintf(w, `{"state":1,"code":0,"data":{"status":%d}}`, status.Load())
		case "/open/deviceCodeToToken":
			verifier := r.Form.Get("code_verifier")
			digest := sha256.Sum256([]byte(verifier))
			if r.Form.Get("uid") != "device-id" || len(r.Form) != 2 || len(verifier) != 43 || base64.StdEncoding.EncodeToString(digest[:]) != challenge.Load() {
				t.Error("exchange did not prove the PKCE verifier")
			}
			io.WriteString(w, `{"state":1,"code":0,"data":{"access_token":"test-access","refresh_token":"test-refresh","expires_in":7200}}`)
		default:
			t.Error("unexpected login endpoint")
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	device, err := client.Begin(ctx, "100197303")
	if err != nil || device.UID != "device-id" || device.QrCode != "https://115.com/scan/device-id" || device.Verifier == "" {
		t.Fatalf("begin login failed: %v", err)
	}
	for _, want := range []int{0, 1, 2, -1, -2} {
		status.Store(int32(want))
		got, err := client.Poll(ctx, device)
		if err != nil || got != want {
			t.Fatalf("poll status %d = %d, %v", want, got, err)
		}
	}
	token, err := client.Exchange(ctx, device)
	if err != nil || token.AccessToken != "test-access" || token.ExpiresIn != 7200 {
		t.Fatalf("exchange failed: %v", err)
	}
	for _, value := range []any{device, token} {
		data, err := json.Marshal(value)
		if err != nil || string(data) != "{}" {
			t.Fatal("login secrets must not serialize as JSON")
		}
	}
}

func TestDeviceLoginDoesNotFollowRedirectsOrExposeFailures(t *testing.T) {
	const secret = "private-device-and-token"
	var redirected atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	t.Cleanup(other.Close)
	for _, tc := range []struct {
		name string
		send func(http.ResponseWriter)
	}{
		{"redirect", func(w http.ResponseWriter) { w.Header().Set("Location", other.URL+"/"+secret); w.WriteHeader(307) }},
		{"upstream error", func(w http.ResponseWriter) { fmt.Fprintf(w, `{"code":401,"message":%q,"error":%q}`, secret, secret) }},
		{"malformed", func(w http.ResponseWriter) { io.WriteString(w, secret) }},
		{"oversize", func(w http.ResponseWriter) {
			io.WriteString(w, strings.Repeat(secret, maxAuthResponseBytes/len(secret)+1))
		}},
		{"unknown status", func(w http.ResponseWriter) { io.WriteString(w, `{"state":1,"data":{"status":3}}`) }},
		{"invalid status type", func(w http.ResponseWriter) { io.WriteString(w, `{"state":1,"data":{"status":"waiting"}}`) }},
		{"invalid data type", func(w http.ResponseWriter) { io.WriteString(w, `{"state":1,"data":[]}`) }},
		{"failed state", func(w http.ResponseWriter) { io.WriteString(w, `{"state":false,"data":{"status":0}}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := authForTest(t, func(w http.ResponseWriter, r *http.Request) { tc.send(w) })
			_, err := client.Poll(context.Background(), DeviceCode{UID: secret, Time: 1770000000, Sign: secret})
			if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), other.URL) {
				t.Fatal("login failure should be sanitized and explicit")
			}
		})
	}
	if redirected.Load() {
		t.Fatal("login client followed an upstream redirect")
	}
}

func TestIdleDeviceLoginPollIsWaiting(t *testing.T) {
	client := authForTest(t, func(w http.ResponseWriter, _ *http.Request) {
		// This is the safe shape observed from the live endpoint after a 30s
		// idle long poll: a successful response whose data object has no fields.
		io.WriteString(w, `{"state":1,"code":0,"errno":0,"data":{}}`)
	})
	status, err := client.Poll(context.Background(), DeviceCode{UID: "device-id", Time: 1770000000, Sign: "device-sign"})
	if err != nil || status != 0 {
		t.Fatalf("idle successful poll = %d, %v; want waiting", status, err)
	}
}

func TestDeviceLoginRejectsInvalidInputAndCredentials(t *testing.T) {
	var requests atomic.Int32
	client := authForTest(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, `{"state":1,"data":{"access_token":"bad\naccess","expires_in":7200}}`)
	})
	for _, id := range []string{"", "0", "not-numeric", "100197303\n"} {
		if _, err := client.Begin(context.Background(), id); err == nil {
			t.Error("accepted invalid app ID")
		}
	}
	if _, err := client.Poll(context.Background(), DeviceCode{}); err == nil {
		t.Error("accepted invalid poll session")
	}
	if _, err := client.Exchange(context.Background(), DeviceCode{}); err == nil {
		t.Error("accepted invalid exchange session")
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input reached an upstream")
	}
	if _, err := client.Exchange(context.Background(), DeviceCode{UID: "device", Verifier: strings.Repeat("a", 43)}); err == nil {
		t.Error("accepted invalid token response")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Begin(ctx, "100197303"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
