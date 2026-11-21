package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meloport/internal/cloud115"
	"meloport/internal/config"
)

const cloudFileViewLimit = 100

type cloudStats struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Confirmed int `json:"confirmed"`
	Exported  int `json:"exported"`
}

type cloudFileView struct {
	TaskID       string `json:"task_id"`
	Platform     string `json:"platform"`
	TrackID      string `json:"track_id"`
	RelativePath string `json:"relative_path"`
	Size         int64  `json:"size"`
	Status       string `json:"status"`
	STRMPath     string `json:"strm_path,omitempty"`
}

type cloudOverviewView struct {
	Settings             config.Cloud115Settings `json:"settings"`
	RuntimeEnabled       bool                    `json:"runtime_enabled"`
	RestartRequired      bool                    `json:"restart_required"`
	TokenConfigured      bool                    `json:"token_configured"`
	SessionAuthenticated bool                    `json:"session_authenticated"`
	Account              *cloudAccountView       `json:"account,omitempty"`
	Stats                cloudStats              `json:"stats"`
	Files                []cloudFileView         `json:"files"`
	FilesTruncated       bool                    `json:"files_truncated"`
}

func (c *webController) cloudOverview(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	saved := c.cfg.Cloud115
	c.mu.RUnlock()
	view := cloudOverviewView{
		Settings: saved.Settings(), RuntimeEnabled: c.cloud != nil,
		RestartRequired: !c.cloudBoot.SameRuntime(saved), Files: []cloudFileView{},
	}
	if c.cloud != nil && c.cloud.drive != nil {
		view.TokenConfigured = c.cloud.drive.environment != nil
		_, view.SessionAuthenticated, view.Account = c.cloud.drive.current()
		var err error
		view.Stats, view.Files, err = c.cloudFiles(r.Context())
		if err != nil {
			webError(w, http.StatusInternalServerError, "cannot read 115 file status")
			return
		}
		view.FilesTruncated = view.Stats.Total > len(view.Files)
	}
	webJSON(w, http.StatusOK, view)
}

func (c *webController) cloudFiles(ctx context.Context) (cloudStats, []cloudFileView, error) {
	stats, views := cloudStats{}, make([]cloudFileView, 0, cloudFileViewLimit)
	if c.store == nil {
		return stats, views, errors.New("115 state is unavailable")
	}
	cfg := c.cloudBoot
	files, err := c.store.ListCloudFiles(ctx, cfg.TargetCID)
	if err != nil {
		return stats, views, err
	}
	var root *os.Root
	if len(files) > 0 {
		root, _ = os.OpenRoot(cfg.STRMDir)
		if root != nil {
			defer root.Close()
		}
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return stats, views, err
		}
		stats.Total++
		view := cloudFileView{
			TaskID: file.TaskID, Platform: file.Provider, TrackID: file.TrackID,
			RelativePath: file.RelativePath, Size: file.Size, Status: "pending",
		}
		if file.ID == "" {
			stats.Pending++
		} else {
			stats.Confirmed++
			view.Status = "confirmed"
			relative := filepath.FromSlash(file.RelativePath + ".strm")
			expected := cfg.MediaBaseURL + "/media/" + file.MediaID + "\n"
			if filepath.IsLocal(relative) && strmMatches(root, relative, expected) {
				stats.Exported++
				view.Status = "exported"
				view.STRMPath = filepath.Join(cfg.STRMDir, relative)
			}
		}
		if len(views) < cloudFileViewLimit {
			views = append(views, view)
		}
	}
	return stats, views, nil
}

func strmMatches(root *os.Root, relative string, expected string) bool {
	if root == nil {
		return false
	}
	info, err := root.Lstat(relative)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(expected)) {
		return false
	}
	file, err := root.Open(relative)
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
	return err == nil && string(data) == expected
}

func (c *webController) cloudSettings(w http.ResponseWriter, r *http.Request) {
	if !c.available(w) {
		return
	}
	var input config.Cloud115Settings
	if !readWebJSON(w, r, &input) {
		return
	}
	c.changes.Lock()
	defer c.changes.Unlock()
	candidate, err := c.editor.Cloud115(input)
	if err != nil {
		editError(w, err)
		return
	}
	// Keep the running listener and handoff options together, even after a
	// separate subscription or global settings edit reloads the YAML.
	c.mu.Lock()
	next := *c.cfg
	next.Cloud115 = candidate.Cloud115
	c.cfg = &next
	c.mu.Unlock()
	webJSON(w, http.StatusOK, map[string]bool{"saved": true, "restart_required": !c.cloudBoot.SameRuntime(candidate.Cloud115)})
}

func (c *webController) cloudReady(w http.ResponseWriter) bool {
	if c.cloud == nil || c.cloud.drive == nil || c.cloud.login == nil {
		webError(w, http.StatusConflict, "enable 115 in settings and restart Meloport first")
		return false
	}
	return true
}

func (c *webController) cloudCheck(w http.ResponseWriter, r *http.Request) {
	if !c.cloudReady(w) {
		return
	}
	client, _, _ := c.cloud.drive.current()
	if client == nil {
		writeCloudError(w, errCloudLoginRequired)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	account, count, err := verifyCloudAccess(ctx, client, c.cloudBoot.TargetCID)
	if err != nil {
		writeCloudError(w, err)
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"account": account, "entries": count})
}

func verifyCloudAccess(ctx context.Context, client cloudDriveClient, cid string) (*cloudAccountView, int, error) {
	user, err := client.UserInfo(ctx)
	if err != nil {
		return nil, 0, err
	}
	files, err := client.List(ctx, cid)
	if err != nil {
		return nil, 0, err
	}
	return &cloudAccountView{ID: user.ID, Name: user.Name}, len(files), nil
}

func (c *webController) cloudReconcile(w http.ResponseWriter, r *http.Request) {
	if !c.cloudReady(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := c.cloud.sync.Reconcile(ctx); err != nil {
		if errors.Is(err, errCloudLoginRequired) || errors.Is(err, cloud115.ErrUnauthorized) || errors.Is(err, context.DeadlineExceeded) {
			writeCloudError(w, err)
			return
		}
		c.logger.Warn("manual 115 reconciliation failed", "error", err)
		webError(w, http.StatusBadGateway, "115 reconcile failed; check CD2, the target directory and service logs")
		return
	}
	stats, _, err := c.cloudFiles(ctx)
	if err != nil {
		webError(w, http.StatusInternalServerError, "cannot read 115 file status after reconcile")
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

func writeCloudError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	message := err.Error()
	switch {
	case errors.Is(err, errCloudLoginRequired), errors.Is(err, cloud115.ErrUnauthorized):
		status = http.StatusConflict
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status = http.StatusGatewayTimeout
	}
	webError(w, status, strings.TrimSpace(message))
}
