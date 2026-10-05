# 项目排期文件

服务端从发布目录中的固定 `schedules/` 目录读取全部 `.bd2schedule` 文件，完整校验后合并下发排期。文件采用自定义二进制格式。可在游戏版本不变时单独发布排期文件，覆盖相应文件后重启服务端加载。文件中的游戏和 GameData 版本标签表示协议，排期 `revision` 独立表示日历修订。

可使用
```pwsh
.\build-release.ps1 -SchedulesOnly
```
单独构建排期文件。

## 二进制格式版本 1

整数使用小端编码。文件头固定 46 字节：

| 偏移 | 长度 | 内容 |
| --- | --- | --- |
| 0 | 8 | 魔数 `BD2SCH` 后接两个零字节 |
| 8 | 2 | uint16 格式版本，当前为 1 |
| 10 | 4 | uint32 payload 字节数 |
| 14 | 32 | payload 的 SHA-256 原始摘要 |
| 46 | payload 长度 | 按固定顺序编码的记录 |

定义在 `go/internal/server/calendar/records.go` ，自行查看。

## 更新和验证

排期更新由项目维护，相关文件的 revision 随数据修订更新。