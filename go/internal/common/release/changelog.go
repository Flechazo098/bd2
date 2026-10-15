package release

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bd2server/internal/common/i18n"
)

// Changelog components.
const (
	ComponentServer = "server"
	ComponentClient = "client"
)

// Notice types, named after the client's Define_NoticeType values.
const (
	NoticeNotice        = "notice"
	NoticeEvent         = "event"
	NoticeUpdate        = "update"
	NoticeEmergency     = "emergency"
	NoticeDeveloperNote = "developer_note"
)

// ChangelogZone dates changelog entries; a notice starts at local midnight.
var ChangelogZone = time.FixedZone("UTC+8", 8*60*60)

// Entry is one CHANGELOG.md release section. It carries everything both the
// in-game notice and the release manifest need, so a release is described
// once, in every language it is translated into.
type Entry struct {
	Component string
	Version   string
	Date      time.Time
	Type      string
	Pinned    bool
	Days      int
	URL       string
	Thumbnail string
	Migration string
	Backup    bool
	// Text is keyed by locale; en_us is always present.
	Text map[string]Localized
}

// Localized is one language's title and markdown body.
type Localized struct {
	Title string
	Body  string
}

// Block is one notice paragraph: TextMeshPro rich text or an image URL.
type Block struct {
	Image bool
	Value string
}

var (
	headingPattern  = regexp.MustCompile(`^##\s+(\S+)\s+(\S+)\s+[·•\-–—]\s+(\d{4}-\d{2}-\d{2})\s*$`)
	languagePattern = regexp.MustCompile(`^###\s+([a-z]{2}_[a-z]{2})\s*[:：]\s*(.*?)\s*$`)
	metaPattern     = regexp.MustCompile(`^>\s*([^:：]+?)\s*[:：]\s*(.*?)\s*$`)
	imagePattern    = regexp.MustCompile(`^!\[[^\]]*\]\((\S+?)\)$`)
	linkPattern     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	boldPattern     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	codePattern     = regexp.MustCompile("`([^`]+)`")
)

var componentNames = map[string]string{"服务端": ComponentServer, "server": ComponentServer, "客户端": ComponentClient, "client": ComponentClient}

var noticeTypes = map[string]string{
	"更新": NoticeUpdate, "公告": NoticeNotice, "活动": NoticeEvent, "紧急": NoticeEmergency, "开发者笔记": NoticeDeveloperNote,
	NoticeUpdate: NoticeUpdate, NoticeNotice: NoticeNotice, NoticeEvent: NoticeEvent, NoticeEmergency: NoticeEmergency, NoticeDeveloperNote: NoticeDeveloperNote,
}

var migrationKinds = map[string]string{
	"无": MigrationNone, "可回滚": MigrationReversible, "不可回滚": MigrationIrreversible,
	MigrationNone: MigrationNone, MigrationReversible: MigrationReversible, MigrationIrreversible: MigrationIrreversible,
}

// defaultTitles name an untitled language block: "Server 0.3.0 update".
var defaultTitles = map[string]map[string]string{
	ComponentServer: {"en_us": "Server %s update", "ko_kr": "서버 %s 업데이트", "ja_jp": "サーバー %s アップデート", "zh_tw": "伺服器 %s 更新", "zh_cn": "服务端 %s 更新"},
	ComponentClient: {"en_us": "Client %s update", "ko_kr": "클라이언트 %s 업데이트", "ja_jp": "クライアント %s アップデート", "zh_tw": "用戶端 %s 更新", "zh_cn": "客户端 %s 更新"},
}

type changelogParser struct {
	entries []Entry
	current *Entry
	locale  string
	body    []string
	inMeta  bool
	// fenced is set inside a ``` code block, whose lines are never headings.
	fenced bool
}

