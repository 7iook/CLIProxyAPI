package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// MaxAccountGroups bounds the size of the account group registry.
	MaxAccountGroups         = 200
	maxAccountGroupNameRunes = 64
)

var (
	accountGroupIDPattern    = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	accountGroupColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// AccountGroup is a display-only credential group shown in the management panel.
type AccountGroup struct {
	ID    string `yaml:"id" json:"id"`
	Name  string `yaml:"name" json:"name"`
	Color string `yaml:"color,omitempty" json:"color,omitempty"`
}

// NormalizeAccountGroups trims and validates groups, generating ids when empty.
// The returned slice is never nil and preserves input order.
func NormalizeAccountGroups(groups []AccountGroup) ([]AccountGroup, error) {
	if len(groups) > MaxAccountGroups {
		return nil, fmt.Errorf("too many groups: at most %d allowed", MaxAccountGroups)
	}
	out := make([]AccountGroup, 0, len(groups))
	ids := make(map[string]struct{}, len(groups))
	names := make(map[string]struct{}, len(groups))
	for _, in := range groups {
		g := AccountGroup{
			ID:    strings.TrimSpace(in.ID),
			Name:  strings.TrimSpace(in.Name),
			Color: strings.TrimSpace(in.Color),
		}
		if g.ID != "" {
			if !accountGroupIDPattern.MatchString(g.ID) {
				return nil, fmt.Errorf("invalid group id %q", g.ID)
			}
			if _, dup := ids[g.ID]; dup {
				return nil, fmt.Errorf("duplicate group id %q", g.ID)
			}
			ids[g.ID] = struct{}{}
		}
		if n := utf8.RuneCountInString(g.Name); n == 0 || n > maxAccountGroupNameRunes {
			return nil, fmt.Errorf("group name must be 1-%d characters", maxAccountGroupNameRunes)
		}
		key := strings.ToLower(g.Name)
		if _, dup := names[key]; dup {
			return nil, fmt.Errorf("duplicate group name %q", g.Name)
		}
		names[key] = struct{}{}
		if g.Color != "" {
			if !accountGroupColorPattern.MatchString(g.Color) {
				return nil, fmt.Errorf("invalid group color %q", g.Color)
			}
			g.Color = strings.ToUpper(g.Color)
		}
		out = append(out, g)
	}
	for i := range out {
		if out[i].ID != "" {
			continue
		}
		id, err := newAccountGroupID(ids)
		if err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
		out[i].ID = id
	}
	return out, nil
}

func newAccountGroupID(used map[string]struct{}) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generate group id: %w", err)
		}
		id := "g-" + hex.EncodeToString(buf)
		if _, taken := used[id]; !taken {
			return id, nil
		}
	}
	return "", fmt.Errorf("generate group id: too many collisions")
}
