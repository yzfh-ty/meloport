package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

var (
	ErrNotFound = errors.New("task not found")
	ErrConflict = errors.New("configuration conflict")
)

// TaskInput contains editable values, not derived IDs, absolute paths or an
// inherited schedule/quality. Empty optional strings retain inheritance.
type TaskInput struct {
	ID            string `json:"id"`
	Platform      string `json:"platform"`
	Playlist      string `json:"playlist"`
	SourceType    string `json:"source_type"`
	SourceID      string `json:"source_id"`
	Schedule      string `json:"schedule"`
	Quality       string `json:"quality"`
	AutoDowngrade *bool  `json:"auto_downgrade"`
	Directory     string `json:"directory"`
	Enabled       bool   `json:"enabled"`
}

type SettingsInput struct {
	Timezone      string  `json:"timezone"`
	Schedule      string  `json:"schedule"`
	Quality       string  `json:"quality"`
	AutoDowngrade bool    `json:"auto_downgrade"`
	FallbackToAPI bool    `json:"fallback_to_api"`
	MusicDir      *string `json:"music_dir,omitempty"`
}

// Editor applies scoped YAML-node changes. Readers and writers through this
// instance are serialized; a fresh byte comparison also catches external edits
// made before publication. No operation writes environment variables or keys.
type Editor struct {
	path          string
	mu            sync.Mutex
	activeCloud   Cloud115
	beforePublish func() // Deterministic coordination for external-editor tests.
}

func NewEditor(path string) *Editor { return &Editor{path: path} }

func (e *Editor) Tasks() ([]TaskInput, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	doc, _, _, err := readEditableDocument(e.path)
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]
	sequence, err := taskSequence(root, false)
	if err != nil {
		return nil, err
	}
	out := make([]TaskInput, 0)
	if sequence == nil {
		return out, nil
	}
	for _, node := range sequence.Content {
		if node.Kind != yaml.MappingNode {
			return nil, errors.New("tasks must contain plain YAML mappings")
		}
		var task Task
		if err := node.Decode(&task); err != nil {
			return nil, errors.New("cannot read editable task fields")
		}
		sourceType, sourceID := task.SourceType, task.SourceID
		if sourceType == "" {
			sourceType, sourceID = "playlist", task.Playlist
		}
		if sourceType == "playlist" && sourceID == "" {
			sourceID = task.Playlist
		}
		if sourceType == "playlist" && sourceID != "" {
			platform := task.Platform
			if platform == "" {
				platform = "netease"
			}
			if normalized, err := ParsePlaylistFor(platform, sourceID); err == nil {
				sourceID = normalized
			}
		}
		out = append(out, TaskInput{ID: task.ID, Platform: task.Platform, Playlist: task.Playlist, SourceType: sourceType, SourceID: sourceID, Schedule: task.Schedule, Quality: task.Quality, AutoDowngrade: task.AutoDowngrade, Directory: task.Directory, Enabled: task.Enabled == nil || *task.Enabled})
	}
	return out, nil
}

func (e *Editor) Upsert(originalID string, input TaskInput, create bool) (*Config, error) {
	return e.update(func(root *yaml.Node) error {
		sequence, err := taskSequence(root, true)
		if err != nil {
			return err
		}
		index := -1
		for i, node := range sequence.Content {
			if node.Kind != yaml.MappingNode {
				return errors.New("tasks must contain plain YAML mappings")
			}
			id := scalarField(node, "id")
			if create && strings.EqualFold(id, input.ID) {
				return fmt.Errorf("%w: task id already exists", ErrConflict)
			}
			if id == originalID {
				index = i
			}
		}
		if create {
			if originalID != "" && originalID != input.ID {
				return fmt.Errorf("%w: create path does not match task id", ErrConflict)
			}
			node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			writeTask(node, input)
			sequence.Content = append(sequence.Content, node)
			return nil
		}
		if index < 0 {
			return ErrNotFound
		}
		if input.ID != originalID {
			return fmt.Errorf("%w: task ids are immutable; create a new task instead", ErrConflict)
		}
		writeTask(sequence.Content[index], input)
		return nil
	})
}

func (e *Editor) Delete(id string) (*Config, error) {
	return e.update(func(root *yaml.Node) error {
		sequence, err := taskSequence(root, false)
		if err != nil {
			return err
		}
		if sequence != nil {
			for i, node := range sequence.Content {
				if node.Kind != yaml.MappingNode {
					return errors.New("tasks must contain plain YAML mappings")
				}
				if scalarField(node, "id") == id {
					sequence.Content = append(sequence.Content[:i], sequence.Content[i+1:]...)
					return nil
				}
			}
		}
		return ErrNotFound
	})
}

func (e *Editor) Settings(input SettingsInput) (*Config, error) {
	return e.update(func(root *yaml.Node) error {
		if input.MusicDir != nil {
			if _, err := ResolveMusicDirectory(filepath.Dir(e.path), *input.MusicDir); err != nil {
				return err
			}
			storage := mappingField(root, "storage")
			if storage == nil {
				if existing := field(root, "storage"); existing != nil && existing.Tag != "!!null" {
					return errors.New("storage must be a plain YAML mapping")
				}
				storage = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				setNode(root, "storage", storage)
			}
			setScalar(storage, "music_dir", *input.MusicDir, "!!str")
		}
		setScalar(root, "timezone", input.Timezone, "!!str")
		setScalar(root, "schedule", input.Schedule, "!!str")
		setScalar(root, "quality", input.Quality, "!!str")
		setScalar(root, "auto_downgrade", boolText(input.AutoDowngrade), "!!bool")
		metadata := mappingField(root, "metadata")
		if metadata == nil {
			if existing := field(root, "metadata"); existing != nil && existing.Tag != "!!null" {
				return errors.New("metadata must be a plain YAML mapping")
			}
			metadata = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setNode(root, "metadata", metadata)
		}
		setScalar(metadata, "fallback_to_api", boolText(input.FallbackToAPI), "!!bool")
		return nil
	})
}

