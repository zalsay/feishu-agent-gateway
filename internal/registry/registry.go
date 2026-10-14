package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"

	"feishu-agent-gateway/internal/agents"
	"feishu-agent-gateway/internal/gateway"
)

const maxConfigSize = 1 << 20

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	envPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type configFile struct {
	Agents []agentConfig `json:"agents"`
}

type agentConfig struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	BinaryEnv  optionalString `json:"binary_env"`
	RepoEnv    optionalString `json:"repo_env"`
	HomeEnv    optionalString `json:"home_env"`
	BaseURLEnv optionalString `json:"base_url_env"`
	APIKeyEnv  optionalString `json:"api_key_env"`
	ModelEnv   optionalString `json:"model_env"`
}

type optionalString struct {
	value   string
	present bool
}

func (s *optionalString) UnmarshalJSON(data []byte) error {
	var value *string
	if err := json.Unmarshal(data, &value); err != nil || value == nil {
		return errors.New("agent config field must be a string")
	}
	s.value = *value
	s.present = true
	return nil
}

// Load creates all configured agents. An empty path preserves the legacy
// Codex/Hermes environment configuration; a non-empty path is parsed strictly.
func Load(path string, getenv func(string) string) (map[string]gateway.Agent, error) {
	if getenv == nil {
		return nil, errors.New("environment lookup function is required")
	}
	if path == "" {
		return loadLegacy(getenv)
	}

	data, err := readConfig(path)
	if err != nil {
		return nil, err
	}
	if err := validateSingleJSON(data); err != nil {
		return nil, fmt.Errorf("invalid agent config: %w", err)
	}
	if err := validateExactConfigKeys(data); err != nil {
		return nil, fmt.Errorf("invalid agent config: %w", err)
	}

	var cfg configFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid agent config: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	result := make(map[string]gateway.Agent, len(cfg.Agents))
	for index, item := range cfg.Agents {
		agent, err := buildAgent(item, getenv)
		if err != nil {
			return nil, fmt.Errorf("agent at index %d: %w", index, err)
		}
		result[item.Name] = agent
	}
	return result, nil
}

func readConfig(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open agent config: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}
	if len(data) > maxConfigSize {
		return nil, errors.New("agent config exceeds 1 MiB limit")
	}
	return data, nil
}

func validateSingleJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("extra JSON value after agent config")
		}
		return err
	}
	return nil
}

// validateExactConfigKeys rejects keys that encoding/json would otherwise
// match case-insensitively to a struct field.
func validateExactConfigKeys(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	if root == nil {
		return errors.New("agent config root must be an object")
	}
	for key := range root {
		if key != "agents" {
			return errors.New("unknown agent config field")
		}
	}
	agentsJSON, ok := root["agents"]
	if !ok {
		return errors.New("agent config is missing agents")
	}
	trimmed := bytes.TrimSpace(agentsJSON)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return errors.New("agents must be an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(agentsJSON, &entries); err != nil {
		return err
	}
	for index, entry := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil {
			return fmt.Errorf("agent at index %d must be an object", index)
		}
		if fields == nil {
			return fmt.Errorf("agent at index %d must be an object", index)
		}
		for key := range fields {
			switch key {
			case "name", "type", "binary_env", "repo_env", "home_env", "base_url_env", "api_key_env", "model_env":
			default:
				return fmt.Errorf("agent at index %d has unknown field", index)
			}
		}
	}
	return nil
}

