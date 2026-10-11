# 服务端游戏规则配置

`game.json` 是服务端统一的游戏规则配置入口。后续可配置的游戏规则在此按功能分组扩展；认证、资源源、版本清单仍分别使用现有配置文件。

发布包不包含 `game.json`，首次运行时在 `bd2server.exe`（Linux/macOS 为 `bd2server`）旁生成默认文件。开发入口在 `.build/config/game.json` 生成并读取配置。已有文件不会覆盖，可修改的规则在重启后生效，起始章节只在首次初始化时选择。客户端无需配置此文件。

默认内容：

```json
{
  "schema_version": 1,
  "story": {
    "start_pack_id": 21
  },
  "gacha": {
    "include_collaboration_ur_weapons": false
  },
  "purchases": {
    "diamond_recharge": {
      "currency": "free",
      "gold_per_paid_diamond": 1000,
      "diamonds_per_paid_diamond": 1
    }
  },
  "text_moderation": {
    "rule_files": [],
    "provider": {
      "type": "off"
    }
  }
}
```

## 实时联机入口

共享存档、PVP 防守快照、公会和排行榜由服务端管理。动作小游戏、联机钓鱼与随机防御另外使用实时连接，需要时在 `game.json` 添加 `realtime` 分组：

```json
"realtime": {
  "enabled": true,
  "tcp_listen": "0.0.0.0:8444",
  "udp_listen": "0.0.0.0:8444",
  "public_host": "game.example.com",
  "tcp_port": 8444,
  "udp_port": 8444,
  "tls_cert_file": "/etc/bd2/tls/server.crt",
  "tls_key_file": "/etc/bd2/tls/server.key",
  "development_plain_loopback": false
}
```

省略此分组时不会监听实时端口，会对玩家体验造成影响。`public_host` 填玩家能够访问的主机名，不带协议、路径或端口。公开端口可以与监听端口不同，适用于端口映射。需要分别转发 TCP 和 UDP，普通 HTTP 反向代理不能直接转发这些原生连接。原有登录与游戏 HTTP(S) 入口继续使用认证配置中的地址。

TCP 使用与 `public_host` 匹配且受客户端信任的 TLS 证书。证书路径相对于 `game.json` 所在目录解析，也可以填写绝对路径。私钥只留在服务端，须允许服务进程读取。证书与私钥更新后，新握手自动载入有效的新证书；文件尚未替换完整时继续使用上一次有效证书。仅限本机开发时可以将 `development_plain_loopback` 设为 `true`，此时监听地址和公开地址都必须是回环 IP，实际生产环境不必理会。

玩家安装登录插件后，客户端通过已有登录会话取得短期、一次性连接票据，无需额外填写实时地址。动作同步可使用经过身份验证和防重放校验的 UDP；玩家明确配置 HTTP 代理时，实时连接通过代理的 CONNECT 隧道并使用 TCP。要让 UDP 经过代理，需要操作系统层面的 TUN 路由。

服务器重启不会保存战斗或房间的中间状态。已经提交的资产、成绩与领奖状态保留。客户端收到实时玩法完成通知前，服务端会先保存最终结果。尚未完成的房间需要重新进入。不要单独恢复某个数据库，备份与恢复应同时涵盖整个 `data/state`，包括账号数据库、认证数据库、共享关系、成绩和待投递事件。

## 联动 UR 专武

`gacha.include_collaboration_ur_weapons` 必须是 JSON 布尔值：

| 值 | UR 必得装备券的结果 |
|---|---|
| `false`（默认） | 使用当前 GameData 的原始 UR 必得装备券池。 |
| `true` | 在原池中加入 14 种联动角色 UR 专武（装备 ID `943200` 至 `943213`）。 |

开关仅作用于 UR 必得装备券。新增装备沿用同星级原有装备的单件权重，原有星级权重规则保持不变；加入候选后，同星级内的概率按扩展后的权重总和计算。该设置只改变券池候选，不会直接发放装备。专武绑定角色，同一角色的多个服装共用专武。

## 服务器的起始章节

`story.start_pack_id` 接受整数 `21` 或 `1`，省略时默认 `21`。

| 值 | 本服玩家的开局 |
|---|---|
| `21`（默认） | 从当前客户端默认的店长篇开始，使用该篇的任务和教学流程。 |
| `1` | 从旧主线第一章开始，使用当前 GameData 中该章的开场剧情、初始任务及购卡奖励。 |

请在服务器首次启动前确定此值。第一次成功初始化时，服务端会在 SQLite 中持久保存服务器的起始章节；此后配置必须与保存值一致。改成另一个章节会拒绝启动，并在日志中明确提示原值与配置值，不会覆盖原设置或玩家进度。首次启动失败并回滚时不会锁定选择，可以修正配置后重试。

这项限制只针对 `story.start_pack_id`；其他游戏规则仍可修改。升级和恢复服务器时应一起保留配置与数据库，不能通过删除数据库来切换开局，这会删除玩家存档。

后续官方若将旧主线与店长篇重构合并为一条剧情线，再重新评估此限制与迁移方案。重构发布日期尚未确认。