// ParseChangelog reads every release section. Text before the first section
// heading is the file's own introduction and is ignored.
func ParseChangelog(raw []byte) ([]Entry, error) {
	p := &changelogParser{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		if err := p.line(strings.TrimRight(scanner.Text(), " \t\r")); err != nil {
			return nil, fmt.Errorf("changelog line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := p.finishEntry(); err != nil {
		return nil, fmt.Errorf("changelog: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range p.entries {
		key := e.Component + " " + e.Version
		if seen[key] {
			return nil, fmt.Errorf("changelog: %s appears twice", key)
		}
		seen[key] = true
	}
	return p.entries, nil
}

func (p *changelogParser) line(text string) error {
	if strings.HasPrefix(strings.TrimSpace(text), "```") {
		p.fenced = !p.fenced
	} else if p.fenced {
		if p.locale != "" {
			p.body = append(p.body, text)
		}
		return nil
	}
	if strings.HasPrefix(text, "## ") {
		if err := p.finishEntry(); err != nil {
			return err
		}
		entry, err := parseHeading(text)
		if err != nil {
			return err
		}
		p.current, p.locale, p.body, p.inMeta = &entry, "", nil, true
		return nil
	}
	if p.current == nil {
		return nil
	}
	if match := languagePattern.FindStringSubmatch(text); match != nil {
		if err := p.finishLanguage(); err != nil {
			return err
		}
		if !i18n.Supported(match[1]) {
			return fmt.Errorf("unsupported language %q; use %s", match[1], strings.Join(i18n.Locales, ", "))
		}
		if _, exists := p.current.Text[match[1]]; exists {
			return fmt.Errorf("%s %s has two %s blocks", p.current.Component, p.current.Version, match[1])
		}
		p.locale, p.inMeta = match[1], false
		p.current.Text[p.locale] = Localized{Title: match[2]}
		return nil
	}
	if p.inMeta {
		if text == "" {
			return nil
		}
		if match := metaPattern.FindStringSubmatch(text); match != nil {
			return p.current.setMeta(match[1], match[2])
		}
		p.inMeta = false
	}
	if p.locale == "" {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return fmt.Errorf("%s %s: text must follow a \"### <locale>: <title>\" line", p.current.Component, p.current.Version)
	}
	if t := strings.TrimSpace(text); t == "-" || t == "*" {
		return fmt.Errorf("%s %s still has an empty template item", p.current.Component, p.current.Version)
	}
	p.body = append(p.body, text)
	return nil
}

func (p *changelogParser) finishLanguage() error {
	if p.locale == "" {
		return nil
	}
	text := p.current.Text[p.locale]
	text.Body = strings.TrimSpace(strings.Join(p.body, "\n"))
	if text.Body == "" {
		return fmt.Errorf("%s %s %s has no content", p.current.Component, p.current.Version, p.locale)
	}
	if text.Title == "" {
		v, _ := ParseVersion(p.current.Version)
		text.Title = fmt.Sprintf(defaultTitles[p.current.Component][p.locale], v.Short())
	}
	p.current.Text[p.locale] = text
	p.locale, p.body = "", nil
	return nil
}

func (p *changelogParser) finishEntry() error {
	if p.current == nil {
		return nil
	}
	if err := p.finishLanguage(); err != nil {
		return err
	}
	if _, ok := p.current.Text[i18n.Fallback]; !ok {
		return fmt.Errorf("%s %s needs an %s block, the fallback for other languages", p.current.Component, p.current.Version, i18n.Fallback)
	}
	p.entries = append(p.entries, *p.current)
	p.current = nil
	return nil
}

func parseHeading(text string) (Entry, error) {
	match := headingPattern.FindStringSubmatch(text)
	if match == nil {
		return Entry{}, fmt.Errorf("release heading must be \"## server|client <version> · YYYY-MM-DD\": %q", text)
	}
	component, ok := componentNames[strings.ToLower(match[1])]
	if !ok {
		return Entry{}, fmt.Errorf("unknown component %q; use server or client", match[1])
	}
	v, err := ParseVersion(match[2])
	if err != nil {
		return Entry{}, err
	}
	if v.Kind != component {
		return Entry{}, fmt.Errorf("%s needs a %s release version, not %q", match[1], component, match[2])
	}
	date, err := time.ParseInLocation("2006-01-02", match[3], ChangelogZone)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Component: component, Version: match[2], Date: date, Type: NoticeUpdate, Days: 30, Migration: MigrationNone, Text: map[string]Localized{}}, nil
}

func (e *Entry) setMeta(key, value string) error {
	switch key {
	case "类型", "type":
		kind, ok := noticeTypes[value]
		if !ok {
			return fmt.Errorf("unknown notice type %q; use update, notice, event, emergency or developer_note", value)
		}
		e.Type = kind
	case "置顶", "pin":
		flag, err := parseFlag(value)
		if err != nil {
			return err
		}
		e.Pinned = flag
	case "展示天数", "days":
		days, err := strconv.Atoi(value)
		if err != nil || days < 1 || days > 365 {
			return fmt.Errorf("days must be 1-365, got %q", value)
		}
		e.Days = days
	case "链接", "url":
		e.URL = value
	case "缩略图", "thumbnail":
		e.Thumbnail = value
	case "迁移", "migration":
		kind, ok := migrationKinds[value]
		if !ok {
			return fmt.Errorf("unknown migration %q; use none, reversible or irreversible", value)
		}
		e.Migration = kind
		if kind != MigrationNone {
			e.Backup = true
		}
	case "备份", "backup":
		flag, err := parseFlag(value)
		if err != nil {
			return err
		}
		e.Backup = flag
	default:
		return fmt.Errorf("unknown changelog field %q", key)
	}
	return nil
}

func parseFlag(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "是", "yes", "true":
		return true, nil
	case "否", "no", "false":
		return false, nil
	}
	return false, fmt.Errorf("expected yes or no, got %q", value)
}

// StartsAt and EndsAt bound when the entry is shown as an in-game notice.
func (e Entry) StartsAt() time.Time { return e.Date }
func (e Entry) EndsAt() time.Time   { return e.Date.AddDate(0, 0, e.Days) }

// NoticeID is stable for a component version, so the client's read marks
// survive restarts and changelog refreshes.
func (e Entry) NoticeID() int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(e.Component + " " + e.Version))
	return int32(h.Sum32()&0x3fffffff) + 1
}

