# 离线开发工具

这里的程序用于查表、转换种子和维护测试存档。它们不属于游戏服务端运行时，不会进入 `bd2server.exe`。

Python 版本为 3.11 以上；GameData 分页 AES 解密需要 `pycryptodome`：

```powershell
python -m pip install pycryptodome
```

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

## 存档检查点

检查点包含九份账号状态和 SHA-256 清单：

```powershell
python .\tools\python\save_checkpoint.py create --label before-test
python .\tools\python\save_checkpoint.py verify .\data\state\checkpoints\某检查点

# 第一次只演练；不会写入
python .\tools\python\save_checkpoint.py restore .\data\state\checkpoints\某检查点

# 确认客户端和服务端均停止后才真正恢复
python .\tools\python\save_checkpoint.py restore .\data\state\checkpoints\某检查点 --apply
```

## 任务 38 定向回档

该工具只撤销 pack21 quest38 的进度、四项物品和 1500 金币，保留其他账号资产；兼容旧存档和 v2 `pack:quest` 键：

```powershell
# 只读预览
python .\tools\python\rollback_quest38.py .\data\state .\data\state\checkpoints\参考检查点

# 停止客户端和服务端后应用；工具会先备份九份状态并校验哈希
python .\tools\python\rollback_quest38.py .\data\state .\data\state\checkpoints\参考检查点 --apply
```

## 工具测试

```powershell
python -m unittest discover -s .\tools\python\tests -p 'test_*.py' -v
```
