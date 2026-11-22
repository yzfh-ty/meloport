package app

import (
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meloport/internal/cloud115"
	"meloport/internal/config"
	"meloport/internal/state"
)

func putFixtureCloudSettings(t *testing.T, f *webFixture, input config.Cloud115Settings) {
	t.Helper()
	reply := webRequest(t, f, http.MethodPut, "/api/cloud115/settings", input)
	webStatus(t, reply, http.StatusOK)
	if !strings.Contains(reply.Body.String(), `"restart_required":true`) {
		t.Fatal("the response does not distinguish saved settings from the running listener")
	}
}

func TestCloudWebSettingsRequireRestartAndProtectLiveStaging(t *testing.T) {
	f := newWebFixture(t)
	path := "/api/cloud115"
	webStatus(t, webRequest(t, f, http.MethodGet, path, nil, func(r *http.Request) {
		r.Header.Del("X-Meloport-Request")
	}), http.StatusForbidden)
	webStatus(t, webRequest(t, f, http.MethodPost, path+"/login", nil), http.StatusConflict)

	settings := f.cfg.Cloud115.Settings()
	settings.Enabled, settings.TargetCID = true, "12345"
	putFixtureCloudSettings(t, f, settings)
	var overview cloudOverviewView
	resp := webRequest(t, f, http.MethodGet, path, nil)
	webStatus(t, resp, http.StatusOK)
	if err := json.Unmarshal(resp.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.RuntimeEnabled || !overview.RestartRequired || overview.Settings.TargetCID != "12345" || overview.Settings.StagingDir != settings.StagingDir {
		t.Fatalf("incorrect saved settings or runtime state: %+v", overview)
	}
	webStatus(t, webRequest(t, f, http.MethodPut, path+"/settings", `{"access_token":"private-fixture"}`), http.StatusBadRequest)
	text, err := os.ReadFile(f.path)
	if err != nil || strings.Contains(string(text), "private-fixture") {
		t.Fatal("the settings editor leaked a submitted private credential")
	}
	// Model a restart with enabled staging, a saved disable, then another live
	// settings edit that would put local audio into the still-watched tree.
	startCloudWebFixture(t, f)
	settings.Enabled = false
	putFixtureCloudSettings(t, f, settings)
	edit := config.SettingsInput{
		Timezone: f.cfg.Timezone, Schedule: f.cfg.Schedule, Quality: f.cfg.Quality,
		MusicDir: &settings.StagingDir,
	}
	webStatus(t, webRequest(t, f, http.MethodPut, "/api/settings", edit), http.StatusBadRequest)
}

func startCloudWebFixture(t *testing.T, f *webFixture) {
	t.Helper()
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.control.cloudBoot = cfg.Cloud115
	f.control.cloud = newCloudRuntime(cfg.Cloud115, nil, f.store)
	f.control.editor.ProtectActiveCloud(cfg.Cloud115)
}

func TestCloudWebMediaSwitchesPreserveLocalDownloadsAndRequireRestart(t *testing.T) {
	f := newWebFixture(t)
	readOverview := func() cloudOverviewView {
		t.Helper()
		response := webRequest(t, f, http.MethodGet, "/api/cloud115", nil)
		webStatus(t, response, http.StatusOK)
		var view cloudOverviewView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	initial := readOverview()
	if initial.Settings.Enabled || initial.Settings.MediaEnabled || initial.Settings.NavidromeEnabled || initial.Settings.NavidromePlaylistSync ||
		initial.RuntimeEnabled || initial.MediaRuntimeEnabled || initial.NavidromeRuntimeEnabled {
		t.Fatal("all optional integrations must start disabled")
	}
	settings := initial.Settings
	settings.MediaEnabled, settings.NavidromeEnabled = true, true
	settings.MediaListen, settings.MediaBaseURL = "127.0.0.1:18081", "http://127.0.0.1:18081"
	settings.NavidromeURL, settings.NavidromeMountRoot = "http://127.0.0.1:4533", "/music"
	putFixtureCloudSettings(t, f, settings)
	saved := readOverview()
	if saved.RuntimeEnabled || saved.MediaRuntimeEnabled || saved.NavidromeRuntimeEnabled || !saved.RestartRequired || saved.Settings.MediaListen != settings.MediaListen {
		t.Fatal("saving a media address must not start its listener before restart")
	}
	startCloudWebFixture(t, f)
	started := readOverview()
	if started.RuntimeEnabled || !started.MediaRuntimeEnabled || !started.NavidromeRuntimeEnabled || started.RestartRequired {
		t.Fatal("standalone Navidrome runtime incorrectly enables 115 handoff")
	}
	webStatus(t, webRequest(t, f, http.MethodPost, "/api/cloud115/login", nil), http.StatusConflict)
	settings.MediaListen = "127.0.0.1:18082"
	putFixtureCloudSettings(t, f, settings)
	settings.MediaEnabled, settings.NavidromeEnabled = false, false
	putFixtureCloudSettings(t, f, settings)
	startCloudWebFixture(t, f)
	disabled := readOverview()
	if disabled.RuntimeEnabled || disabled.MediaRuntimeEnabled || disabled.NavidromeRuntimeEnabled || disabled.RestartRequired {
		t.Fatal("disabled integration still appears active after restart")
	}
	cfg, err := config.Load(f.path)
	if err != nil || cfg.Storage.MusicDir != f.cfg.Storage.MusicDir || cfg.Cloud115.NavidromeURL != settings.NavidromeURL {
		t.Fatalf("integration settings changed local downloads or lost saved endpoints: %v", err)
	}
}

type webCloudQRFixture struct {
	status int
}

const (
	fixtureQR         = "https://115.com/fixture/unique-qr-content"
	fixtureVerifier   = "unique-fixture-verifier"
	fixtureCloudToken = "unique-fixture-access-token"
)

func (f *webCloudQRFixture) Begin(context.Context, string) (cloud115.DeviceCode, error) {
	return cloud115.DeviceCode{UID: "fixture-uid", QrCode: fixtureQR, Verifier: fixtureVerifier}, nil
}

func (f *webCloudQRFixture) Poll(context.Context, cloud115.DeviceCode) (int, error) {
	return f.status, nil
}

func (f *webCloudQRFixture) Exchange(context.Context, cloud115.DeviceCode) (cloud115.Token, error) {
	return cloud115.Token{AccessToken: fixtureCloudToken, ExpiresIn: 60}, nil
}

type webCloudDriveFixture struct {
	t *testing.T
}

func (f *webCloudDriveFixture) UserInfo(context.Context) (cloud115.User, error) {
	return cloud115.User{ID: "331", Name: "Fixture account"}, nil
}

func (f *webCloudDriveFixture) List(_ context.Context, cid string) ([]cloud115.File, error) {
	if cid != "12345" {
		f.t.Errorf("unexpected 115 directory %q", cid)
	}
	return []cloud115.File{}, nil
}

func (f *webCloudDriveFixture) DownURL(context.Context, string, string) (string, error) {
	f.t.Error("unexpected direct link resolution")
	return "", nil
}

func TestCloudWebLoginReconcileAndLogout(t *testing.T) {
	f := newWebFixture(t)
	settings := f.cfg.Cloud115.Settings()
	settings.Enabled, settings.TargetCID = true, "12345"
	putFixtureCloudSettings(t, f, settings)
	startCloudWebFixture(t, f)
	cloud := f.control.cloud
	cloud.login.auth = &webCloudQRFixture{status: 2}
	cloud.login.client = func(token string) (cloudDriveClient, error) {
		if token != fixtureCloudToken {
			t.Error("QR login did not set its session access token")
		}
		return &webCloudDriveFixture{t}, nil
	}
	if ready, err := cloud.Ready(t.Context(), "daily", "netease", "1"); ready || err != errCloudLoginRequired {
		t.Fatal("unsigned runtime should hold new cloud files before transfer")
	}
	path := "/api/cloud115"
	webStatus(t, webRequest(t, f, http.MethodPost, path+"/check", nil), http.StatusConflict)
	login := webRequest(t, f, http.MethodPost, path+"/login", nil)
	webStatus(t, login, http.StatusOK)
	var session cloudLoginView
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil || session.Status != "waiting" || session.SessionID == "" || session.QRCode == "" {
		t.Fatalf("invalid initial QR state: %+v, %v", session, err)
	}
	qr := webRequest(t, f, http.MethodGet, session.QRCode, nil)
	webStatus(t, qr, http.StatusOK)
	if len(qr.Body.Bytes()) < 26 || (qr.Body.Bytes()[25] != 2 && qr.Body.Bytes()[25] != 6) {
		t.Fatalf("QR response must be a browser-compatible true-colour PNG; colour type = %d", qr.Body.Bytes()[25])
	}
	if _, err := png.Decode(qr.Body); err != nil {
		t.Fatal(err)
	}
	status := webRequest(t, f, http.MethodGet, path+"/login?session_id="+session.SessionID, nil)
	webStatus(t, status, http.StatusOK)
	if !strings.Contains(status.Body.String(), `"status":"authorized"`) {
		t.Fatalf("unexpected QR authorization result: %s", status.Body)
	}
	for _, body := range []string{login.Body.String(), status.Body.String()} {
		for _, secret := range []string{fixtureCloudToken, fixtureVerifier, fixtureQR} {
			if strings.Contains(body, secret) {
				t.Fatal("browser JSON exposed login credentials")
			}
		}
	}
	webStatus(t, webRequest(t, f, http.MethodGet, session.QRCode, nil), http.StatusNotFound)
	webStatus(t, webRequest(t, f, http.MethodPost, path+"/check", nil), http.StatusOK)

	const virtualID = "fixture-media-id-do-not-serialize"
	const privatePickCode = "fixture-pick-code-do-not-serialize"
	file, err := f.store.SaveCloudIntent(t.Context(), state.CloudFile{
		RootCID: "12345", TaskID: "daily", Provider: "netease", TrackID: "1",
		LocalPath: "fake-work", StagingPath: "fake-stage", RelativePath: "daily/曲名.flac",
		Size: 128, MediaID: virtualID, SHA1: strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	file.CID, file.ID, file.PickCode = "221", "333", privatePickCode
	if err := f.store.ConfirmCloudFile(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	readStatus := func(want string) {
		t.Helper()
		response := webRequest(t, f, http.MethodGet, path, nil)
		webStatus(t, response, http.StatusOK)
		if strings.Contains(response.Body.String(), virtualID) || strings.Contains(response.Body.String(), privatePickCode) {
			t.Fatal("cloud status leaked playback or upstream identifiers")
		}
		var view cloudOverviewView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || len(view.Files) != 1 || view.Files[0].Status != want {
			t.Fatalf("unexpected export state: %+v, %v", view, err)
		}
	}
	readStatus("confirmed")
	webStatus(t, webRequest(t, f, http.MethodPost, path+"/reconcile", nil), http.StatusOK)
	readStatus("exported")
	exportPath := filepath.Join(f.control.cloudBoot.STRMDir, "daily", "曲名.flac.strm")
	if body, err := os.ReadFile(exportPath); err != nil || string(body) != settings.MediaBaseURL+"/media/"+virtualID+"\n" {
		t.Fatal("reconcile did not generate the expected local STRM")
	}
	webStatus(t, webRequest(t, f, http.MethodDelete, path+"/session", nil), http.StatusOK)
	webStatus(t, webRequest(t, f, http.MethodPost, path+"/check", nil), http.StatusConflict)
}
