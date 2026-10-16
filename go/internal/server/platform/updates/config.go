// Package updates keeps the server informed about new releases and applies
// one when an operator asks: it checks the signed release manifest at a low
// rate, can pre-download and verify the server package, and switches versions
// only after warning players, closing logins, draining requests and saving.
// It never replaces code inside the running process; the next process start
// runs the new version and rolls the program back if that start fails.
package updates

import (
	"embed"
	"strings"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/i18n"
	"bd2server/internal/common/release"
)

const FileName = "updates.json"

// Install methods.
const (
	MethodNative = "native"
	MethodDocker = "docker"
	MethodNone   = "none"
)

type Config struct {
	Check              bool           `json:"check"`
	CheckInterval      string         `json:"check_interval"`
	Download           bool           `json:"download"`
	Method             string         `json:"method"`
	CountdownMinutes   int            `json:"countdown_minutes"`
	MaintenanceMinutes int            `json:"maintenance_minutes"`
	ManifestURL        string         `json:"manifest_url,omitempty"`
	ChangelogURLs      []string       `json:"changelog_urls,omitempty"`
	WebhookURL         string         `json:"webhook_url,omitempty"`
	Changelog          ChangelogFlags `json:"changelog"`
}

type ChangelogFlags struct {
	Notices bool `json:"notices"`
}

func Default() Config {
	return Config{
		Check: true, CheckInterval: "6h", Download: true, Method: MethodNative,
		CountdownMinutes: 10, MaintenanceMinutes: 5,

		Changelog: ChangelogFlags{Notices: true},
	}
}

var countdownMin, countdownMax = commonconfig.Range(0, 1440)
var downtimeMin, downtimeMax = commonconfig.Range(1, 1440)

var Document = commonconfig.Document[Config]{
	Name:     "updates",
	FileName: FileName,
	Defaults: Default,
	Decode:   commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true},
	Schema: commonconfig.Schema{
		Title:   "版本更新",
		Summary: "检查新版、预下载与维护更新方式。检查与下载不影响游戏；更新需要在管理台或命令行确认。修改后重启服务端生效。",
		Sections: []commonconfig.Section{
			{ID: "check", Title: "检查与下载", Fields: []commonconfig.Field{
				{Path: "check", Kind: commonconfig.KindBool, Label: "自动检查新版", Help: "启动后及运行期间按间隔检查发布清单，并加随机偏移。"},
				{Path: "check_interval", Kind: commonconfig.KindString, Label: "检查间隔", Format: "duration", Placeholder: "6h"},
				{Path: "download", Kind: commonconfig.KindBool, Label: "提前下载", Help: "发现新版后在后台下载并校验，应用更新时不必再等待。"},
				{Path: "manifest_url", Kind: commonconfig.KindString, Label: "发布清单地址", Format: "url", Advanced: true, Help: "留空使用内置地址；清单必须由内置公钥签名。"},
				{Path: "changelog_urls", Kind: commonconfig.KindList, Label: "更新日志地址", Format: "url", Advanced: true, Help: "按顺序尝试；留空使用内置的 GitHub 与 Gitea 地址。"},
				{Path: "changelog.notices", Kind: commonconfig.KindBool, Label: "更新日志作为游戏公告"},
			}},
			{ID: "install", Title: "安装方式", Fields: []commonconfig.Field{
				{Path: "method", Kind: commonconfig.KindString, Label: "部署方式", Options: []commonconfig.Option{
					{Value: MethodNative, Label: "原生（systemd）", Help: "替换程序文件后退出，由 systemd 以新版重启；失败时自动换回上一版程序。"},
					{Value: MethodDocker, Label: "Docker", Help: "排空并保存后退出，由宿主机脚本切换到清单指定的镜像。"},
					{Value: MethodNone, Label: "仅通知", Help: "只检查和展示，不提供应用更新。"},
				}},
				{Path: "countdown_minutes", Kind: commonconfig.KindInteger, Label: "默认倒计时（分钟）", Min: countdownMin, Max: countdownMax,
					Help: "0 表示立即维护；否则至少 5 分钟，挂机玩家的客户端每 3 分钟才查询一次服务器。"},
				{Path: "maintenance_minutes", Kind: commonconfig.KindInteger, Label: "预计维护时长（分钟）", Min: downtimeMin, Max: downtimeMax},
				{Path: "webhook_url", Kind: commonconfig.KindString, Label: "Webhook 地址", Format: "url", Advanced: true, Help: "发现新版时向此地址 POST 一次 JSON；留空不发送。"},
			}},
		},
	},
	Rules: func(c Config) commonconfig.Problems {
		var problems commonconfig.Problems
		if interval, err := time.ParseDuration(c.CheckInterval); err != nil || interval < 10*time.Minute {
			problems = append(problems, commonconfig.Problem{Path: "check_interval", Rule: commonconfig.RuleFormat, Message: "检查间隔至少 10m，例如 6h"})
		}
		if c.CountdownMinutes > 0 && time.Duration(c.CountdownMinutes)*time.Minute < MinimumCountdown {
			problems = append(problems, commonconfig.Problem{Path: "countdown_minutes", Rule: commonconfig.RuleRange, Message: "倒计时为 0（立即维护）或至少 5 分钟"})
		}
		switch c.Method {
		case MethodNative, MethodDocker, MethodNone:
		default:
			problems = append(problems, commonconfig.Problem{Path: "method", Rule: commonconfig.RuleEnum, Message: "部署方式必须是 native、docker 或 none"})
		}
		for field, addresses := range map[string][]string{"manifest_url": {c.ManifestURL}, "changelog_urls": c.ChangelogURLs, "webhook_url": {c.WebhookURL}} {
			for _, address := range addresses {
				if address == "" && field != "changelog_urls" {
					continue
				}
				if _, err := release.Resolve(address, address); err != nil {
					problems = append(problems, commonconfig.Problem{Path: field, Rule: commonconfig.RuleFormat, Message: err.Error()})
				}
			}
		}
		return problems
	},
}

func (c Config) Interval() time.Duration {
	d, _ := time.ParseDuration(c.CheckInterval)
	return d
}

func (c Config) manifestURL() string {
	if c.ManifestURL != "" {
		return c.ManifestURL
	}
	return release.BuiltinChannel().ManifestURL
}

func (c Config) changelogURLs() []string {
	if len(c.ChangelogURLs) != 0 {
		return c.ChangelogURLs
	}
	return release.BuiltinChannel().ChangelogURLs
}

func (c Config) webhook() string {
	return strings.TrimSpace(c.WebhookURL)
}

//go:embed lang/*.json
var langFiles embed.FS

// texts holds the player-facing maintenance messages in every game language.
var texts = func() *i18n.Catalog {
	c, err := i18n.Load(langFiles, "lang")
	if err != nil {
		panic(err)
	}
	return c
}()

// maintenanceText translates key for a maintenance to version at at.
func maintenanceText(locale, key, version string, at time.Time, minutes int) string {
	return texts.Translate(locale, key, version, at.In(release.ChangelogZone).Format("01-02 15:04"), minutes)
}
