# 离线开发工具

这里的程序用于查表、转换种子和维护测试存档。它们不属于游戏服务端运行时，不会进入 `bd2server.exe`。

Python 版本为 3.11 以上；GameData 分页 AES 解密需要 `pycryptodome`：

```powershell
python -m pip install pycryptodome
```

## 客户端源码镜像与 Proto 查看

客户端反编译源码、混淆翻译表和官方会话抓包是开发取证输入；这些工具只读取客户端目录，绝不原地改写。两个输出目录都必须位于 `--source` 之外。为避免清空人工文件，输出已存在时只有带本工具清单的目录才能被重新生成。

```powershell
# 用 ObfuscationTranslation 的 “混淆名⇨含义” 映射生成可检索 C# 镜像。
# 替换只作用于代码中的 C# 标识符；注释、字符串和字符字面量原样保留。
python .\tools\python\deobfuscate_client_source.py `
  --source "<Assembly-CSharp目录>" `
  --mapping "<ObfuscationTranslation.obfuscate>" `
  --output .\tmp\client-source-readable

# 从 *Reflection.cs 内嵌的 FileDescriptorProto 重建真正的 .proto。
python .\tools\python\extract_client_proto.py `
  --source "<Assembly-CSharp目录>" `
  --output .\tmp\client-proto
```

反混淆输出根下的 `.bd2-deobfuscate-manifest.json` 记录有效映射、冲突改名、未处理警告和替换统计。映射含命名空间或路径时，工具取末段并规范为一个合法标识符；发生同名时加入稳定后缀。该镜像用于阅读与检索，并不承诺可编译。

Proto 输出根按 descriptor 原始文件名保存可读 `.proto`；`client-descriptors.pb` 是无损 `FileDescriptorSet`，用于保留文本渲染器暂未展开的复杂 options。`.bd2-proto-extract-manifest.json` 记录 package、依赖、源 Reflection、descriptor/.proto SHA-256 和渲染警告。

## 查询 GameData

无需运行 `go test` 或构建服务端：

```powershell
# 列出任务库中的表
python .\tools\python\gamedata_db.py tables --root "<GameData目录>" --version "<版本>" --db quest --match QuestTable

# 查看表结构
python .\tools\python\gamedata_db.py schema --root "<GameData目录>" --version "<版本>" --db quest --table QuestTable22

# 展开一行 protobuf 的指定字段
python .\tools\python\gamedata_db.py row --root "<GameData目录>" --version "<版本>" --db quest --table PackTable --id 21 --fields 45

# 查看任务真实奖励（五个难度槽分别显示）
python .\tools\python\gamedata_db.py rewards --root "<GameData目录>" --version "<版本>" --pack 21 --quest 38

# 沿真实 PackTable.NextPackId 查看故事包链
python .\tools\python\gamedata_db.py chain --root "<GameData目录>" --version "<版本>" --start-pack 21

# 任意只读 SQL；参数用重复的 --param 传入
python .\tools\python\gamedata_db.py sql --root "<GameData目录>" --version "<版本>" --db pack21 `
  --query "SELECT id,ProtoBuf FROM BattleDeckTable WHERE id=?" --param 1

# 必要时导出普通 SQLite，默认拒绝覆盖
python .\tools\python\gamedata_db.py extract --root "<GameData目录>" --version "<版本>" --db quest --output .\tmp\quest.db
```

GameData 根目录和版本没有机器相关默认值，所有查询都必须显式传入 `--root` 和 `--version`。

## 种子转换

输入必须是已经解密、解包后的原始 protobuf，不接受 HTTP 信封或 AES 密文：

```powershell
python .\tools\python\import_seed.py login .\decoded\LoginUser.pb `
  --packet-code 11 --output .\go\seed\v2_34_13\login_user.json

python .\tools\python\import_seed.py starter `
  --items .\decoded\ItemInfo.pb `
  --costumes .\decoded\CostumeInfo.pb `
  --characters .\decoded\CharInfo.pb `
  --output .\go\seed\v2_34_13\starter_player.json

python .\tools\python\import_seed.py mail .\decoded\MailInfo.pb `
  --output .\go\seed\v2_34_13\mail.json
```

输出已存在时默认拒绝覆盖；审阅输入和预期差异后才传 `--force`。

`readonly` 子命令接收一个 JSON 清单，键是接口路径，值含 `packet_code` 和相对于清单的 protobuf 文件路径：

