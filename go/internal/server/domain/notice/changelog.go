package notice

import (
	"bd2server/internal/common/release"
)

// SourceChangelog owns the release notes published from CHANGELOG.md.
const SourceChangelog = "changelog"

var noticeTypes = map[string]Type{
	release.NoticeNotice: TypeNotice, release.NoticeEvent: TypeEvent, release.NoticeUpdate: TypeUpdate,
	release.NoticeEmergency: TypeEmergency, release.NoticeDeveloperNote: TypeDeveloperNote,
}

// FromChangelog selects the entries this server should show: server releases
// up to the running one, and client releases for the same game version, since
// players of this server can only run those.
func FromChangelog(entries []release.Entry, serverVersion string) []Notice {
	running, err := release.ParseVersion(serverVersion)
	if err != nil {
		return nil
	}
	var out []Notice
	for _, e := range entries {
		v, err := release.ParseVersion(e.Version)
		if err != nil {
			continue
		}
		if e.Component == release.ComponentServer && v.Compare(running) > 0 {
			continue
		}
		if e.Component == release.ComponentClient && v.GameVersion() != running.GameVersion() {
			continue
		}
		n := Notice{ID: e.NoticeID(), Type: noticeTypes[e.Type], Thumbnail: e.Thumbnail, Start: e.StartsAt(), End: e.EndsAt(), URL: e.URL, Pinned: e.Pinned, Text: map[string]Content{}}
		for _, locale := range e.Locales() {
			content := Content{Title: e.Text[locale].Title}
			for _, block := range e.Blocks(locale) {
				content.Blocks = append(content.Blocks, Block{Image: block.Image, Value: block.Value})
			}
			n.Text[locale] = content
		}
		out = append(out, n)
	}
	return out
}
