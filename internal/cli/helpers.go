package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// resolveContent returns the content string directly, or if it starts with "@",
// reads the content from the referenced file path. A leading "~" or "~/" in the
// file path is expanded to the current user's home directory, since os.ReadFile
// does not perform shell-style tilde expansion.
func resolveContent(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "@") {
		path, err := expandHome(s[1:])
		if err != nil {
			return "", fmt.Errorf("reading file %s: %w", s[1:], err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading file %s: %w", s[1:], err)
		}
		return strings.TrimRight(string(data), "\n"), nil
	}
	return s, nil
}

// expandHome replaces a leading "~" or "~/" in path with the current user's
// home directory. Other paths (including "~user" forms) are returned unchanged.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expanding ~: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// parseNameContent splits a "name=content" or "name=@file" string into name
// and resolved content. The flagName parameter is used in error messages.
func parseNameContent(s, flagName string) (string, string, error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", fmt.Errorf("invalid --%s value %q: must be name=content or name=@file", flagName, s)
	}
	content, err := resolveContent(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("resolving --%s %q: %w", flagName, parts[0], err)
	}
	return parts[0], content, nil
}

// parseMCPFlag parses a --mcp flag value in the format "name=JSON" or
// "name=@file" into an MCPServerSpec. The JSON (or file content) must
// contain at least a "type" field.
func parseMCPFlag(s string) (kelos.MCPServerSpec, error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return kelos.MCPServerSpec{}, fmt.Errorf("invalid --mcp value %q: must be name=JSON or name=@file", s)
	}
	name := parts[0]
	content, err := resolveContent(parts[1])
	if err != nil {
		return kelos.MCPServerSpec{}, fmt.Errorf("resolving --mcp %q: %w", name, err)
	}

	var raw struct {
		Type    string            `json:"type"`
		Command string            `json:"command,omitempty"`
		Args    []string          `json:"args,omitempty"`
		URL     string            `json:"url,omitempty"`
		Headers map[string]string `json:"headers,omitempty"`
		Env     json.RawMessage   `json:"env,omitempty"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return kelos.MCPServerSpec{}, fmt.Errorf("invalid --mcp %q JSON: %w", name, err)
	}
	if raw.Type == "" {
		return kelos.MCPServerSpec{}, fmt.Errorf("--mcp %q: \"type\" field is required", name)
	}
	switch raw.Type {
	case "stdio":
		if raw.Command == "" {
			return kelos.MCPServerSpec{}, fmt.Errorf("--mcp %q: \"command\" is required when type is stdio", name)
		}
	case "http", "sse":
		if raw.URL == "" {
			return kelos.MCPServerSpec{}, fmt.Errorf("--mcp %q: \"url\" is required when type is %s", name, raw.Type)
		}
	default:
		return kelos.MCPServerSpec{}, fmt.Errorf("--mcp %q: unsupported type %q (must be stdio, http, or sse)", name, raw.Type)
	}

	env, err := parseMCPEnv(name, raw.Env)
	if err != nil {
		return kelos.MCPServerSpec{}, err
	}

	return kelos.MCPServerSpec{
		Name:    name,
		Type:    raw.Type,
		Command: raw.Command,
		Args:    raw.Args,
		URL:     raw.URL,
		Headers: raw.Headers,
		Env:     env,
	}, nil
}

// parseMCPEnv accepts the env field of a --mcp JSON payload in either of two
// shapes:
//
//   - a []corev1.EnvVar list with full valueFrom support, e.g.
//     [{"name":"FOO","value":"bar"},{"name":"BAZ","valueFrom":{...}}]
//   - a {"NAME":"VALUE",...} map, retained as shorthand for the common case of
//     literal values
//
// Both decode into []corev1.EnvVar so downstream code only deals with one
// shape.
func parseMCPEnv(name string, raw json.RawMessage) ([]corev1.EnvVar, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	trimmed := strings.TrimLeft(string(raw), " \t\n\r")
	if strings.HasPrefix(trimmed, "[") {
		var list []corev1.EnvVar
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("--mcp %q: invalid env list: %w", name, err)
		}
		return list, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var asMap map[string]string
		if err := json.Unmarshal(raw, &asMap); err != nil {
			return nil, fmt.Errorf("--mcp %q: invalid env map: %w", name, err)
		}
		if len(asMap) == 0 {
			return nil, nil
		}
		keys := make([]string, 0, len(asMap))
		for k := range asMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]corev1.EnvVar, 0, len(asMap))
		for _, k := range keys {
			out = append(out, corev1.EnvVar{Name: k, Value: asMap[k]})
		}
		return out, nil
	}
	return nil, fmt.Errorf("--mcp %q: env must be an array or object", name)
}

// parseSkillsShFlag parses a --skills-sh flag value in the format
// "source" or "source:skill" into a SkillsShSpec. A colon only selects a
// skill when it is the final separator and the suffix does not contain a
// slash, so full URLs such as https://ghe.example.com:8443/org/repo.git stay
// intact as sources.
func parseSkillsShFlag(s string) (kelos.SkillsShSpec, error) {
	if s == "" {
		return kelos.SkillsShSpec{}, fmt.Errorf("invalid --skills-sh value: must not be empty")
	}
	source := s
	skill := ""
	if idx := strings.LastIndex(s, ":"); idx >= 0 && !strings.Contains(s[idx+1:], "/") {
		source = s[:idx]
		skill = s[idx+1:]
	}
	if source == "" {
		return kelos.SkillsShSpec{}, fmt.Errorf("invalid --skills-sh value %q: source must not be empty", s)
	}
	spec := kelos.SkillsShSpec{Source: source}
	if skill != "" || source != s {
		if skill == "" {
			return kelos.SkillsShSpec{}, fmt.Errorf("invalid --skills-sh value %q: skill name after colon must not be empty", s)
		}
		spec.Skill = skill
	}
	return spec, nil
}