```json
{
  "/SkyWayScheduleInfo": {
    "packet_code": 162,
    "protobuf": "SkyWayScheduleInfo.pb"
  }
}
```

## 临时邮件物品发放

`dev_mail_grant.py` 是独立的、仅监听环回地址的开发期浏览器工具。它只读取指定版本的 GameData，列出已验证、可走 `ItemDBInfo` 领取路径的有名道具：`FoodTable`（类型 5）、`CookingTable`（7）、`ResourceTable`（8）、`QuestItemTable`（13）、`UseItemTable`（14）、`CollectionTable`（17）、`MyRoomItemTable`（27）和 `InstantUseItemTable`（29）。物品名按来源表引用的文本命名空间解析；确定性随机箱使用 `RandomBoxTextTable` 的真实名称作为内容物搜索别名，因此不依赖固定物品 ID。`ResourceTable.Type=2` 的场景/展示哨兵和无可用名称行不提供；固定内容随机箱只用于反查内容物的真实 ID，所有 type 9 随机箱均不作为邮件选项。另提供单一金币货币条目 `type4/id0`，填写的数量在领取后直接叠加至钱包，不再发送“金币随机箱”。工具本身既不属于 `bd2server.exe`，也不修改 `data/state` 的九份账号状态。

启动工具时，`--mail-seed` 是只读的当前基础邮件种子；`--output` 是新生成的完整临时种子。工具启动后在浏览器打开 `http://127.0.0.1:8765/`：

```powershell
python .\tools\python\dev_mail_grant.py serve `
  --game-data "E:\bd2\dl\GameData" `
  --game-data-version "20260910162539" `
  --mail-seed .\go\seed\v2_34_13\mail.json `
  --output .\data\dev\mail-grants.json
```

工具启动时立即原子写出规范化的完整 `--output`（尚未发放也一样），因此首次启用时可先启动工具、再让本地服务端监听这个输出文件。每次发放同样原子更新该文件。`bd2server` 的邮件服务会在下一次正常 `/MailInfo` 请求检查它：网页发放后重新打开或刷新游戏邮箱即可看到新邮件，**不需要每次重启服务器**。服务仅在启动时需要加入（或替换为）以下参数：

```powershell
--mail-seed ".\data\dev\mail-grants.json"
```

客户端 `MailDBInfo.ItemType`、`ItemId` 和 `ItemCount` 均为 `int32`，所以该工具把单附件数量限制为 `1..2147483647`；每封工具邮件固定只有一个附件。当前官方样本中单封最多观察到 5 个附件，但没有证据证明这是协议上限，因此工具不据此宣称或实施“5 件”上限。客户端邮箱 UI 按一次请求加载最多 100 封普通邮件，现有本地服务目前回传全部未开封邮件，故大量历史未领取邮件的实际 UI 表现尚待验证。现有本地 `/MailOpen` 对同一邮件 ID 的领取由 `data/state/mail.json` 的 `opened` 集合持久化，重试不会重复发奖；工具会在完整种子中分配唯一递增邮件 ID。

这不是“所有 GameData 表都可发放”的虚假承诺：角色（元素类型 6）、装备（10）、服装（11）和我的房间奖杯（28）在客户端 `RewardDBInfoBundle` 中分别必须使用 `CharDBInfo`、`EquipDBInfo`、`CostumeDBInfo`、`MyRoomTrophyDBInfo`，而当前本地邮件服务尚未连接相应领域存档，工具不会提供它们；直接伪装成 `ItemDBInfo` 会造成客户端状态错误。付费/普通货币之外的特殊货币亦不在当前本地钱包实现范围内。客户端 `DataManager.GetItemDTO` 对 `ContentTicket`（19）和 `LobbySettingItem`（25）没有可用于 `ItemDBInfo` 领取的 DTO 分支，故也没有提供；`GetItemInfo` 的显示分支不足以证明可安全存储。若要补齐这些类型，需要先实现对应的服务端存储、去重及正确 reward-bundle 字段，不需要客户端 patch。

热载只接受经过 `mail.Starter.Validate` 校验的完整 JSON 种子：文件未变化时不会重新读取；被检测到的坏替换会保留上一次已验证邮箱，并使该次 `/MailInfo` 请求失败而不会部分加载。工具本身总是完整写临时文件、`fsync` 后原子替换，正常发放不会让服务器看到半文件。

## 工具测试

```powershell
python -m unittest discover -s .\tools\python\tests -p 'test_*.py' -v
```
