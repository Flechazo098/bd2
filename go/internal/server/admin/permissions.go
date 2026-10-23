package admin

import (
	"fmt"
	"slices"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

// Permissions an owner may grant to an administrator. Managing administrators
// and the server overview are never grantable: they belong to the owner.
const (
	PermissionMail      = "mail.send"
	PermissionPlayers   = "players.view"
	PermissionInventory = "players.inventory"
)

// Configuration permissions are "config.<document>.<section>.read|write".
func configPermission(document, section, mode string) string {
	return "config." + document + "." + section + "." + mode
}

// DefaultAdministratorPermissions are granted when an owner appoints an
// administrator. Mail is never implied: the owner grants it explicitly.
var DefaultAdministratorPermissions = []string{PermissionPlayers}

type Ability struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Help     string   `json:"help"`
	Requires []string `json:"requires,omitempty"`
	Default  bool     `json:"default"`
}

var abilities = []Ability{
	{Key: PermissionPlayers, Label: "查看玩家", Help: "查看玩家列表、UID 与背包设置。", Default: true},
	{Key: PermissionMail, Label: "邮件发放", Help: "给自己的账号寄送物品和货币；只有服主能给其他玩家寄送。", Requires: []string{PermissionPlayers}},
	{Key: PermissionInventory, Label: "调整背包容量", Help: "开关任意玩家的无限背包容量。", Requires: []string{PermissionPlayers}},
}

type ConfigurationGrant struct {
	Document string                 `json:"document"`
	Title    string                 `json:"title"`
	Sections []ConfigurationSection `json:"sections"`
}

type ConfigurationSection struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// PermissionCatalog describes every grantable permission for the owner's editor.
type PermissionCatalog struct {
	Abilities     []Ability            `json:"abilities"`
	Configuration []ConfigurationGrant `json:"configuration"`
}

func permissionCatalog(registry *commonconfig.Registry) PermissionCatalog {
	catalog := PermissionCatalog{Abilities: abilities}
	for _, entry := range registry.Entries() {
		schema := entry.Schema()
		grant := ConfigurationGrant{Document: entry.Name(), Title: schema.Title}
		for _, section := range schema.Sections {
			grant.Sections = append(grant.Sections, ConfigurationSection{ID: section.ID, Title: section.Title})
		}
		catalog.Configuration = append(catalog.Configuration, grant)
	}
	return catalog
}

// normalizePermissions rejects unknown keys and adds implied permissions:
// writing a section implies reading it, and player tools imply the player list.
func normalizePermissions(registry *commonconfig.Registry, requested []string) ([]string, error) {
	known := map[string]bool{}
	for _, ability := range abilities {
		known[ability.Key] = true
	}
	for _, entry := range registry.Entries() {
		for _, section := range entry.Schema().Sections {
			known[configPermission(entry.Name(), section.ID, "read")] = true
			known[configPermission(entry.Name(), section.ID, "write")] = true
		}
	}
	granted := map[string]bool{}
	for _, key := range requested {
		if !known[key] {
			return nil, fmt.Errorf("admin: unknown permission %q", key)
		}
		granted[key] = true
	}
	for key := range granted {
		if section, ok := strings.CutSuffix(key, ".write"); ok {
			granted[section+".read"] = true
		}
	}
	for _, ability := range abilities {
		if granted[ability.Key] {
			for _, required := range ability.Requires {
				granted[required] = true
			}
		}
	}
	out := make([]string, 0, len(granted))
	for key := range granted {
		out = append(out, key)
	}
	slices.Sort(out)
	return out, nil
}

// configurationAccess converts a session's permissions into section access.
func (s session) configurationAccess(document string) commonconfig.Access {
	if s.Role == RoleOwner {
		return commonconfig.AllAccess
	}
	access := commonconfig.Access{Read: map[string]bool{}, Write: map[string]bool{}}
	prefix := "config." + document + "."
	for _, key := range s.Permissions {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		if section, ok := strings.CutSuffix(rest, ".write"); ok {
			access.Write[section] = true
		} else if section, ok := strings.CutSuffix(rest, ".read"); ok {
			access.Read[section] = true
		}
	}
	return access
}

func (s session) can(permission string) bool {
	return s.Role == RoleOwner || slices.Contains(s.Permissions, permission)
}

func (s session) canReadConfiguration(registry *commonconfig.Registry) bool {
	for _, entry := range registry.Entries() {
		if s.configurationAccess(entry.Name()).Any(entry.Schema()) {
			return true
		}
	}
	return false
}
