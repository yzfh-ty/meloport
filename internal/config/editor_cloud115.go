package config

import (
	"errors"

	"go.yaml.in/yaml/v3"
)

// Cloud115Settings holds the editable configuration. Account credentials are
// managed separately and are never accepted by the YAML editor.
type Cloud115Settings struct {
	Enabled               bool   `json:"enabled"`
	AccessTokenEnv        string `json:"access_token_env"`
	ClientID              string `json:"client_id"`
	TargetCID             string `json:"target_cid"`
	StagingDir            string `json:"staging_dir"`
	STRMDir               string `json:"strm_dir"`
	MediaEnabled          bool   `json:"media_enabled"`
	MediaListen           string `json:"media_listen"`
	MediaBaseURL          string `json:"media_base_url"`
	NavidromeEnabled      bool   `json:"navidrome_enabled"`
	NavidromeURL          string `json:"navidrome_url"`
	NavidromeMountRoot    string `json:"navidrome_mount_root"`
	NavidromeUsername     string `json:"navidrome_username"`
	NavidromePasswordEnv  string `json:"navidrome_password_env"`
	NavidromePlaylistSync bool   `json:"navidrome_playlist_sync"`
	PollInterval          string `json:"poll_interval"`
	Timeout               string `json:"timeout"`
}

func (c Cloud115) Settings() Cloud115Settings {
	stagingDir, strmDir := c.stagingDirInput, c.strmDirInput
	if stagingDir == "" {
		stagingDir = c.StagingDir
	}
	if strmDir == "" {
		strmDir = c.STRMDir
	}
	return Cloud115Settings{
		Enabled: c.Enabled, AccessTokenEnv: c.AccessTokenEnv, ClientID: c.ClientID, TargetCID: c.TargetCID,
		StagingDir: stagingDir, STRMDir: strmDir, MediaEnabled: c.MediaEnabled, MediaListen: c.MediaListen, MediaBaseURL: c.MediaBaseURL,
		NavidromeEnabled: c.NavidromeEnabled, NavidromeURL: c.NavidromeURL, NavidromeMountRoot: c.NavidromeMountRoot,
		NavidromeUsername: c.NavidromeUsername, NavidromePasswordEnv: c.NavidromePasswordEnv, NavidromePlaylistSync: c.NavidromePlaylistSync,
		PollInterval: c.PollInterval.String(), Timeout: c.Timeout.String(),
	}
}

// SameRuntime compares the effective values, ignoring equivalent path spellings
// and edits to a disabled integration.
func (c Cloud115) SameRuntime(other Cloud115) bool {
	if c.Enabled != other.Enabled || c.MediaEnabled != other.MediaEnabled || c.NavidromeEnabled != other.NavidromeEnabled {
		return false
	}
	if c.Enabled && (c.AccessTokenEnv != other.AccessTokenEnv || c.ClientID != other.ClientID || c.TargetCID != other.TargetCID ||
		c.StagingDir != other.StagingDir || c.STRMDir != other.STRMDir || c.MediaBaseURL != other.MediaBaseURL ||
		c.PollInterval != other.PollInterval || c.Timeout != other.Timeout) {
		return false
	}
	if c.MediaEnabled && c.MediaListen != other.MediaListen {
		return false
	}
	if c.NavidromeEnabled {
		if c.NavidromeURL != other.NavidromeURL || c.NavidromeMountRoot != other.NavidromeMountRoot || c.NavidromePlaylistSync != other.NavidromePlaylistSync {
			return false
		}
		if c.NavidromePlaylistSync && (c.NavidromeUsername != other.NavidromeUsername || c.NavidromePasswordEnv != other.NavidromePasswordEnv) {
			return false
		}
	}
	return true
}

// ProtectActiveCloud keeps storage and task edits valid for the active settings
// until the process restarts and loads the settings saved on disk.
func (e *Editor) ProtectActiveCloud(active Cloud115) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeCloud = active
}

func (e *Editor) Cloud115(input Cloud115Settings) (*Config, error) {
	return e.update(func(root *yaml.Node) error {
		cloud := mappingField(root, "cloud115")
		if cloud == nil {
			if existing := field(root, "cloud115"); existing != nil && existing.Tag != "!!null" {
				return errors.New("cloud115 must be a plain YAML mapping")
			}
			cloud = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setNode(root, "cloud115", cloud)
		}
		setScalar(cloud, "enabled", boolText(input.Enabled), "!!bool")
		setScalar(cloud, "media_enabled", boolText(input.MediaEnabled), "!!bool")
		setScalar(cloud, "navidrome_enabled", boolText(input.NavidromeEnabled), "!!bool")
		for _, entry := range []struct{ key, value string }{
			{"access_token_env", input.AccessTokenEnv}, {"client_id", input.ClientID}, {"target_cid", input.TargetCID},
			{"staging_dir", input.StagingDir}, {"strm_dir", input.STRMDir}, {"media_listen", input.MediaListen},
			{"media_base_url", input.MediaBaseURL}, {"navidrome_url", input.NavidromeURL}, {"navidrome_mount_root", input.NavidromeMountRoot},
			{"navidrome_username", input.NavidromeUsername}, {"navidrome_password_env", input.NavidromePasswordEnv},
			{"poll_interval", input.PollInterval}, {"timeout", input.Timeout},
		} {
			setOptional(cloud, entry.key, entry.value)
		}
		setScalar(cloud, "navidrome_playlist_sync", boolText(input.NavidromePlaylistSync), "!!bool")
		return nil
	})
}