// Locales lists the languages the entry is written in.
func (e Entry) Locales() []string {
	var out []string
	for _, l := range i18n.Locales {
		if _, ok := e.Text[l]; ok {
			out = append(out, l)
		}
	}
	return out
}

// Titles returns the title in every written language.
func (e Entry) Titles() i18n.Text {
	out := i18n.Text{}
	for locale, text := range e.Text {
		out[locale] = text.Title
	}
	return out
}

// Blocks renders one language's body for the client's TextMeshPro notice
// view: each paragraph becomes one text block and every standalone image
// line one image.
func (e Entry) Blocks(locale string) []Block {
	var blocks []Block
	var paragraph []string
	flush := func() {
		if len(paragraph) != 0 {
			blocks = append(blocks, Block{Value: strings.Join(paragraph, "\n")})
			paragraph = nil
		}
	}
	for line := range strings.SplitSeq(e.Text[locale].Body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case imagePattern.MatchString(trimmed):
			flush()
			blocks = append(blocks, Block{Image: true, Value: imagePattern.FindStringSubmatch(trimmed)[1]})
		case strings.HasPrefix(trimmed, "#"):
			flush()
			paragraph = append(paragraph, "<b>"+inline(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))+"</b>")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			paragraph = append(paragraph, "• "+inline(trimmed[2:]))
		default:
			paragraph = append(paragraph, inline(trimmed))
		}
	}
	flush()
	return blocks
}

func inline(text string) string {
	text = linkPattern.ReplaceAllString(text, "$1")
	text = boldPattern.ReplaceAllString(text, "<b>$1</b>")
	return codePattern.ReplaceAllString(text, "$1")
}

// Notes is the body as plain text in every written language, for the
// launcher and the console.
func (e Entry) Notes() i18n.Text {
	out := i18n.Text{}
	for _, locale := range e.Locales() {
		var lines []string
		for _, block := range e.Blocks(locale) {
			if !block.Image {
				lines = append(lines, strings.NewReplacer("<b>", "", "</b>", "").Replace(block.Value))
			}
		}
		out[locale] = strings.Join(lines, "\n\n")
	}
	return out
}

// Find returns the entry for component and version.
func Find(entries []Entry, component, version string) (Entry, bool) {
	for _, e := range entries {
		if e.Component == component && e.Version == version {
			return e, true
		}
	}
	return Entry{}, false
}

// ChangelogTemplate is the section `bd2w release changelog` inserts: an
// English block (required) and a Simplified Chinese block; other languages
// may be added and fall back to English when absent.
func ChangelogTemplate(component, version string, date time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s %s · %s\n\n", component, version, date.In(ChangelogZone).Format("2006-01-02"))
	b.WriteString("> type: update\n")
	if component == ComponentServer {
		b.WriteString("> migration: none\n")
	}
	v, _ := ParseVersion(version)
	sections := map[string][3]string{"en_us": {"Added", "Changed", "Fixed"}, "zh_cn": {"新增", "调整", "修复"}}
	for _, locale := range []string{"en_us", "zh_cn"} {
		fmt.Fprintf(&b, "\n### %s: %s\n", locale, fmt.Sprintf(defaultTitles[component][locale], v.Short()))
		for _, heading := range sections[locale] {
			fmt.Fprintf(&b, "\n#### %s\n\n- \n", heading)
		}
	}
	return b.String()
}
