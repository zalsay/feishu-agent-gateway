package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type conversation struct {
	Selected     string                 `json:"selected,omitempty"`
	Preferences  map[string]RunOptions  `json:"preferences,omitempty"`
	PendingCards map[string]PendingCard `json:"pending_cards,omitempty"`
	Sessions     map[string]string      `json:"sessions,omitempty"`
	Events       []string               `json:"events,omitempty"`
}

type PendingCard struct {
	Agent    string    `json:"agent"`
	Kind     string    `json:"kind"`
	Expected string    `json:"expected"`
	Expires  time.Time `json:"expires"`
}

type diskState struct {
	Conversations map[string]*conversation `json:"conversations"`
}

func loadState(path string) (diskState, error) {
	state := diskState{Conversations: map[string]*conversation{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Conversations == nil {
		state.Conversations = map[string]*conversation{}
	}
	return state, nil
}

func saveState(path string, state diskState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".gateway-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
