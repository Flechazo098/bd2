package gameconfig

import commonconfig "bd2server/internal/common/config"

var (
	rateMin, rateMax         = commonconfig.Range(1, 1_000_000_000)
	portMin, portMax         = commonconfig.Range(1, 65535)
	timeoutMin, timeoutMax   = commonconfig.Range(10, 30000)
	severityMin, severityMax = commonconfig.Range(0, 7)
)

var realtimeEnabled = &commonconfig.Condition{Path: "realtime.enabled", In: []any{true}}
var providerActive = &commonconfig.Condition{Path: "text_moderation.provider.type", In: []any{"off"}, Not: true}

var schema = commonconfig.Schema{
	Title:   "游戏规则",
	Summary: "服务端统一的玩法规则。修改后重启服务端生效。",
	Sections: []commonconfig.Section{
		{ID: "spine_recording", Title: "互动录制", Help: "保存玩家的互动立绘操作记录，并供原生客户端回放。修改后重启服务端生效。", Fields: []commonconfig.Field{
			{Path: "spine_recording.download_base_url", Kind: commonconfig.KindString, Label: "录制下载基础 URL", Format: "url", Placeholder: "https://game.example.com/client/spine-recordings",
				Help: "留空使用本服地址。自定义地址须反向代理到本服的 /client/spine-recordings/；系统会在其后追加录制路径和访问凭据。公网须使用 HTTPS。"},
		}},
		{ID: "story", Title: "开局章节", Help: "首次成功初始化后永久保存在服务器状态中，之后不能更改；不能通过删除数据库切换开局。", Fields: []commonconfig.Field{
			{Path: "story.start_pack_id", Kind: commonconfig.KindInteger, Label: "起始章节", Options: []commonconfig.Option{
				{Value: 21, Label: "店长篇", Help: "当前客户端默认的开局，使用该篇的任务和教学流程。"},
				{Value: 1, Label: "旧主线第一章", Help: "使用当前 GameData 中该章的开场剧情、初始任务及购卡奖励。"},
			}},
		}},
		{ID: "gacha", Title: "抽卡", Fields: []commonconfig.Field{
			{Path: "gacha.include_collaboration_ur_weapons", Kind: commonconfig.KindBool, Label: "UR 必得装备券加入联动专武",
				Help: "在原池中加入 14 种联动角色 UR 专武（装备 943200–943213），沿用同星级单件权重；只改变券池候选，不直接发放装备。"},
		}},
		{ID: "purchases", Title: "付费钻石充值", Help: "普通现金商品固定消耗付费钻石，价格由 GameData 换算；这里只设置付费钻石充值本身的兑换方式。", Fields: []commonconfig.Field{
			{Path: "purchases.diamond_recharge.currency", Kind: commonconfig.KindString, Label: "充值方式", Options: []commonconfig.Option{
				{Value: "free", Label: "免费", Help: "不扣货币，直接发放所选充值商品的原始奖励。"},
				{Value: "gold", Label: "金币兑换", Help: "每单位常规付费钻石消耗设定数量的金币。"},
				{Value: "diamonds", Label: "普通钻石兑换", Help: "每单位常规付费钻石消耗设定数量的普通钻石。"},
				{Value: "ban", Label: "禁止充值", Help: "禁止购买付费钻石充值商品，不影响玩家已有付费钻石。"},
				{Value: "", Label: "禁止充值（空字符串）", Help: "与 ban 相同的旧写法。"},
			}},
			{Path: "purchases.diamond_recharge.gold_per_paid_diamond", Kind: commonconfig.KindInteger, Label: "金币 / 付费钻石", Min: rateMin, Max: rateMax,
				Help: "金币兑换时每单位常规付费钻石的费用。即使未选用也必须合法。"},
			{Path: "purchases.diamond_recharge.diamonds_per_paid_diamond", Kind: commonconfig.KindInteger, Label: "普通钻石 / 付费钻石", Min: rateMin, Max: rateMax,
				Help: "普通钻石兑换时每单位常规付费钻石的费用。即使未选用也必须合法。"},
		}},
		{ID: "realtime", Title: "实时联机", Help: "动作小游戏、联机钓鱼与随机防御使用的原生 TCP/UDP 连接。关闭时不监听实时端口，会影响玩家体验。", Fields: []commonconfig.Field{
			{Path: "realtime.enabled", Kind: commonconfig.KindBool, Label: "启用实时联机"},
			{Path: "realtime.public_host", Kind: commonconfig.KindString, Label: "公开主机名", Format: "host", Placeholder: "game.example.com", When: realtimeEnabled,
				Help: "玩家能够访问的主机名，不带协议、路径或端口。"},
			{Path: "realtime.tcp_port", Kind: commonconfig.KindInteger, Label: "公开 TCP 端口", Min: portMin, Max: portMax, When: realtimeEnabled},
			{Path: "realtime.udp_port", Kind: commonconfig.KindInteger, Label: "公开 UDP 端口", Min: portMin, Max: portMax, When: realtimeEnabled},
			{Path: "realtime.tcp_listen", Kind: commonconfig.KindString, Label: "TCP 监听地址", Format: "listen", Placeholder: "0.0.0.0:8444", When: realtimeEnabled,
				Help: "公开端口可以与监听端口不同，适用于端口映射。"},
			{Path: "realtime.udp_listen", Kind: commonconfig.KindString, Label: "UDP 监听地址", Format: "listen", Placeholder: "0.0.0.0:8444", When: realtimeEnabled},
			{Path: "realtime.tls_cert_file", Kind: commonconfig.KindString, Label: "TLS 证书", Format: "path", When: realtimeEnabled,
				Help: "与公开主机名匹配且受客户端信任的证书。相对路径以 game.json 所在目录为基准；更新后新握手自动载入。"},
			{Path: "realtime.tls_key_file", Kind: commonconfig.KindString, Label: "TLS 私钥", Format: "path", When: realtimeEnabled,
				Help: "私钥只留在服务端，须允许服务进程读取。"},
			{Path: "realtime.development_plain_loopback", Kind: commonconfig.KindBool, Label: "本机开发明文模式", Advanced: true, When: realtimeEnabled,
				Help: "仅限本机开发：监听地址和公开地址都必须是回环 IP，此时不需要证书。"},
		}},
		{ID: "text_moderation", Title: "文本审核", Help: "本地规则先执行，通过后才调用第三方。已有昵称和简介不会在登录时重新审核。", Fields: []commonconfig.Field{
			{Path: "text_moderation.rule_files", Kind: commonconfig.KindList, Label: "本地词库文件", Format: "path",
				Help: "UTF-8 的 .txt（每行一个词，子串匹配）或 .json（逐条指定匹配方式与范围），单文件最多 4 MiB。相对路径以 game.json 所在目录为基准。"},
			{Path: "text_moderation.provider.type", Kind: commonconfig.KindString, Label: "第三方审核", Options: []commonconfig.Option{
				{Value: "off", Label: "关闭"},
				{Value: "http_json", Label: "通用 HTTP JSON", Help: "接入自建审核服务或第三方适配网关。"},
				{Value: "azure_content_safety", Label: "Azure AI Content Safety"},
			}},
			{Path: "text_moderation.provider.endpoint", Kind: commonconfig.KindString, Label: "审核地址", Format: "url", When: providerActive,
				Help: "使用 HTTPS，只有回环地址允许 HTTP，不跟随重定向。Azure 填资源根地址。"},
			{Path: "text_moderation.provider.api_key_env", Kind: commonconfig.KindString, Label: "密钥环境变量", Format: "env", When: providerActive,
				Help: "服务端从这个环境变量读取密钥，配置文件里不保存密钥本身。"},
			{Path: "text_moderation.provider.api_key_header", Kind: commonconfig.KindString, Label: "密钥请求头", Advanced: true,
				When: &commonconfig.Condition{Path: "text_moderation.provider.type", In: []any{"http_json"}},
				Help: "Authorization 头会发送 Bearer <密钥>，其他头名原样传递密钥。"},
			{Path: "text_moderation.provider.scopes", Kind: commonconfig.KindList, Label: "审核范围", Unique: true, OpenOptions: true,
				Help: "第三方只审核明确列出的输入入口。", Options: []commonconfig.Option{
					{Value: "nickname", Label: "玩家昵称"}, {Value: "greeting", Label: "个人简介"}, {Value: "master_title", Label: "主角称呼"},
					{Value: "preset_name", Label: "配队预设名"}, {Value: "equipment_preset_name", Label: "装备预设名"}, {Value: "equipment_mark", Label: "装备标记"},
					{Value: "monster_hunt_preset_name", Label: "魔兽追踪者预设名"}, {Value: "guild_raid_preset_name", Label: "公会战预设名"},
					{Value: "colosseum_preset_name", Label: "斗兽场预设名"}, {Value: "guild_name", Label: "公会名称"}, {Value: "guild_message", Label: "公会介绍"},
					{Value: "guild_notice", Label: "公会公告"}, {Value: "room_name", Label: "房间名称"}, {Value: "chat", Label: "联机聊天"},
				}},
			{Path: "text_moderation.provider.timeout_ms", Kind: commonconfig.KindInteger, Label: "超时（毫秒）", Min: timeoutMin, Max: timeoutMax, Advanced: true},
			{Path: "text_moderation.provider.failure_policy", Kind: commonconfig.KindString, Label: "接口故障时", Advanced: true, Options: []commonconfig.Option{
				{Value: "deny", Label: "暂不接受修改", Help: "客户端提示审核暂时不可用。"},
				{Value: "allow", Label: "放行", Help: "放行已通过本地规则的文本；明确拒绝的结果不会被放行。"},
			}},
			{Path: "text_moderation.provider.severity_threshold", Kind: commonconfig.KindInteger, Label: "Azure 拒绝阈值", Min: severityMin, Max: severityMax, Advanced: true,
				Help: "任一类别达到该严重程度即拒绝；0 表示连严重程度 0 的结果也拒绝。"},
		}},
		{ID: "format", Title: "格式", Fields: []commonconfig.Field{
			{Path: "schema_version", Kind: commonconfig.KindInteger, Label: "格式版本", Advanced: true, Options: []commonconfig.Option{{Value: 1, Label: "1"}}},
		}},
	},
}
