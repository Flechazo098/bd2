# BD2 本地服务器发布包

## 首次使用

1. 为游戏手动安装 BepInEx。官方下载：
   <https://github.com/BepInEx/BepInEx/releases>
2. 修改本地客户端入口：

   ```powershell
   .\bd2server.exe patch-client --game-dir "<客户端目录>"
   ```

3. 进入发布包的 `go` 目录并启动服务器：

   ```powershell
   Push-Location .\go
   & ..\bd2server.exe serve `
     --game-dir "<客户端目录>" `
     --cdn "<ServerData目录>" `
     --game-data "<GameData目录>" `
     --game-data-version "<GameData版本>"
   Pop-Location
   ```

服务器会检查 BepInEx，并自动把包内 `plugins\BD2LocalIdentity.dll` 安装或更新到客户端。如果没有安装 BepInEx，服务器会给出上述下载地址、保持客户端不变并拒绝启动。

4. 确认健康检查成功：

   ```powershell
   Invoke-WebRequest http://127.0.0.1:8080/healthz
   ```

5. 最后启动游戏客户端。请勿在服务器启动前运行客户端。

`bd2-state.exe` 是服务端启动期使用的 Haskell 状态工具，必须与 `bd2server.exe` 保持在同一目录。
