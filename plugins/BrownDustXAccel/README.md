# BD2 BrownDustX Accel

给第三方插件 BrownDustX（`lynesth.bd2.browndustx` 2.36.7.1）加速角色模组加载的独立插件。BrownDustX 的原始文件不做任何修改，本插件在运行时用 Harmony 接管它加载角色 Spine 模组（名称以 `char`、`npc`、`cutscene`、`illust`、`specialillust`、`storypack` 开头）的几个函数。intro、volume、sprite-atlas 等其他模组仍由 BrownDustX 自己加载。

没装 BrownDustX 时插件什么也不做。如果绑定 BrownDustX 内部成员失败（例如 BrownDustX 版本更新导致签名变化），插件会撤销全部补丁，恢复原逻辑，并在日志里报错。

## 原版的瓶颈（以实际运行的 DLL 为准，SHA-256 `c3da43eb…2403301`）

| 位置 | 问题 |
|---|---|
| `ModManager.GetMods` → `CheckModFolders` → `AddModFolder` → `LoadModData(string, Modfile, List)` | “Preload all Mods = Yes”（内存 ≥ 12 GB 时的默认值）下，所有模组在**一个协程帧内、主线程上串行**加载：每张 atlas 页都要 `File.ReadAllBytes` + `ImageConversion.LoadImage`（单线程 PNG 解码并生成 mipmap），模组多达几 GB 时游戏会卡住很久 |
| 同上，`Conf.ShouldCompressTextures` 为真时 | 每加载一个模组就 `yield BrownDustX.Cleanup()`：两次 `GC.Collect`、一次 `WaitForPendingFinalizers`，在 Splash 场景还会调用 `Resources.UnloadUnusedAssets()`，开销随模组数线性叠加；`ResizeToNearestMultipleOf4` 换出的原贴图也没有销毁 |
| `ModManager.PrepareModForLoading` | “Preload = No”时，启动阶段会在主线程同步读取**所有**模组的 `.skel`/`.json`，并且一直留在内存里，包括从没显示过的模组 |
| `SpineReplacer.PrepareRuntimeAsync` / `BuildModDataFromPrepared` | 后台线程只负责读取 PNG 字节，PNG 解码 `LoadImage` 仍在主线程逐页执行 |
| `ModManager.LoadModData(PreloadModData)`（`SkeletonGraphic/SkeletonMecanim.Initialize` 前缀、Intro） | 读文件和解码全部在主线程上同步完成 |
| `BrownDustX.OnSceneLoaded` | “Preload = No”时每次切场景都会清空 `LoadedModsData`，同一个角色再次出现时要从磁盘重新读取并解码 |

## 优化内容与预期效果

1. **多线程 PNG 解码，主线程只上传贴图**：自带 PNG 解码器，支持 8 位 RGBA/RGB/灰度/灰度+α，以及 1/2/4/8 位调色板（含 tRNS）。它在后台线程把文件解码成 Unity RGBA32 的原始布局（行序上下翻转，mip 链连续排列），主线程只调用 `SetPixelData` + `Apply` 上传。隔行扫描、16 位、色键 tRNS、损坏的文件会自动退回 `LoadImage`，结果与原版一致。用 Pillow 对比了 65 种格式和尺寸，解码结果（含 mip 链）逐字节一致。
   - 预期效果：每张页在主线程上的耗时从“解码 + 生成 mip”（4096² 通常 200–400 ms）降到一次内存拷贝加 GPU 上传（几十 ms），解码负载分摊到 `DecodeThreads` 个后台线程。
2. **启动预取流水线**（Preload = Yes）：`GetMods` 一开始就在后台线程按照与 `CheckModFolders` 完全相同的规则和顺序扫描模组目录。它利用原版固定等待的 1 秒，提前把各模组的 skel/json 和 atlas 页排进解码队列。主线程处理到某个模组时，直接取用已经解码好的数据。冲突或无效的模组排过之后会立即释放。已解码、未上传的数据总量受 `PrefetchBudgetMB` 限制，正在处理的模组永远不受这个限制，因此不会死锁。
   - 预期效果：启动总耗时从“所有页的解码时间之和”降到大约 max(磁盘读取, 解码时间 ÷ 线程数) + 上传时间。几 GB 模组、8 核 CPU 时，预计是原来的 1/3 到 1/6（取决于 CPU 核数和磁盘）。
