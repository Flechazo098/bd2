# 项目排期文件

服务端从发布目录中的固定 `schedules/` 目录读取全部 `.bd2schedule` 文件，完整校验后合并下发排期。文件采用项目自定义二进制格式；没有 JSON 排期入口或服主自定义配置功能。项目维护者可在游戏版本不变时单独发布排期文件，覆盖相应文件后重启服务端加载。文件中的游戏和 GameData 版本标签表示协议、静态设计兼容性，排期 `revision` 独立表示日历修订。

## 二进制格式版本 1

整数使用小端编码。文件头固定 46 字节：

| 偏移 | 长度 | 内容 |
| --- | --- | --- |
| 0 | 8 | 魔数 `BD2SCH` 后接两个零字节 |
| 8 | 2 | uint16 格式版本，当前为 1 |
| 10 | 4 | uint32 payload 字节数 |
| 14 | 32 | payload 的 SHA-256 原始摘要 |
| 46 | payload 长度 | 按固定顺序编码的记录 |

payload 首先编码 revision、game version、GameData version 三个字符串，再依次编码事件列表、抽卡列表、StepUp 列表、可选常规内容日历、可选魔物追踪日历、商品列表、活动 Hub 列表、小游戏 Hub 列表。

字符串编码为 uint32 字节长度及 UTF-8 字节；列表编码为 uint32 记录数量及逐条记录；可选记录先写 0/1 存在标记；布尔值只允许单字节 0 或 1；身份、数量等整数字段编码为 uint64。每类记录采用显式固定字段顺序，权威定义为 `go/internal/server/calendar/records.go`，不使用反射或嵌入 JSON。单文件最多 8 MiB，单字符串最多 64 KiB，记录总量最多 100000；错误魔数、版本、摘要、长度、布尔值及尾随内容都拒绝加载。

记录内时间采用 RFC3339 UTC 字符串并保留毫秒，以保留跨年份和永久窗口语义。永久事件起点 `1969-12-31T15:00:00.000Z` 对应协议有符号 int64 的 `-32400000` 毫秒。它不是无符号巨大日期。二进制校验用于发现文件损坏，不代表密码签名。

项目维护者在代码中构建具名 `calendar.Manifest` 并调用 `calendar.MarshalBinary` 生成发行文件；读取使用 `calendar.UnmarshalBinary`。项目不提供从公开 JSON 输入生成排期的服主管理命令。发布时保留所有要下发的排期文件；运行时不从版本化 seed 或抓包补齐缺失数据。

## 初始数据来源

当前 `local-20261005-1` 保留已有时间、编号和协议标记，没有推算后续官方日程：

| 文件 | 初始记录 | 原始来源 |
| --- | --- | --- |
| `gacha.bd2schedule` | 普通抽卡 11，StepUp 2 | 已迁移删除的 `go/seed/v2_35_10/gacha_schedule.json` |
| `regular.bd2schedule` | 常规内容 9，regular 标记 5 | 已迁移删除的 `go/seed/v2_35_10/schedule.json` |
| `monster_hunt.bd2schedule` | 魔物追踪窗口 1，历史 78 | 原 `go/seed/v2_35_10/readonly.json` 的 `/MonsterHuntScheduleInfo` |
| `cash_shop.bd2schedule` | 商品窗口 37 | 原 readonly seed 的 `/CashShopInfo` 商品行 |
| `event_hubs.bd2schedule` | 活动 Hub 2，小游戏 slot 6 | 原 readonly seed 的 `/EventHubInfo`、`/MiniGameHubInfo` |
| `events.bd2schedule` | 事件排期 54 | `data/capture/2.35.10/20261001-004259/bodies/000004_response_EventScheduleInfoResponse.pb` |

事件文件将以上同版本响应的既有身份和起止时间维护为项目日历；捕获的 `is_active` 不复制，由服务端根据当前时间重新计算。来源仅用于审计，运行时不读取抓包。UID 为 0 的多条任务以类型和设计组 ID 区分，不制造公开 UID。任务进度和领奖也使用对应组的独立本地身份。

Hub 的 UID 是 Hub 身份；slot 内的 UID 按内容类型引用事件、魔物追踪赛季等不同领域。例如 MonsterHunt slot 引用赛季 77/78。旧 Hub 74 的原始时间和子身份作为归档保留，当前捕获事件列表主要对应 Hub 75；不据此虚构已结束 Hub 的子事件窗口。Hub 的 play_end 与 end 独立保留，setting 可以引用多个 UID。

事件类型 8 的 ID 引用 `PackTable.Id`，依据当前客户端 `PackInfo.IsProgressUseScheduleEventPack` 和 `IsFinishEventPackSchedule`；它与 Hub 的 `PackEventHubTable.id`、`packId` 分属不同身份。小游戏 Hub 的 progress_type 表示普通、首次开放或复刻状态，不等于静态小游戏的 eventClearType。

商品每日、每周、每月重置时间由运行时根据当前时间生成。`SkyWayScheduleInfo` 的 weekday/bonus 规则仍属于静态 seed，`PassInfo` 和 `SeasonRewardInfo` 属于玩家或奖励域。

## 更新和验证

排期更新由项目维护，相关文件的 revision 随数据修订更新。仅调整时间无需修改游戏版本；引用不同静态设计时必须更新相应版本标签并校验实际 GameData。加载同时校验跨文件重复身份、时间窗口和可玩设计引用，全部通过才下发。事件类型 0、1、4、5、7、8、9、10、11、12、13、17、19 和 20 至 25 校验对应 GameData；类型 3、6、14、15、16、18 当前仅校验协议范围和格式。魔物追踪可玩窗口校验实际 hunt 设计，归档历史仅校验显示记录格式；小游戏 Hub 的 progress_type 校验状态格式。公共日历保留未来、当前和归档记录，玩法入口及进行中标记根据时间和玩家状态限制。
