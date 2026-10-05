# 服主资源与 CDN 配置指南

`bd2server` 服务端只会自动准备自身逻辑需要的 GameData。玩家在 `bd2client` 中有三种资源来源：

- **官方 CDN**：玩家直接从官方节点下载。
- **服务器资源源**：客户端向游戏服务器查询资源 URL；服主可以在 URL 后部署自建镜像或官方 CDN 反代都可，两者使用同一种协议。
- **本地资源目录**：客户端使用玩家已经下载的资源。

无论怎样，最终的资源都是要来到玩家的客户端的。

## 小服与 FRP

可以在 `resources.json` 中使用默认配置：

```json
{
  "mode": "official"
}
```

玩家在 `bd2client.exe` 中选择“官方 CDN”。这样我们的服务器只会承担平常游玩的游戏 API。

## 获取官方资源镜像

服主准备自建 CDN 时，可以运行：

Windows（PowerShell 7）：

```powershell
.\bd2server.exe resources fetch --output 'D:\bd2-resources'
```

macOS/Linux（Bash 或 Zsh）：

```bash
./bd2server resources fetch --output '/srv/bd2-resources'
```

命令会按照同目录 `versions.json` 中的版本下载所需的数据。

ServerData 会达到数 GB。注意合理分配，服务器启动时不会下载这些，只会下载所需的游戏数据文件。

同步结果的目录结构是：

```text
<output>/
├─ ServerData/StandaloneWindows64/HD/<bundle_version>/...
├─ GameData/<game_data_version>/...
└─ resource-fetch-manifest.json
```

## 服务器资源源

服主自建资源镜像与反代官方 CDN 对客户端来说没有区别：它们都提供两个可公开访问的 HTTP(S) 基础 URL。服务端统一使用 `server` 模式。

把上述 `ServerData` 和 `GameData` 目录上传到对象存储、静态 Web 服务器或 CDN 时，资源 URL 必须保留目录层级，并允许普通 `GET` 请求。

例如：

```json
{
  "mode": "server",
  "server_data_url": "https://cdn.example.com/ServerData",
  "game_data_url": "https://cdn.example.com/GameData"
}
```

保存为 `bd2server` 可执行文件同目录的 `resources.json`，然后重启服务端。玩家在 `bd2client.exe` 中选择“服务器资源源”；LI 插件会执行：

```http
PUT /client/resources
Content-Type: application/json

{"cdn_mode":"server"}
```

服务端会返回经过校验的 URL、Bundle 版本和 GameData 版本。客户端会拒绝模式或版本不一致的响应。

### 反代官方 CDN

如果服主不保存完整镜像，也可以把相同的 `server` 配置指向已有边缘 CDN 或带磁盘缓存的 Nginx。

Nginx 参考配置：

```nginx
proxy_cache_path /var/cache/nginx/bd2 levels=1:2 keys_zone=bd2:256m max_size=100g inactive=30d use_temp_path=off;

server {
    listen 443 ssl http2;
    server_name cdn.example.com;

    location /ServerData/ {
        proxy_pass https://bd2-cdn.akamaized.net/ServerData/;
        proxy_set_header Host bd2-cdn.akamaized.net;
        proxy_ssl_server_name on;
        proxy_cache bd2;
        proxy_cache_lock on;
    }

    location /GameData/ {
        proxy_pass https://bd2-cdn.akamaized.net/GameData/;
        proxy_set_header Host bd2-cdn.akamaized.net;
        proxy_ssl_server_name on;
        proxy_cache bd2;
        proxy_cache_lock on;
    }
}
```

TLS 证书、缓存容量、访问日志和服务条款由服主负责。公网资源 URL 必须使用 HTTPS；只有 loopback 开发地址允许 HTTP。URL 不能包含用户凭据、查询参数、fragment 或尾部 `/`。

## 本地已下载资源

适用于玩家已经自行取得当前版本完整资源的情况。资源来源需要玩家自行解决。

选择的目录必须是同时包含 `ServerData` 和 `GameData` 的根目录，结构如下：

```text
<本地资源根目录>/
├─ ServerData/
│  └─ StandaloneWindows64/
│     └─ HD/
│        └─ <bundle_version>/
│           ├─ catalog_alpha.json
│           ├─ catalog_alpha.hash
│           └─ <catalog 引用的所有 bundle 文件及其原始子目录>
└─ GameData/
   └─ <game_data_version>/
      ├─ design.version
      └─ release/
         ├─ common-dbdata.info
         └─ common-dbdata.bin
```

`<bundle_version>` 和 `<game_data_version>` 必须分别等于客户端包内 `versions.json` 的 `bundle_version` 与 `game_data_version`。例如当前发布版本对应：

```text
ServerData/StandaloneWindows64/HD/20260921135230/
GameData/20260923193640/
```

准备完成后，在 `bd2client` 中选择“本地已下载资源”，并选中最外层的 `<本地资源根目录>`，不要直接选择 `ServerData`、`GameData` 或某个版本号目录。客户端会验证目录结构和版本，然后由 LI 插件在游戏进程内把该目录作为仅监听 loopback 的本机资源源提供给游戏。校验失败时需要玩家自行修正或重新准备资源，客户端不会跨版本拼接或自动从网络补齐缺失文件。

## 版本更新

每次更新 `versions.json` 后：

1. 重新运行 `bd2server resources fetch`。
2. 等待服务器资源源完成上传，或确认反代缓存已经准备好。
3. 确认新版本目录可以访问。
4. 再启动新版本服务端并让玩家更新客户端工具包和插件。

不要把新 catalog 与旧 bundle 目录混用，也不要只修改 `resources.json` 中的 URL 而忽略版本文件。