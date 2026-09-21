# BD2 本地服务器

用于本地开发、协议研究和服务端实现练习。服务默认只监听环回地址，不应暴露到公网。

## 许可与免责声明

本项目原创内容由 Flechazo 保留全部权利，具体中英双语条款见 [LICENSE](LICENSE)。通过项目权利人指定渠道合法取得副本的人，仅可用于个人学习、研究和本地非商业测试；未经项目权利人事先明确书面许可，禁止向第三方转发、镜像、上传、二次分发项目源码或构建产物，禁止售卖、收费提供、商业化运营或对公众开放基于本项目的服务。公开可见不代表允许转载或再分发。

本项目是非官方开发与研究项目，与 Brown Dust II 及其权利人无隶属或授权关系。游戏名称、商标、客户端、游戏资源和数据归各自权利人所有；本项目的许可不覆盖它们，也不变更第三方依赖的许可证。项目按现状提供，无正确性、安全性、可用性或不侵权保证。使用者须自行确认适用法律、相关协议和第三方权利要求，并自行承担使用、客户端修改及本地服务运行风险。

## 开发构建

```powershell
# Go
Push-Location .\go
$env:GOCACHE = (Join-Path $PWD '.cache\go-build')
go test ./...
go build .\cmd\bd2server
Pop-Location

# Haskell
Push-Location .\haskell
cabal build all --enable-tests
cabal test all --test-show-details=direct
Pop-Location

$env:PYTHONDONTWRITEBYTECODE = '1'
python -m unittest discover -s .\tools\python\tests -p 'test_*.py' -v
```

## 发布包

```powershell
.\build-release.ps1 -GameDir "<客户端目录>"
```

脚本生成 `.build\bd2server-windows-x64.zip`。解压后得到一个完整目录，其中包含 Go 服务端、独立 Haskell 状态工具、本地身份插件、运行种子和空存档目录。Haskell 不再嵌入 Go 可执行文件。Go 构建缓存只写入 `go\.cache`。

## 客户端插件项目

- `plugins/LocalIdentity/`：本地服务端客户端专用插件。
- `plugins/CaptureEnvironment/`：独立原版对照客户端的抓包插件；不要装进本地服务端客户端。

## 首次安装本地客户端

先手动安装 BepInEx：<https://github.com/BepInEx/BepInEx/releases>。然后修改客户端入口：

```powershell
$clientDir = "<客户端目录>"

.\.build\package\bd2server\bd2server.exe patch-client `
  --game-dir $clientDir

```

启动服务端时会检查 BepInEx，并自动从发布目录安装或更新 `BD2LocalIdentity.dll`。如果未检测到 BepInEx，服务端只提示官方下载链接，不修改客户端，也不会继续启动。不要把原版抓包插件 `BD2CaptureEnvironment.dll` 安装进这个客户端。

## 启动顺序

顺序必须是：

1. 确认客户端已经安装 BepInEx。
2. 启动服务器；服务器自动同步 `BD2LocalIdentity.dll`。
3. 健康检查返回成功后，再启动游戏客户端。

启动服务器：

```powershell
Push-Location .\.build\package\bd2server\go
& ..\bd2server.exe serve `
  --game-dir "<客户端目录>" `
  --cdn "<ServerData目录>" `
  --game-data "<GameData目录>" `
  --game-data-version "<GameData版本>"
Pop-Location
```

启动时先检查 BepInEx 并同步插件，再核验资源；Go 随后调用同目录的 Haskell CLI 检查和修复存档，通过后加载账号与玩法服务，最后监听指定地址（默认 `127.0.0.1:8080`）。本地身份插件目前固定连接该默认地址；要更换地址，需要同步修改插件。

健康检查：

```powershell
Invoke-WebRequest http://127.0.0.1:8080/healthz
```

健康检查成功后才能启动本地客户端。服务器尚未监听时启动客户端，会在登录或维护信息阶段连接失败。

## 修改客户端

构建服务后运行：

```powershell
.\.build\package\bd2server\bd2server.exe patch-client --game-dir "<客户端目录>" --verify
.\.build\package\bd2server\bd2server.exe patch-client --game-dir "<客户端目录>"
```
