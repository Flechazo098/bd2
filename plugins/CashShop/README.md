# BD2 Cash Shop

把当前游戏的现金支付入口替换为本服的游戏内货币购买。插件依赖 `BD2LocalIdentity.dll`，目标游戏版本在项目中明确声明为 `2.35.10`，使用共享 `BD2.GameNames.dll` 和 GameSdk 构建。

普通现金商品固定使用付费钻石。充值商品的获取方式由服务端 `game.json` 的 `purchases.diamond_recharge` 设置：默认免费，也可使用金币、普通钻石，或者禁止充值。完整配置和换算说明见 [GAME_CONFIGURATION.md](../../GAME_CONFIGURATION.md#现金商品与付费钻石充值)。

客户端从当前服务器 `/client/commerce` 获取完整价格目录。配置未加载、版本不匹配或禁止购买时，购买入口会显示游戏提示框，不会调用外部支付 SDK。收费商品使用游戏原有货币图标与数量；免费充值显示 `Free`。

补丁保留原购买回调，因此无限抽最终确认、特殊甄选券、月卡、礼包、皮肤和现金通行证继续执行各自的游戏流程。原本使用普通钻石、付费钻石、金币等游戏内货币的商品不修改价格类型。服务器独立验证商品、价格、余额、购买次数、开放时间和接力前置条件，并在一个存档事务内完成扣款与发奖；客户端发送的价格仅用于检测过期目录。

月卡、登录通行礼包和付费签到的领取状态在服务器保存，`AttendanceResponse` 与 `EventRewardResponse` 的本服扩展字段同步每日奖励并用回执防止重复应用。现金通行证激活必须消费购买权益。主线通关礼包按服务器主线进度领取；恶魔城奖励需要对应玩法提供真实完成记录，目前占位玩法不能领取通关奖励。

```powershell
dotnet build plugins/CashShop/CashShop.csproj -c Release
```

客户端开发入口和发布包会同时构建、安装此插件。手动部署时使用 `bin/Release/netstandard2.1/BD2CashShop.dll`，与 LocalIdentity 及共享运行时一起放入本服游戏的 `BepInEx/plugins`。原版客户端与官方抓包环境不要安装这个插件。