## 现金商品与付费钻石充值

所有原本走平台现金支付的普通商品，包括礼包、皮肤、通行证与付费抽卡商品，都固定消耗付费钻石。这类商品没有免费购买配置开关；`purchases.diamond_recharge` 仅设置付费钻石充值的兑换方式。原本已使用游戏内货币的商品仍走原来的游戏内购买流程。

普通现金商品的价格由当前版本 GameData 计算：取最小常规充值档位的现金标价与付费钻石数量作为换算基准，将商品现金标价换算为付费钻石并向上取整。当前最小档位为现金标价 `2200`、常规付费钻石 `100`，因此普通商品价格为 `ceil(现金标价 / 22)`。运行时读取 GameData 的实际档位，不把该比值固定写入代码。首次充值的加倍奖励不参与换算基准，也不增加充值兑换费用。

`purchases.diamond_recharge.currency` 接受下列值：

| 值 | 付费钻石充值行为 |
|---|---|
| `"free"`（默认） | 不扣货币，发放所选充值商品的原始奖励。 |
| `"gold"` | 每单位常规付费钻石消耗 `gold_per_paid_diamond` 金币。 |
| `"diamonds"` | 每单位常规付费钻石消耗 `diamonds_per_paid_diamond` 普通钻石。 |
| `"ban"` 或 `""` | 禁止购买付费钻石充值商品。 |

省略该配置分组或 `currency` 时使用 `"free"`；显式写空字符串 `""` 会禁止充值。`null` 仍属于无效配置。兑换倍率必须是 `1` 到 `1000000000` 之间的整数，默认分别为 `1000` 金币与 `1` 普通钻石。即使选中其他兑换方式，两个倍率也必须合法。配置导致任何实际商品单次费用超出游戏钱包支持的整数范围时，服务端拒绝加载该商品目录；多件购买超过范围时拒绝该次购买。

例如，使用金币兑换充值：

```json
"purchases": {
  "diamond_recharge": {
    "currency": "gold",
    "gold_per_paid_diamond": 1000,
    "diamonds_per_paid_diamond": 1
  }
}
```

充值的兑换费用按常规充值数量计算，实际发放仍遵守所选商品的奖励和首购限制。因此常规数量为 `100` 的首购加倍商品在默认金币倍率下扣 `100000` 金币，保留原来的加倍奖励；后续常规商品扣相同费用。禁止充值不会免费化其他现金商品，也不会删除玩家已有的付费钻石。

客户端从服务端 `/client/commerce` 获取商品价格目录。商品由 `group_id`、`product_id`、`sale_group` 三元组识别，Google 与 Apple 平台商品编号均作为别名返回；同一平台编号对应首购与常规变体时仍保留不同商品键。显示价格和购买校验使用同一服务端规则。

## 文本审核

输入框的内容审核由服务端决定。客户端插件关闭官方词库的拒绝与文字替换，保留长度、字符和空值校验。已有昵称和简介不会在登录时重新审核。

默认 `rule_files` 为空、第三方 `provider.type` 为 `"off"`，不会进行内容拦截。服主自行寻找、检查并维护词库，再将文件加入配置。所有文件在服务端启动时加载，修改后重启生效。相对路径以 `game.json` 所在目录为基准；绝对路径使用服务器实际路径。发布包不附带词库。

例如，加载两份本地规则：

```json
"text_moderation": {
  "rule_files": ["moderation/words.txt", "moderation/names.json"],
  "provider": {
    "type": "off"
  }
}
```

文件须为 UTF-8，可带 BOM，单文件最多 4 MiB。`.txt` 每行一个词，忽略空行和以 `#` 开头的整行注释；其规则为子串匹配，作用于所有已接入的文本入口。下载的其他词库格式须先转换。英文短词容易命中正常名字的一部分，建议使用下面的 JSON 精确匹配或完整单词匹配。

`.json` 文件支持逐条指定匹配方式、作用范围及完整文本例外：

```json
{
  "rules": [
    {
      "text": "reservedname",
      "match": "exact",
      "scopes": ["nickname"],
      "exceptions": []
    },
    {
      "text": "sampleword",
      "match": "word",
      "scopes": ["nickname", "greeting"],
      "exceptions": ["SamplewordHero"]
    }
  ]
}
```

以上词语仅演示格式，不是随包提供的屏蔽词库。

| 字段 | 含义 |
|---|---|
| `match: "exact"` | 整段文本相同才命中。 |
| `match: "word"` | 两侧须为文本边界或非单词字符。Unicode 字母、数字、组合标记和下划线均视为单词字符；不提供中文分词。 |
| `match: "substring"` | 文本中包含指定词语即命中。 |
| `scopes` | 当前接入范围见下表；本地规则省略或空数组作用于所有已接入入口。第三方 provider 的 `scopes` 必须明确列出需要审核的范围，已有显式列表不会自动扩展。写入其他范围名本身不会实现对应玩法。 |
| `exceptions` | 整段文本匹配才豁免当前这一条规则；仍检查其他规则及第三方审核。 |

