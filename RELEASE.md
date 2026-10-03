# BD2 纯服务端发布包

## 首次使用

1. 准备服务端数据目录。服务端不需要 Windows 游戏客户端、BepInEx 或客户端插件。
2. 编辑同目录的 `authentication.json`、`resources.json`；OAuth 配置见 `AUTHENTICATION.md`。
3. 启动服务器：

   Windows PowerShell：

   ```powershell
   .\bd2server.exe serve
   ```

   macOS/Linux（Bash 或 Zsh）：

   ```bash
   ./bd2server serve
   ```

服务端默认使用可执行文件旁的 `data` 目录，玩家存档位于 `data/state/state.db`，服务端逻辑所需 GameData 位于 `data/resources/GameData`。缺失时会从官方源下载并完整校验。服务端不会访问或修改玩家客户端目录。

4. 确认健康检查成功：

   ```bash
   curl --fail --silent --show-error http://127.0.0.1:8080/readyz
   ```

5. 让玩家使用独立的 `bd2client.exe` 选择游戏目录、填写服务器地址、选择 CDN，并安装 `BD2LocalIdentity.dll` 与 `BD2LoginUI.dll`。客户端工具说明见 `README.md`（客户端包）或仓库的 `docs/CLIENT.md`。

`versions.json` 是服务端和客户端插件共用的版本选择，必须与各自可执行文件保持在同一目录。`game_version` 锁定官方游戏兼容版本；`client_version` 与 `server_version` 分别标识客户端工具和纯服务端发布版本，并以 `游戏版本+组件.X.Y.Z` 组合。服务端从包内 `go/seed` 读取种子，不依赖当前工作目录。需要临时测试另一组版本时可传 `serve --version-config <文件>`。玩家存档位于 `data\state\state.db`；服务端以 Go 迁移器按相邻版本升级，并在同一个 SQLite 事务内完成迁移和最终校验，失败时不会留下部分升级。

`authentication.json` 和 `resources.json` 必须与 `bd2server.exe` 保持在同一目录。默认 `local` 认证模式继续自动本地登录；公网或联机服可以启用 OAuth，并把资源策略设为官方 CDN 或统一的服务器资源源。服主自建与反代使用相同的 `server` 模式；玩家本地资源目录仅由 `bd2client.exe` 配置。逐步配置方法见发布包内的 [服主第三方登录配置指南](AUTHENTICATION.md) 和 [服主资源与 CDN 配置指南](RESOURCES.md)。

游戏规则使用同目录的 `game.json`，修改后重启服务端生效。默认关闭联动 UR 专武扩展；需要将 14 种联动角色专武加入 UR 必得装备券池时，按 [服务端游戏规则配置](GAME_CONFIGURATION.md) 设置。升级时保留已修改的文件；旧安装缺少文件时使用默认规则。

OAuth access token 只在客户端内存中；Windows refresh credential 使用当前用户 DPAPI 加密后写入 PlayerPrefs 注册表，macOS 使用 Keychain Services。服务端 `auth.db` 只保存 token、device secret、OAuth state 及 provider subject 的 HMAC，临时可恢复材料使用 AES-256-GCM。