3. **加载期间不再卡死界面，并显示进度**：每处理一个模组目录时检查当前帧的耗时，超过 100 ms 就让出一帧，加载对话框能持续刷新，Windows 也不会提示“未响应”。BrownDustX 的“正在加载 Mod”对话框下方会多出两样东西：
   - 正在加载的模组目录（相对 `mods/` 的路径）；
   - 一条进度条，标出“已加载 / 总数（百分比）”。总数在 `GetMods` 开始时由后台线程按 `CheckModFolders` 的相同规则统计。热重载（文件监听触发）时只处理改动过的目录，所以只显示已处理数量，进度条以循环动画表示还在加载。

   每个模组加载完成后才会刷新一次画面，所以单个特别大的模组加载期间进度条会停在原处。关闭加速（`Enabled = false`）时不显示进度条，因为原版加载过程中不会刷新画面。
4. **去掉逐模组 Cleanup**：扫描期间，压缩模式下每个模组后的 `Cleanup()`（GC ×2 + UnloadUnusedAssets）改为空操作，只保留扫描结束时那一次。缩放前的临时贴图会主动 `Destroy`。
5. **按需模式（Preload = No）**：
   - 启动时只做文件校验（atlas、贴图是否存在，json/skel 命名是否合法），skel/json 推迟到模组第一次显示时再读。启动 IO 减少，未使用的模组也不再常驻内存。报错信息与原版一致。
   - `PrepareRuntimeAsync` 把所有页并行解码放在后台，`BuildModDataFromPrepared` 只负责上传。同步路径 `LoadModData(PreloadModData)` 由主线程和后台线程一起并行解码。
   - 场景 LRU 缓存（`SceneCacheMB`，默认 512 MB 贴图内存）：切场景后再次出现的角色直接复用已经上传的贴图，耗时接近 0。缓存中的贴图如果已被 Unity 卸载会自动失效，监听到模组目录变化重载时会清空。
6. **解码缓冲池**：文件读取缓冲和像素缓冲用 `ArrayPool` 复用，每轮加载结束后整体丢弃，避免几 GB 的大对象反复分配，给 Mono Boehm GC 带来压力。

## 配置

`BepInEx/config/bd2.browndustx.accel.cfg`，修改后需要重启游戏：

| 键 | 默认值 | 说明 |
|---|---|---|
| `General.Enabled` | `true` | `false` 时不加速，只输出耗时日志，用于对比 |
| `Performance.DecodeThreads` | `0` | 0 表示 CPU 核数 − 1，最多 8 |
| `Performance.PrefetchBudgetMB` | `1024` | 启动预取时，已解码但尚未上传的数据上限 |
| `Performance.SceneCacheMB` | `512` | 按需模式下跨场景保留的贴图内存，0 表示关闭 |
| `Logging.VerboseTiming` | `false` | 启动预加载时逐个模组输出耗时 |

## 在游戏里验证

1. 构建：`dotnet build plugins/BrownDustXAccel/BrownDustXAccel.csproj -c Release`，把 `bin/Release/netstandard2.1/BD2BrownDustXAccel.dll` 复制到游戏的 `BepInEx/plugins/` 下。
2. 启动后，在 `BepInEx/LogOutput.log` 里查找 `BD2 BrownDustX Accel`：
   - `Attached to BrownDustX 2.36.7.1: character mod acceleration on, N decode threads`：补丁安装成功。
   - 模组加载完成后会输出一行，例如：
     `Mod scan (preload all, acceleration on) finished: folder scan 9840 ms, total 10870 ms; 182 character mods, 534 pages (11200 MB pixels, 534 prefetched, 0 Unity-decoded); main thread upload 2310 ms, waiting 4100 ms; decode CPU 61200 ms on 7 threads`
     - `folder scan` 是 BrownDustX 遍历并加载全部模组的耗时，`total` 还包括它固定的 1 秒等待和最后一次清理。
     - `waiting` 是主线程等待后台解码的时间，数值大说明瓶颈在解码线程或磁盘，可以调大 `DecodeThreads`。
     - `Unity-decoded` 是退回 `LoadImage` 解码的页数，正常应为 0。
   - 按需模式下，每次加载一个角色会输出：`On-demand load char000101: 4 pages in 180 ms, main thread 25 ms`。切场景后再次出现的同一角色走缓存，不会再出现这行日志。
3. 对比原版：把 `General.Enabled` 设为 `false` 后重启，得到同一格式的 `Mod scan (…, acceleration off) finished: folder scan … ms, total … ms`，对比两次的 `folder scan` 即可。