func (e *Editor) update(change func(*yaml.Node) error) (*Config, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	doc, original, info, err := readEditableDocument(e.path)
	if err != nil {
		return nil, err
	}
	if err := change(doc.Content[0]); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, errors.New("cannot encode configuration update")
	}
	if err := encoder.Close(); err != nil {
		return nil, errors.New("cannot finish configuration update")
	}
	if buffer.Len() > 1<<20 {
		return nil, errors.New("configuration update exceeds 1 MiB")
	}
	file, err := os.CreateTemp(filepath.Dir(e.path), ".meloport-config-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("stage configuration update: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("preserve configuration permissions: %w", err)
	}
	if _, err := file.Write(buffer.Bytes()); err != nil {
		return nil, fmt.Errorf("write staged configuration: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("flush staged configuration: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close staged configuration: %w", err)
	}
	// Load resolves the candidate against exactly the original file's directory.
	candidate, err := Load(temporary)
	if err != nil {
		return nil, fmt.Errorf("configuration update is invalid: %w", err)
	}
	if (e.activeCloud.Enabled || e.activeCloud.MediaEnabled) && !e.activeCloud.SameRuntime(candidate.Cloud115) {
		// Until restart the running process still writes to the original
		// mount and export trees, even if the YAML uses different ones.
		running := *candidate
		running.Cloud115 = e.activeCloud
		if err := running.validateCloud115(filepath.Dir(temporary)); err != nil {
			return nil, fmt.Errorf("configuration conflicts with the running integration settings; restart Meloport before changing the download directories: %w", err)
		}
	}
	if e.beforePublish != nil {
		e.beforePublish()
	}
	_, current, currentInfo, err := readEditableDocument(e.path)
	if err != nil {
		return nil, fmt.Errorf("%w: configuration changed before save", ErrConflict)
	}
	if !bytes.Equal(original, current) || !os.SameFile(info, currentInfo) || info.Mode().Perm() != currentInfo.Mode().Perm() {
		return nil, fmt.Errorf("%w: configuration was edited externally; reload before saving", ErrConflict)
	}
	if err := os.Rename(temporary, e.path); err != nil {
		return nil, fmt.Errorf("publish configuration update: %w", err)
	}
	return candidate, nil
}

func readEditableDocument(path string) (*yaml.Node, []byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, nil, nil, errors.New("editable configuration must be a regular file of at most 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, nil, errors.New("cannot read bounded configuration contents")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, nil, errors.New("configuration must be one YAML mapping document")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, nil, nil, errors.New("configuration must contain exactly one YAML document")
	}
	// Decoding catches duplicate mapping keys without exposing their raw values.
	var generic any
	if err := doc.Decode(&generic); err != nil {
		return nil, nil, nil, errors.New("configuration contains invalid or duplicate mapping fields")
	}
	return &doc, data, info, nil
}

func taskSequence(root *yaml.Node, create bool) (*yaml.Node, error) {
	node := field(root, "tasks")
	if node == nil || node.Tag == "!!null" {
		if !create {
			return nil, nil
		}
		node = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setNode(root, "tasks", node)
	}
	if node.Kind != yaml.SequenceNode {
		return nil, errors.New("tasks must be a plain YAML sequence")
	}
	return node, nil
}

func writeTask(node *yaml.Node, input TaskInput) {
	setScalar(node, "id", input.ID, "!!str")
	setOptional(node, "platform", input.Platform)
	sourceType := input.SourceType
	if sourceType == "" {
		sourceType = "playlist"
	}
	if sourceType == "ranking" {
		setScalar(node, "source_type", sourceType, "!!str")
		setScalar(node, "source_id", input.SourceID, "!!str")
		deleteField(node, "playlist")
	} else {
		// Keep the established playlist YAML shape for ordinary subscriptions.
		playlist := input.Playlist
		if playlist == "" {
			playlist = input.SourceID
		}
		setScalar(node, "playlist", playlist, "!!str")
		deleteField(node, "source_type")
		deleteField(node, "source_id")
	}
	setOptional(node, "schedule", input.Schedule)
	setOptional(node, "quality", input.Quality)
	if input.AutoDowngrade == nil {
		deleteField(node, "auto_downgrade")
	} else {
		setScalar(node, "auto_downgrade", boolText(*input.AutoDowngrade), "!!bool")
	}
	setOptional(node, "directory", input.Directory)
	setScalar(node, "enabled", boolText(input.Enabled), "!!bool")
}

func deleteField(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func field(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func mappingField(root *yaml.Node, key string) *yaml.Node {
	node := field(root, key)
	if node != nil && node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func scalarField(node *yaml.Node, key string) string {
	value := field(node, key)
	if value != nil && value.Kind == yaml.ScalarNode {
		return value.Value
	}
	return ""
}

func setNode(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			old := mapping.Content[i+1]
			value.HeadComment = old.HeadComment
			value.LineComment = old.LineComment
			value.FootComment = old.FootComment
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func setScalar(mapping *yaml.Node, key, value, tag string) {
	if existing := field(mapping, key); existing != nil && existing.Kind == yaml.ScalarNode {
		existing.Value = value
		existing.Tag = tag
		if tag != "!!str" {
			existing.Style = 0
		}
		return
	}
	setNode(mapping, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

func setOptional(mapping *yaml.Node, key, value string) {
	if value != "" {
		setScalar(mapping, key, value, "!!str")
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
