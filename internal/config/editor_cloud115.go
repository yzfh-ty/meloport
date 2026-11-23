package config

import (
	"errors"

	"go.yaml.in/yaml/v3"
)

// Cloud115Settings holds the Web-editable integration options. Deployment
// settings and account credentials are never accepted by the YAML editor.
type Cloud115Settings struct {
	Enabled               bool   `json:"enabled"`
	TargetCID             string `json:"target_cid"`
	MountDir              string `json:"mount_dir"`
	STRMDir               string `json:"strm_dir"`
	MediaEnabled          bool   `json:"media_enabled"`
	MediaBaseURL          string `json:"media_base_url"`
	NavidromeEnabled      bool   `json:"navidrome_enabled"`
	NavidromeURL          string `json:"navidrome_url"`
	NavidromeMountRoot    string `json:"navidrome_mount_root"`
	NavidromePlaylistSync bool   `json:"navidrome_playlist_sync"`
}

func (c Cloud115) Settings() Cloud115Settings {
	mountDir, strmDir := c.mountDirInput, c.strmDirInput
	if mountDir == "" {
		mountDir = c.MountDir
	}
	if strmDir == "" {
		strmDir = c.STRMDir
	}
	return Cloud115Settings{
		Enabled: c.Enabled, TargetCID: c.TargetCID,
		MountDir: mountDir, STRMDir: strmDir, MediaEnabled: c.MediaEnabled, MediaBaseURL: c.MediaBaseURL,
		NavidromeEnabled: c.NavidromeEnabled, NavidromeURL: c.NavidromeURL, NavidromeMountRoot: c.NavidromeMountRoot,
		NavidromePlaylistSync: c.NavidromePlaylistSync,
	}
}

// SameRuntime compares the effective values, ignoring equivalent path spellings
// and edits to a disabled integration.
func (c Cloud115) SameRuntime(other Cloud115) bool {
	if c.Enabled != other.Enabled || c.MediaEnabled != other.MediaEnabled || c.NavidromeEnabled != other.NavidromeEnabled {
		return false
	}
	if c.Enabled && (c.AccessTokenEnv != other.AccessTokenEnv || c.ClientID != other.ClientID || c.TargetCID != other.TargetCID ||
		c.MountDir != other.MountDir || c.STRMDir != other.STRMDir || c.MediaBaseURL != other.MediaBaseURL ||
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
		if c.NavidromePlaylistSync && (c.NavidromeUsername != other.NavidromeUsername || c.NavidromePasswordEnv != other.NavidromePasswordEnv || c.Timeout != other.Timeout) {
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
		deleteField(cloud, "staging_dir")
		setScalar(cloud, "enabled", boolText(input.Enabled), "!!bool")
		setScalar(cloud, "media_enabled", boolText(input.MediaEnabled), "!!bool")
		setScalar(cloud, "navidrome_enabled", boolText(input.NavidromeEnabled), "!!bool")
		for _, entry := range []struct{ key, value string }{
			{"target_cid", input.TargetCID}, {"mount_dir", input.MountDir}, {"strm_dir", input.STRMDir},
			{"media_base_url", input.MediaBaseURL}, {"navidrome_url", input.NavidromeURL}, {"navidrome_mount_root", input.NavidromeMountRoot},
		} {
			setOptional(cloud, entry.key, entry.value)
		}
		setScalar(cloud, "navidrome_playlist_sync", boolText(input.NavidromePlaylistSync), "!!bool")
		return nil
	})
}