| 范围 | 输入入口 |
|---|---|
| `nickname` | 玩家昵称 |
| `greeting` | 个人简介 |
| `master_title` | 主角自定义称呼 |
| `preset_name` | 普通配队预设名称 |
| `equipment_preset_name` | 装备预设名称 |
| `monster_hunt_preset_name` | 魔兽追踪者预设名称 |
| `guild_raid_preset_name` | 公会战预设名称 |
| `colosseum_preset_name` | 斗兽场预设名称 |
| `equipment_mark` | 装备标记中的自定义字符 |
| `guild_name` | 公会名称 |
| `guild_message` | 公会介绍 |
| `guild_notice` | 公会公告 |
| `chat` | 联机钓鱼大厅与房间自由文本 |

新生成配置的第三方范围列表包含以上入口。允许空名称或默认名称的玩法保留原有协议行为；图标编号和颜色值不作为文字审核。已成功提交的请求重放返回原结果，第三方审核在新请求的玩家写事务之前完成。

匹配时统一进行 Unicode NFKC 规范化和大小写折叠，保存、显示及发送给第三方的仍是原文。不会自动删除空格或标点，也不会自动替换数字、拼音或相似字符。规则编译为进程共享的多模式匹配结构，普通玩法请求不触发文本审核。

### 第三方接入

本地规则先执行，通过后才调用所配置范围的第三方。直接支持 Azure AI Content Safety，同时提供通用 HTTP JSON 协议，供服主接入自己的审核服务或第三方适配网关。腾讯、阿里等供应商须通过网关处理各自的鉴权和响应转换，当前没有内置它们的原生适配器。

通用接口配置：

```json
"text_moderation": {
  "rule_files": ["moderation/names.json"],
  "provider": {
    "type": "http_json",
    "endpoint": "https://moderation.example.com/check",
    "api_key_env": "BD2_TEXT_MODERATION_KEY",
    "api_key_header": "Authorization",
    "scopes": ["greeting"],
    "timeout_ms": 1500,
    "failure_policy": "deny"
  }
}
```

密钥由 `api_key_env` 指定的服务端环境变量读取。`Authorization` 头会使用 `Bearer <密钥>`；其他头名原样传递密钥。不需要鉴权的自建接口可省略 `api_key_env`。审核地址使用 HTTPS，只有回环地址允许 HTTP，不跟随重定向。

服务端以 POST 发送原文、范围和稳定账号标识：

```json
{
  "scope": "greeting",
  "account_id": "stable-account-id",
  "text": "玩家输入的原文"
}
```

接口须返回 HTTP 200 和一个 JSON 对象，允许时为 `{"decision":"allow"}`，拒绝时为 `{"decision":"reject"}`。其他状态码、字段、缺失判定、无效 JSON 或超时均按接口故障处理；响应最多 64 KiB。服务端日志不记录原文、密钥或第三方响应体。

Azure 配置示例：

```json
"provider": {
  "type": "azure_content_safety",
  "endpoint": "https://your-resource.cognitiveservices.azure.com",
  "api_key_env": "BD2_AZURE_CONTENT_SAFETY_KEY",
  "scopes": ["greeting"],
  "timeout_ms": 1500,
  "failure_policy": "deny",
  "severity_threshold": 4
}
```

Azure 使用资源根地址，自动调用 `2024-09-01` 版 Analyze Text API，以八级严重程度检查 Hate、Sexual、SelfHarm 和 Violence。任一类别达到 `severity_threshold` 就拒绝；阈值范围为 `0..7`，默认 `4`，设为 `0` 会连严重程度为 `0` 的结果也拒绝。这些通用类别不能代替本服的广告、冒名等自定义规则。接口规范见 [Azure Analyze Text 官方文档](https://learn.microsoft.com/en-us/rest/api/contentsafety/text-operations/analyze-text?view=rest-contentsafety-2024-09-01)。

`timeout_ms` 默认 `1500`，允许 `10..30000`。`failure_policy` 默认 `"deny"`：接口故障时暂不接受修改，客户端提示审核暂时不可用；`"allow"` 则在接口故障时放行已通过本地规则的文本。第三方明确拒绝以及本地规则拒绝都不会被此设置放行。被拒绝或暂时无法审核的改名不扣券、不保存名字，个人简介也不写入；玩家可保持登录并重试。

## 校验与升级

`schema_version` 当前必须为 `1`。字段拼写错误、未知字段、错误类型（例如字符串 `"true"`）、`null`、格式错误或文件末尾多出的 JSON 都会使服务端拒绝启动，并在错误中给出文件路径。省略 `gacha` 或此布尔开关时使用 `false`。

缺少文件时服务端会生成默认配置；已有文件保留，格式错误会明确报错。升级时保留服主修改过的配置，新增默认字段可参考本指南。Docker 镜像的默认命令在可写数据目录 `/app/data/config/` 生成和读取三个配置；自定义命令或只读挂载时通过 `--game-config` 指定路径，重启后生效。
