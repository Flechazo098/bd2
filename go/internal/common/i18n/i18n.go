// Package i18n resolves translated text the way the plugin SDK's Component
// does: flat per-locale JSON files of key → text, looked up in the requested
// locale, then en_us, then the key itself. Arguments use fmt verbs.
package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
)

// Locales are the game's text languages, in the SDK's order.
var Locales = []string{"en_us", "ko_kr", "ja_jp", "zh_tw", "zh_cn"}

// Fallback is consulted when the requested locale lacks a key.
const Fallback = "en_us"

// Supported reports whether locale is one of Locales.
func Supported(locale string) bool { return slices.Contains(Locales, locale) }

// FromGameLanguage maps Define_LanguageType (KOREA=1, ENGLISH=2, JAPAN=3,
// TAIWAN=4, CHINA=5) to a locale; anything else falls back to en_us.
func FromGameLanguage(language uint64) string {
	switch language {
	case 1:
		return "ko_kr"
	case 3:
		return "ja_jp"
	case 4:
		return "zh_tw"
	case 5:
		return "zh_cn"
	}
	return Fallback
}

// Text is one string in several locales.
type Text map[string]string

// In picks locale, then en_us, then any available translation.
func (t Text) In(locale string) string {
	if s, ok := t[locale]; ok {
		return s
	}
	if s, ok := t[Fallback]; ok {
		return s
	}
	for _, l := range Locales {
		if s, ok := t[l]; ok {
			return s
		}
	}
	return ""
}

// Catalog holds the translations of one component.
type Catalog struct {
	entries map[string]map[string]string
}

// Load reads <dir>/<locale>.json for every supported locale present in fsys.
func Load(fsys fs.FS, dir string) (*Catalog, error) {
	c := &Catalog{entries: map[string]map[string]string{}}
	for _, locale := range Locales {
		raw, err := fs.ReadFile(fsys, path.Join(dir, locale+".json"))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("i18n: read %s/%s.json: %w", dir, locale, err)
		}
		var entries map[string]string
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("i18n: %s/%s.json: %w", dir, locale, err)
		}
		c.entries[locale] = entries
	}
	if c.entries[Fallback] == nil {
		return nil, fmt.Errorf("i18n: %s has no %s.json", dir, Fallback)
	}
	return c, nil
}

// Translate resolves a key and formats arguments with fmt.Sprintf.
func (c *Catalog) Translate(locale, key string, args ...any) string {
	text, ok := c.entries[locale][key]
	if !ok {
		if text, ok = c.entries[Fallback][key]; !ok {
			text = key
		}
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

// All translates key into every locale the catalog has.
func (c *Catalog) All(key string, args ...any) Text {
	out := Text{}
	for locale := range c.entries {
		out[locale] = c.Translate(locale, key, args...)
	}
	return out
}