// scanJSONValue rejects duplicate keys, which encoding/json otherwise accepts
// by silently retaining the last value.
func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func validateConfig(cfg configFile) error {
	if len(cfg.Agents) == 0 {
		return errors.New("agent config must contain at least one agent")
	}
	seen := make(map[string]struct{}, len(cfg.Agents))
	for _, item := range cfg.Agents {
		if !namePattern.MatchString(item.Name) {
			return errors.New("invalid agent name")
		}
		if _, duplicate := seen[item.Name]; duplicate {
			return errors.New("duplicate agent name")
		}
		seen[item.Name] = struct{}{}

		switch item.Type {
		case "codex":
			if item.BaseURLEnv.present || item.APIKeyEnv.present || item.ModelEnv.present {
				return errors.New("codex agent has unsupported fields")
			}
			if err := requireEnvReference(item.RepoEnv, "repo_env"); err != nil {
				return err
			}
			if err := requireEnvReference(item.HomeEnv, "home_env"); err != nil {
				return err
			}
			if item.BinaryEnv.present {
				if err := validateEnvName(item.BinaryEnv.value); err != nil {
					return fmt.Errorf("invalid binary_env: %w", err)
				}
			}
		case "hermes":
			if item.BinaryEnv.present || item.RepoEnv.present || item.HomeEnv.present {
				return errors.New("hermes agent has unsupported fields")
			}
			if err := requireEnvReference(item.BaseURLEnv, "base_url_env"); err != nil {
				return err
			}
			if err := requireEnvReference(item.APIKeyEnv, "api_key_env"); err != nil {
				return err
			}
			if item.ModelEnv.present {
				if err := validateEnvName(item.ModelEnv.value); err != nil {
					return fmt.Errorf("invalid model_env: %w", err)
				}
			}
		default:
			return errors.New("unsupported agent type")
		}
	}
	return nil
}

func requireEnvReference(ref optionalString, field string) error {
	if !ref.present {
		return fmt.Errorf("missing required %s environment reference", field)
	}
	return validateEnvName(ref.value)
}

func validateEnvName(name string) error {
	if !envPattern.MatchString(name) {
		return errors.New("invalid environment variable name")
	}
	return nil
}

func buildAgent(item agentConfig, getenv func(string) string) (gateway.Agent, error) {
	switch item.Type {
	case "codex":
		binary, err := optionalValue(item.BinaryEnv, "codex", getenv)
		if err != nil {
			return nil, err
		}
		if _, err := exec.LookPath(binary); err != nil {
			return nil, fmt.Errorf("Codex binary is unavailable")
		}
		repo, err := requiredValue(item.RepoEnv, getenv)
		if err != nil {
			return nil, err
		}
		home, err := requiredValue(item.HomeEnv, getenv)
		if err != nil {
			return nil, err
		}
		return agents.NewCodex(binary, repo, home)
	case "hermes":
		base, err := requiredValue(item.BaseURLEnv, getenv)
		if err != nil {
			return nil, err
		}
		key, err := requiredValue(item.APIKeyEnv, getenv)
		if err != nil {
			return nil, err
		}
		model, err := optionalValue(item.ModelEnv, "hermes-agent", getenv)
		if err != nil {
			return nil, err
		}
		return agents.NewHermes(base, key, model, &http.Client{})
	default:
		return nil, errors.New("unsupported agent type")
	}
}

func requiredValue(ref optionalString, getenv func(string) string) (string, error) {
	value := getenv(ref.value)
	if value == "" {
		return "", fmt.Errorf("missing required environment variable %s", ref.value)
	}
	return value, nil
}

func optionalValue(ref optionalString, defaultValue string, getenv func(string) string) (string, error) {
	if !ref.present {
		return defaultValue, nil
	}
	value := getenv(ref.value)
	if value == "" {
		return "", fmt.Errorf("missing required environment variable %s", ref.value)
	}
	return value, nil
}

func loadLegacy(getenv func(string) string) (map[string]gateway.Agent, error) {
	codexConfig := agentConfig{
		Name:      "codex",
		Type:      "codex",
		BinaryEnv: optionalString{value: "CODEX_BIN", present: true},
		RepoEnv:   optionalString{value: "CODEX_REPO", present: true},
		HomeEnv:   optionalString{value: "GATEWAY_CODEX_HOME", present: true},
	}
	hermesConfig := agentConfig{
		Name:       "hermes",
		Type:       "hermes",
		BaseURLEnv: optionalString{value: "HERMES_BASE_URL", present: true},
		APIKeyEnv:  optionalString{value: "HERMES_API_KEY", present: true},
		ModelEnv:   optionalString{value: "HERMES_MODEL", present: true},
	}

	// Legacy optional variables preserve their defaults when unset.
	codexConfig.BinaryEnv.present = getenv("CODEX_BIN") != ""
	hermesConfig.ModelEnv.present = getenv("HERMES_MODEL") != ""

	result := make(map[string]gateway.Agent, 2)
	for _, item := range []agentConfig{codexConfig, hermesConfig} {
		agent, err := buildAgent(item, getenv)
		if err != nil {
			return nil, fmt.Errorf("legacy agent initialization failed: %w", err)
		}
		result[item.Name] = agent
	}
	return result, nil
}
