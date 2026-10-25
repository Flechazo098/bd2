# 更新日志

## server 2.37.13+server.0.2.3 · 2026-10-25

> type: update
> migration: none

### en_us: Preserve health when changing equipment

- Equipping, removing or upgrading gear and switching presets now preserve the character's health percentage. Full health stays full, injured characters retain their health ratio, and defeated characters remain defeated.
- Transferring equipped gear updates both characters' health together with the equipment change.

### zh_cn: 换装与切换预设保留生命比例

- 穿戴、卸下、强化装备或切换预设时，当前生命按新生命上限同比换算：满血保持满血，受伤保留比例，阵亡角色不会复活。
- 转移已穿戴装备时，装备原持有者与接收者的生命会随装备一同保存。

### zh_tw: 換裝與切換預設保留生命比例

- 穿戴、卸下、強化裝備或切換預設時，目前生命會依新生命上限同比換算：滿血維持滿血，受傷保留比例，陣亡角色不會復活。
- 轉移已穿戴裝備時，裝備原持有者與接收者的生命會隨裝備一同儲存。

### ja_jp: 装備変更時のHP割合を保持

- 装備の着脱・強化やプリセットの切り替え時に、変更後の最大HPに合わせて現在HPの割合を維持します。満タンなら満タンを維持し、戦闘不能のキャラクターは復活しません。
- 装備を他のキャラクターへ移す際、両者のHPを装備変更と同時に保存します。

### ko_kr: 장비 변경 시 체력 비율 유지

- 장비 착용, 해제, 강화 및 프리셋 변경 시 새로운 최대 체력에 맞춰 현재 체력 비율을 유지합니다. 최대 체력은 그대로 유지되고, 부상 비율도 유지되며, 전투 불능 캐릭터는 부활하지 않습니다.
- 착용한 장비를 옮기면 기존 착용자와 새 착용자의 체력을 장비 변경과 함께 저장합니다.

## client 2.37.13+client.0.2.13 · 2026-10-25

> type: update

### en_us: Fix health display after switching presets

- Prevent intermediate equipment changes while applying a preset from reducing a full-health character's HP. After all slots are applied, retain the server's final health value and refresh the display.

### zh_cn: 修复切换预设后的生命显示

- 修复应用预设时逐件替换装备导致满血角色变为受伤状态的问题；全部装备应用后保留服务端最终生命值，并刷新界面。

### zh_tw: 修正切換預設後的生命顯示

- 修正套用預設時逐件替換裝備，導致滿血角色變為受傷狀態的問題；全部裝備套用後保留伺服器最終生命值，並更新介面。

### ja_jp: プリセット切り替え後のHP表示を修正

- プリセット適用中の一時的な装備変更で満タンのHPが減る問題を修正しました。全スロットの適用後にサーバーの最終HPを保持し、表示を更新します。

### ko_kr: 프리셋 변경 후 체력 표시 수정

- 프리셋 적용 중 장비가 하나씩 바뀌면서 최대 체력 상태의 캐릭터 체력이 줄어드는 문제를 수정했습니다. 모든 장비 적용 후 서버의 최종 체력을 유지하고 화면을 갱신합니다.

## server 2.37.13+server.0.2.2 · 2026-10-25

> type: update
> migration: none

### en_us: Game card quests and ID card fixes

- Restore the introductory quests for Mirror War, Last Night and Evil Castle, and the quest chains in character and older event packs.
- Restore character-pack side quest previews and completion counts, and preserve quest progress and cutscene checkpoints after reconnecting.
- Award arena quest currencies correctly and prevent repeated quest completion from paying rewards twice.
- Fix saving a portrait ID card from the personal profile.

### zh_cn: 游戏卡任务与 ID 卡修复

- 补齐镜中之战、末日之书和恶魔城的入门任务，以及角色卡、旧活动卡的任务链。
- 恢复角色卡支线预览和通关计数，重连后保留任务进度与剧情检查点。
- 修复竞技玩法任务货币奖励发放，重复完成不会重复领奖。
- 修复在个人资料中保存竖屏 ID 卡失败的问题。

### zh_tw: 遊戲卡任務與 ID 卡修正

- 補齊鏡中之戰、末日之書與惡魔城的入門任務，以及角色卡、舊活動卡的任務鏈。
- 恢復角色卡支線預覽與通關計數，重新連線後保留任務進度及劇情檢查點。
- 修正競技玩法任務貨幣獎勵發放，重複完成不會重複領獎。
- 修正在個人資料中儲存直式 ID 卡失敗的問題。

### ja_jp: ゲームカセットのクエストとIDカードの修正

- 鏡戦争、ラストナイト、悪魔城の導入クエストと、キャラクター・過去イベントのカセットのクエストチェーンを復元しました。
- キャラクターカセットのサブクエスト表示とクリア数を修正し、再接続後もクエスト進行とシナリオのチェックポイントを保持します。
- 競技コンテンツのクエスト通貨報酬を正しく配布し、同じクエストの再精算で報酬が重複しないようにしました。
- プロフィールから縦向きのIDカードを保存できない問題を修正しました。

### ko_kr: 게임팩 퀘스트 및 ID 카드 수정

- 거울전쟁, 마물의 밤, 악마성의 도입 퀘스트와 캐릭터팩 및 이전 이벤트팩의 퀘스트 연계를 복원했습니다.
- 캐릭터팩 서브 퀘스트 미리 보기와 완료 횟수를 수정하고, 재접속 후에도 퀘스트 진행도와 스토리 체크포인트를 유지합니다.
- 경쟁 콘텐츠 퀘스트의 재화 보상을 올바르게 지급하고, 같은 퀘스트를 다시 정산해도 보상이 중복 지급되지 않도록 했습니다.
- 프로필에서 세로형 ID 카드를 저장하지 못하는 문제를 수정했습니다.

## client 2.37.13+client.0.2.12 · 2026-10-25

> type: update

### en_us: Field party, item stacks and story recovery fixes

- Preserve full character and costume identifiers when selecting the field party, preventing invalid selections and crashes.
- Fix automatic item stack refresh failing to initialize after the game update.
- Fix resuming an interrupted opening quest cutscene.

### zh_cn: 跑图队伍、物品堆叠与剧情恢复修复

- 修复选择跑图队伍时角色、服装编号被截断导致选择异常或卡退的问题。
- 修复游戏更新后物品堆叠自动刷新无法初始化的问题。
- 修复恢复未完成的开场任务剧情失败的问题。

### zh_tw: 跑圖隊伍、物品堆疊與劇情恢復修正

- 修正選擇跑圖隊伍時角色、服裝編號被截斷，導致選擇異常或遊戲退出的問題。
- 修正遊戲更新後物品堆疊自動重新整理無法初始化的問題。
- 修正恢復尚未完成的開場任務劇情失敗的問題。

### ja_jp: フィールド編成、アイテムスタックとシナリオ復帰の修正

- フィールド編成の選択時にキャラクターと衣装の識別番号が切り詰められ、選択異常やクラッシュが起きる問題を修正しました。
- ゲーム更新後にアイテムスタックの自動更新を初期化できない問題を修正しました。
- 中断した導入クエストのシナリオを再開できない問題を修正しました。

### ko_kr: 필드 편성, 아이템 스택 및 스토리 복구 수정

- 필드 편성을 선택할 때 캐릭터와 코스튬 식별 번호가 잘려 선택 오류나 게임 종료가 발생하는 문제를 수정했습니다.
- 게임 업데이트 후 아이템 스택 자동 새로 고침을 초기화하지 못하는 문제를 수정했습니다.
- 중단된 도입 퀘스트 스토리를 재개하지 못하는 문제를 수정했습니다.

## server 2.37.13+server.0.2.1 · 2026-10-24

> type: update
> migration: none

### en_us: Unlimited item stacks, farm quests and Evil Castle battle fixes

- The admin console's unlimited item stacks switch takes effect right away: matching items merge into one stack, and switching it off splits them back to the game's limits.
- The farm serves its quest chain, so the farm tutorial can finish and life quests pay their rewards.
- Farm food can be bought, cooked and eaten in bulk, objects can be moved across farm areas, and crops show the right growth stage.
- Evil Castle battles with a borrowed supporter or no costume settle correctly, and floor rewards only count finished runs and new victories.
- Field research that failed to load in some areas loads again.

### zh_cn: 不限物品堆叠、农场任务与恶魔城战斗修复

- 管理台的不限物品堆叠开关即时生效：同一物品合并为一堆，关闭后按游戏上限重新拆分。
- 农场提供任务链，农场教程可以完成，生活任务会发放奖励。
- 农场食物可以批量购买、烹饪和食用，物件可以跨区域移动，作物显示正确的生长阶段。
- 带借用支援角色或未装备服装的恶魔城战斗可以正常结算，楼层奖励只计算已完成的挑战和新的胜利。
- 部分区域无法载入的野外调查恢复正常。

### zh_tw: 不限物品堆疊、農場任務與惡魔城戰鬥修正

- 管理台的不限物品堆疊開關即時生效：同一物品合併為一堆，關閉後依遊戲上限重新拆分。
- 農場提供任務鏈，農場教學可以完成，生活任務會發放獎勵。
- 農場食物可以批次購買、烹飪與食用，物件可以跨區域移動，作物顯示正確的生長階段。
- 帶借用支援角色或未裝備服裝的惡魔城戰鬥可以正常結算，樓層獎勵只計算已完成的挑戰與新的勝利。
- 部分區域無法載入的野外調查恢復正常。

### ja_jp: スタック無制限、農場クエストと悪魔城バトルの修正

- 管理コンソールのスタック無制限スイッチがすぐに反映され、同じアイテムが一つにまとまり、オフにするとゲームの上限で分け直されます。
- 農場がクエストチェーンを提供し、農場チュートリアルを完了でき、ライフクエストの報酬を受け取れます。
- 農場の料理をまとめて購入・調理・食事でき、オブジェクトをエリアをまたいで移動でき、作物が正しい成長段階を表示します。
- 借りたサポーターや衣装なしの悪魔城バトルが正しく精算され、フロア報酬は完了した挑戦と新しい勝利のみを数えます。
- 一部のエリアで読み込めなかったフィールド調査が正常に戻りました。

### ko_kr: 아이템 스택 무제한, 농장 퀘스트 및 악마성 전투 수정

- 관리 콘솔의 아이템 스택 무제한 스위치가 바로 적용되어 같은 아이템이 하나로 합쳐지고, 끄면 게임 상한에 맞게 다시 나뉩니다.
- 농장이 퀘스트 체인을 제공하여 농장 튜토리얼을 완료할 수 있고 라이프 퀘스트 보상을 받을 수 있습니다.
- 농장 음식을 한꺼번에 구매, 요리, 섭취할 수 있고, 오브젝트를 구역을 넘어 이동할 수 있으며, 작물이 올바른 성장 단계를 표시합니다.
- 빌린 서포터나 코스튬 없이 진행한 악마성 전투가 올바르게 정산되고, 층 보상은 완료한 도전과 새로운 승리만 계산합니다.
- 일부 지역에서 불러오지 못하던 필드 조사가 정상으로 돌아왔습니다.

## client 2.37.13+client.0.2.11 · 2026-10-24

> type: update

### en_us: Item stacks update in game right away

- After the admin console switches unlimited item stacks on or off, merged or split stacks show up in the bag, storage and life items without logging in again.

### zh_cn: 物品堆叠即时刷新

- 在管理台开启或关闭不限物品堆叠后，背包、仓库和生活物品会立即显示合并或拆分后的结果，不用重新登录。

### zh_tw: 物品堆疊即時刷新

- 在管理台開啟或關閉不限物品堆疊後，背包、倉庫與生活物品會立即顯示合併或拆分後的結果，不用重新登入。

### ja_jp: アイテムスタックの即時反映

- 管理コンソールでスタック無制限をオン・オフすると、ログインし直さなくてもバッグ、倉庫、ライフアイテムにまとめた結果や分け直した結果がすぐに表示されます。

### ko_kr: 아이템 스택 즉시 반영

- 관리 콘솔에서 아이템 스택 무제한을 켜거나 끄면 다시 로그인하지 않아도 가방, 창고, 라이프 아이템에 합치거나 나눈 결과가 바로 표시됩니다.

## client 2.37.13+client.0.2.10 · 2026-10-24

> type: update

### en_us: Guild raid introduction and costume settings

- Fix the guild raid introduction checkpoint and replay an unfinished introduction after reconnecting.
- Preserve full character and costume identifiers when automatically adding a newly acquired costume to the field party.
- Fix the friend online indicator failing to initialize.

### zh_cn: 公会联合战介绍与服装设置修复

- 修复公会联合战入场介绍的剧情检查点，重连后可重新播放未完成的介绍。
- 修复新服装自动加入跑图队伍时角色和服装编号被截断的问题。
- 修复好友在线标记初始化失败的问题。

### zh_tw: 公會聯合戰介紹與服裝設定修正

- 修正公會聯合戰入場介紹的劇情檢查點，重新連線後可重播未完成的介紹。
- 修正新服裝自動加入跑圖隊伍時角色與服裝編號被截斷的問題。
- 修正好友上線標記初始化失敗的問題。

### ja_jp: ギルドレイドの導入と衣装設定の修正

- ギルドレイドの導入チェックポイントを修正し、再接続後に未完了の導入を最初から再生できるようにしました。
- 新しい衣装をフィールド編成に自動追加する際、キャラクターと衣装の識別番号が切り詰められる問題を修正しました。
- フレンドのオンライン表示を初期化できない問題を修正しました。

### ko_kr: 길드 레이드 도입 및 코스튬 설정 수정

- 길드 레이드 도입 체크포인트를 수정하여 재접속 후 완료하지 못한 도입을 처음부터 재생할 수 있습니다.
- 새 코스튬을 필드 파티에 자동 추가할 때 캐릭터와 코스튬 식별 번호가 잘리는 문제를 수정했습니다.
- 친구 온라인 표시가 초기화되지 않는 문제를 수정했습니다.

## client 2.37.13+client.0.2.9 · 2026-10-23

> type: update

### en_us: Friend presence and connection fixes

- Show a green online indicator in the friend list.
- Fix the Square connection being repeatedly rejected.
- Remove the blinking debugger error icon while keeping error logs.

### zh_cn: 好友在线状态与连接修复

- 好友列表新增绿色在线标记。
- 修复虚拟广场连接被重复拒绝的问题。
- 移除闪烁的调试错误图标，错误日志继续保留。

### zh_tw: 好友上線狀態與連線修正

- 好友列表新增綠色上線標記。
- 修正虛擬廣場連線被重複拒絕的問題。
- 移除閃爍的除錯錯誤圖示，錯誤日誌繼續保留。

### ja_jp: フレンドのオンライン表示と接続修正

- フレンド一覧に緑色のオンライン表示を追加しました。
- 仮想広場への接続が繰り返し拒否される問題を修正しました。
- エラーログを残したまま、点滅するデバッガー通知アイコンを非表示にしました。

### ko_kr: 친구 접속 상태 및 연결 수정

- 친구 목록에 초록색 온라인 표시를 추가했습니다.
- 가상 광장 연결이 반복해서 거부되는 문제를 수정했습니다.
- 오류 로그는 유지하면서 깜박이는 디버거 오류 아이콘을 숨겼습니다.

## server 2.37.13+server.0.2.0 · 2026-10-23

> type: update
> migration: none

### en_us: Brown Dust II 2.37.13, guild raid overkill and console permissions

- Support Brown Dust II 2.37.13 with schedules from the current official events.
- Guild raid standings break score ties with the overkill of each guild's top-scoring members.
- The console can mail every currency the game credits, including the Cafeteria and decoration coins.
- Administrators no longer receive mail access by default; with it they can only mail their own account, and only the owner chooses other recipients.
- A server that cannot serve its client patch plan now tells the launcher why.

### zh_cn: 适配 2.37.13，公会讨伐超杀与管理台权限

- 适配棕色尘埃2 2.37.13，排期更新为官方当前活动。
- 公会讨伐排名在分数相同时，按各公会得分最高成员的超杀值决定先后。
- 管理台可以寄送游戏能入账的全部货币，包括格鲁菲之星和装饰币。
- 任命管理员不再默认获得邮件权限；获得后也只能给自己的账号寄送，只有服主可以选择其他收件人。
- 服务器无法提供客户端补丁计划时，会告诉启动器具体原因。

### zh_tw: 支援 2.37.13，公會討伐超殺與管理台權限

- 支援棕色塵埃2 2.37.13，排程更新為官方目前的活動。
- 公會討伐排名在分數相同時，依各公會得分最高成員的超殺值決定先後。
- 管理台可以寄送遊戲能入帳的所有貨幣，包括格魯菲之星與裝飾幣。
- 任命管理員不再預設取得郵件權限；取得後也只能寄給自己的帳號，只有服主可以選擇其他收件人。
- 伺服器無法提供用戶端修補計畫時，會告訴啟動器具體原因。

### ja_jp: 2.37.13 対応、ギルド討伐のオーバーキルと管理画面の権限

- ブラウンダスト2 2.37.13 に対応し、スケジュールを公式の現行イベントに更新しました。
- ギルド討伐の順位は、スコアが同じ場合に各ギルドの上位メンバーのオーバーキル値で決まります。
- 管理画面から、ゲームが付与できるすべての通貨（食堂コインや装飾コインを含む）を送れるようになりました。
- 管理者には既定でメール権限が付与されなくなりました。付与された場合も自分のアカウントにのみ送れ、ほかの受取人を選べるのはサーバー主だけです。
- サーバーがクライアントパッチ計画を提供できない場合、その理由をランチャーに伝えます。

### ko_kr: 2.37.13 지원, 길드 토벌 오버킬 및 관리 콘솔 권한

- 브라운더스트2 2.37.13을 지원하며 일정을 공식 현재 이벤트로 갱신했습니다.
- 길드 토벌 순위는 점수가 같을 때 각 길드 상위 멤버의 오버킬 값으로 결정됩니다.
- 관리 콘솔에서 게임이 지급할 수 있는 모든 재화(식당 코인, 장식 코인 포함)를 보낼 수 있습니다.
- 관리자는 더 이상 기본으로 메일 권한을 받지 않으며, 권한이 있어도 자신의 계정에만 보낼 수 있습니다. 다른 수신자는 서버 주인만 선택할 수 있습니다.
- 서버가 클라이언트 패치 계획을 제공하지 못하면 그 이유를 런처에 알려 줍니다.

## client 2.37.13+client.0.2.8 · 2026-10-23

> type: update

### en_us: Brown Dust II 2.37.13 and clearer errors

- Support Brown Dust II 2.37.13.
- When the server refuses a download or update, the launcher now shows the server's own reason instead of only an HTTP status code.

### zh_cn: 适配 2.37.13，错误提示更清楚

- 适配棕色尘埃2 2.37.13。
- 服务器拒绝下载或更新时，启动器会显示服务器给出的具体原因，而不再只显示 HTTP 状态码。

### zh_tw: 支援 2.37.13，錯誤提示更清楚

- 支援棕色塵埃2 2.37.13。
- 伺服器拒絕下載或更新時，啟動器會顯示伺服器提供的具體原因，而不再只顯示 HTTP 狀態碼。

### ja_jp: 2.37.13 対応とわかりやすいエラー表示

- ブラウンダスト2 2.37.13 に対応しました。
- サーバーがダウンロードや更新を拒否した場合、HTTP ステータスコードだけでなくサーバーが示した理由を表示します。

### ko_kr: 2.37.13 지원 및 오류 안내 개선

- 브라운더스트2 2.37.13을 지원합니다.
- 서버가 다운로드나 업데이트를 거부하면 HTTP 상태 코드 대신 서버가 알려 준 구체적인 이유를 표시합니다.

## client 2.36.7+client.0.2.7 · 2026-10-21

> type: update

### en_us: Local preset migration

- Migrate saved local inventory references before game startup to preserve talent shortcuts and party presets after inventory identifiers change.

### zh_cn: 本地预设迁移

- 启动游戏前迁移本地保存的背包引用，避免实例编号调整后天赋快捷栏与配队预设失效。

### zh_tw: 本地預設遷移

- 啟動遊戲前遷移本機儲存的背包引用，避免實例編號調整後天賦快捷欄與隊伍預設失效。

### ja_jp: ローカルプリセットの移行

- ゲーム起動前に保存済みのインベントリ参照を移行し、識別子変更後も才能ショートカットと編成プリセットを保持します。

### ko_kr: 로컬 프리셋 이전

- 게임 시작 전에 저장된 인벤토리 참조를 이전하여 식별자 변경 후에도 재능 단축 슬롯과 편성 프리셋을 유지합니다.

## client 2.36.7+client.0.2.6 · 2026-10-20

> type: update

### en_us: Supporter identity and battle recovery

- Fix borrowed supporters replacing an owned character when their inventory identifiers collide.
- Preserve the borrowed supporter's character data and selected slot when resuming a battle.

### zh_cn: 助战角色与战斗恢复修复

- 修复助战与自有角色的背包实例编号冲突，导致自有角色被错误替换的问题。
- 恢复战斗时保留借用助战的角色数据和实际选择的槽位。

### zh_tw: 助戰角色與戰鬥恢復修復

- 修復助戰與自有角色的背包實例編號衝突，導致自有角色被錯誤替換的問題。
- 恢復戰鬥時保留借用助戰的角色資料與實際選擇的欄位。

### ja_jp: 助っ人と戦闘復帰の修正

- 所持キャラクターと助っ人のインベントリ識別子が重複し、所持キャラクターが誤って置き換わる問題を修正しました。
- 戦闘再開時に、借りた助っ人のキャラクターデータと選択したスロットを保持します。

### ko_kr: 지원 캐릭터 및 전투 복구 수정

- 보유 캐릭터와 지원 캐릭터의 인벤토리 식별자가 중복되어 보유 캐릭터가 잘못 교체되는 문제를 수정했습니다.
- 전투 재개 시 빌린 지원 캐릭터의 데이터와 선택한 슬롯을 유지합니다.

## client 2.36.7+client.0.2.5 · 2026-10-20

> type: update

### en_us: Story replay and battle preparation

- Fix completed story replays incorrectly showing a story recovery failure.
- Preserve the combat party when borrowing a supporter fails before the first turn, preventing an empty formation and unresponsive battle controls.

### zh_cn: 剧情回放与战斗布阵修复

- 修复回放已完成剧情时误报剧情恢复失败的问题。
- 修复首回合开始前借用支援失败后队伍被清空、战斗布阵卡住的问题。

### zh_tw: 劇情回放與戰鬥佈陣修復

- 修復回放已完成劇情時誤報劇情恢復失敗的問題。
- 修復首回合開始前借用支援失敗後隊伍被清空、戰鬥佈陣卡住的問題。

### ja_jp: ストーリー再生と戦闘準備の修正

- クリア済みストーリーの再生時に、ストーリーの復元に失敗したと表示される問題を修正しました。
- 最初のターン開始前に助っ人の借用が失敗すると、編成が空になり戦闘画面を操作できなくなる問題を修正しました。

### ko_kr: 스토리 다시보기 및 전투 편성 수정

- 완료한 스토리를 다시 볼 때 스토리 복구 실패가 표시되는 문제를 수정했습니다.
- 첫 턴 시작 전에 지원 캐릭터 대여가 실패하면 편성이 비워지고 전투 화면이 멈추는 문제를 수정했습니다.

## client 2.36.7+client.0.2.4 · 2026-10-19

> type: update

### en_us: Download progress and story battle recovery

- Show game and resource download progress during preparation.
- Improve resource downloads and resume handling.
- Add BrownDustX character mod loading acceleration.
- Fix story battles getting stuck when the combat party is exhausted, including recovery and inn return.

### zh_cn: 下载进度与剧情战斗恢复

- 准备游戏时显示游戏和资源的下载进度。
- 改进资源下载与断点续传处理。
- 加入 BrownDustX 角色模组加载加速。
- 修复战斗队伍全部虚脱时剧情入战卡住，补全恢复与返回旅馆流程。

### zh_tw: 下載進度與劇情戰鬥恢復

- 準備遊戲時顯示遊戲與資源下載進度。
- 改善資源下載與續傳處理。
- 加入 BrownDustX 角色模組載入加速。
- 修復戰鬥隊伍全員虛脫時劇情入戰卡住，補全恢復與返回旅館流程。

### ja_jp: ダウンロード進捗とストーリー戦闘の復帰

- 準備中にゲームとリソースのダウンロード進捗を表示します。
- リソースのダウンロードと再開処理を改善しました。
- BrownDustX のキャラクターMOD読み込みを高速化します。
- 戦闘パーティー全員が戦闘不能の場合にストーリー戦闘へ進めなくなる問題を修正し、回復と宿屋への帰還処理を補いました。

### ko_kr: 다운로드 진행 상황 및 스토리 전투 복구

- 준비 중 게임과 리소스 다운로드 진행 상황을 표시합니다.
- 리소스 다운로드와 이어받기 처리를 개선했습니다.
- BrownDustX 캐릭터 모드 로딩 가속을 추가했습니다.
- 전투 파티 전원이 탈진한 경우 스토리 전투에서 멈추는 문제를 수정하고 회복 및 여관 복귀 흐름을 보완했습니다.

## client 2.36.7+client.0.2.3 · 2026-10-18

> type: update

### en_us: Launch reliability

- Launching and preparing the game now wait for a background update check or download to finish instead of failing with "Another update operation is in progress".
- Checking for updates waits for a running background check.
- Game and resource downloads started from Launch are no longer cut off after 12 seconds.
- The launcher log now records what each update check found, every file it downloads, the server's requirement and what was installed.

### zh_cn: 启动稳定性

- 启动和准备游戏时会等待后台的更新检查或下载完成，不再报“另一项更新操作正在进行”。
- 手动检查更新会等待正在进行的后台检查。
- 从“启动”触发的游戏与资源下载不再在 12 秒后被中断。
- 启动器日志会记录每次检查更新发现了什么、下载的每个文件、服务器的要求以及安装了什么。

### zh_tw: 啟動穩定性

- 啟動與準備遊戲時會等待背景的更新檢查或下載完成，不再出現「另一項更新操作正在進行」。
- 手動檢查更新會等待進行中的背景檢查。
- 從「啟動」觸發的遊戲與資源下載不再於 12 秒後中斷。
- 啟動器記錄會寫明每次檢查更新發現了什麼、下載的每個檔案、伺服器的要求以及安裝了什麼。

### ja_jp: 起動の安定性

- ゲームの起動と準備は、バックグラウンドの更新確認やダウンロードの完了を待つようになり、「別の更新操作が実行中です」で失敗しなくなりました。
- 手動の更新確認は、実行中のバックグラウンド確認の完了を待ちます。
- 起動から始まったゲームとリソースのダウンロードが 12 秒で中断されなくなりました。
- ランチャーのログに、更新確認で見つかった内容、ダウンロードした各ファイル、サーバーの要件、インストールした内容を記録します。

### ko_kr: 실행 안정성

- 게임 실행과 준비가 백그라운드 업데이트 확인이나 다운로드가 끝날 때까지 기다리며, 더 이상 '다른 업데이트 작업이 진행 중입니다'로 실패하지 않습니다.
- 수동 업데이트 확인은 진행 중인 백그라운드 확인을 기다립니다.
- 실행에서 시작된 게임 및 리소스 다운로드가 12초 후에 중단되지 않습니다.
- 런처 로그에 업데이트 확인 결과, 다운로드한 각 파일, 서버 요구 사항, 설치한 내용을 기록합니다.

## client 2.36.7+client.0.2.2 · 2026-10-18

> type: update

### en_us: Installation and compatibility diagnostics

- Allow a new installation location during first-time setup, then download and prepare the game directly from the launcher.
- Reuse verified downloads and installed files, repair missing or changed files individually, and avoid repeated catalog processing and unchanged configuration writes.
- Preserve launcher patches when preparing again and reuse complete BepInEx installations.
- Add detailed compatibility diagnostics for connection failures, invalid responses, plugin versions and game resource requirements.

### zh_cn: 安装准备与兼容性诊断

- 首次设置支持选择新的安装位置，无需提前安装游戏，进入启动器即可下载并准备。
- 复用已校验的下载与安装文件，按文件修复缺失或变化，避免重复处理资源目录及改写相同配置。
- 重复准备保留启动器补丁，并复用完整的 BepInEx 安装。
- 补全兼容性检查日志，区分连接失败、响应无效、插件版本及游戏资源要求。

### zh_tw: 安裝準備與相容性診斷

- 首次設定支援選擇新的安裝位置，無需預先安裝遊戲，進入啟動器即可下載並準備。
- 重用已驗證的下載與安裝檔案，逐檔修復缺少或變動的內容，避免重複處理資源目錄及寫入相同設定。
- 重複準備會保留啟動器修補，並重用完整的 BepInEx 安裝。
- 補全相容性檢查記錄，區分連線失敗、回應無效、插件版本及遊戲資源要求。

### ja_jp: インストールと互換性の診断

- 初回設定で新しいインストール先を選択し、ゲームがなくてもランチャーからダウンロードと準備を行えるようにしました。
- 検証済みのダウンロードとファイルを再利用し、欠落・変更されたファイルのみを修復します。カタログの重複処理と同一設定の書き直しを削減しました。
- 再準備時もランチャーのパッチを保持し、導入済みの BepInEx を再利用します。
- 接続失敗、不正な応答、プラグインのバージョン、ゲームリソースの要件を区別する診断ログを追加しました。

### ko_kr: 설치 준비 및 호환성 진단

- 처음 설정할 때 새 설치 위치를 선택하고, 게임이 없어도 런처에서 다운로드와 준비를 진행할 수 있습니다.
- 검증된 다운로드와 설치 파일을 재사용하고 누락되거나 변경된 파일만 복구합니다. 카탈로그 중복 처리와 동일한 설정 쓰기를 줄였습니다.
- 다시 준비할 때 런처 패치를 유지하고 완전히 설치된 BepInEx를 재사용합니다.
- 연결 실패, 잘못된 응답, 플러그인 버전 및 게임 리소스 요구 사항을 구분하는 진단 로그를 추가했습니다.

## server 2.36.7+server.0.2.0 · 2026-10-15

> type: update
> migration: none

### zh_cn: 玩法补全

- 装备支持制作后强化、制作后分解和强化后分解，补全装备仓库存取与升阶购买。
- 补全道具锁定、解锁、丢弃及仓库存取，保留数量和属性并检查使用限制。
- 补全角色升阶、临时伙伴到期清理、服装强化、万能强化及已解锁技能潜能的锁定与解锁。
- 伙伴关系随共同参战结果结算，支持羁绊阶段奖励及满关系伙伴剧情奖励，进度持久保存。
- 补全未完成战斗恢复、上一蓝方回合回退和放弃战斗；魔兽追踪、公会突袭支持作战记录与最高伤害统计。
- 镜中之战支持实际回合重放、进攻和防守阵容战绩分别重置；补全胜利阵容推荐、战力排序、已拥有角色筛选及分享设置。
- 补全广场每日奖励、个人及全服雕像击打统计、已结算赛季排名雕像展示和每日奖励。
- 捕鱼笼支持离线累计、奖励预览、领取鱼类或自动出售及钓鱼经验结算，修正出售价格取整。

### en_us: Gameplay additions

- Added craft-to-upgrade, craft-to-dismantle and upgrade-to-dismantle equipment actions, equipment storage and eligible rank-up purchases.
- Added item locking, unlocking, discarding and storage transfers, preserving quantities and properties and enforcing item restrictions.
- Added character promotion, expired temporary companion cleanup, costume and universal upgrades, and locking or unlocking acquired skill potentials.
- Partner relationships now settle with shared battle results. Solidarity milestone and maximum-relationship partner story rewards retain their progress.
- Added unfinished battle recovery, rollback to the previous allied turn and abandonment. Fiend Hunt and Guild Raid now support action records and best-damage statistics.
- Mirror Wars replays now use actual battle turns, with separate attack and defense record resets. Added winning-team recommendations, power sorting, an owned-character filter and sharing preferences.
- Added daily square rewards, personal and server-wide statue hit counts, settled-season ranking statue displays and daily rewards.
- Fishing traps now accumulate catches offline, preview rewards, deliver fish or sell them automatically, and award fishing experience. Corrected sale-price rounding.

### zh_tw: 玩法補全

- 裝備支援製作後強化、製作後分解和強化後分解，補全裝備倉庫存取與升階購買。
- 補全道具鎖定、解鎖、丟棄及倉庫存取，保留數量和屬性並檢查使用限制。
- 補全角色升階、臨時夥伴到期清理、服裝強化、萬能強化及已解鎖技能潛能的鎖定與解鎖。
- 夥伴關係隨共同參戰結果結算，支援羈絆階段獎勵及滿關係夥伴劇情獎勵，進度持久保存。
- 補全未完成戰鬥恢復、上一藍方回合回退和放棄戰鬥；魔獸追蹤、公會突襲支援作戰記錄與最高傷害統計。
- 鏡中之戰支援實際回合重播、進攻和防守陣容戰績分別重置；補全勝利陣容推薦、戰力排序、已擁有角色篩選及分享設定。
- 補全廣場每日獎勵、個人及全服雕像擊打統計、已結算賽季排名雕像展示和每日獎勵。
- 捕魚籠支援離線累積、獎勵預覽、領取魚類或自動出售及釣魚經驗結算，修正出售價格取整。

### ja_jp: ゲームプレイ機能の追加

- 装備の製作後強化、製作後分解、強化後分解に対応し、装備倉庫への出し入れと対象装備のランクアップ購入を追加しました。
- アイテムのロック、解除、廃棄、倉庫への移動を追加しました。数量と属性を保持し、使用制限を確認します。
- キャラクターの昇級、期限切れの一時的な仲間の整理、コスチューム強化、万能強化、解放済みスキル潜在能力のロックと解除を追加しました。
- 共に参加した戦闘結果に応じてパートナー関係を更新し、絆段階報酬と最大関係のパートナーストーリー報酬の進捗を保存します。
- 未完了の戦闘の復元、前の味方ターンへの巻き戻し、戦闘の放棄を追加しました。魔獣追跡とギルドレイドでは行動記録と最高ダメージを保存します。
- 鏡戦争のリプレイを実際のターンに対応させ、攻撃・防御編成の戦績を個別にリセットできます。勝利編成のおすすめ、戦闘力順、所持キャラクターのみの絞り込み、共有設定を追加しました。
- 広場のデイリー報酬、個人・サーバー全体の彫像攻撃回数、確定済みシーズンのランキング彫像とデイリー報酬を追加しました。
- 漁獲かごのオフライン蓄積、報酬確認、魚の受け取り・自動売却、釣り経験値の精算に対応し、売却価格の丸めを修正しました。

### ko_kr: 게임 플레이 기능 추가

- 장비 제작 후 강화, 제작 후 분해, 강화 후 분해를 지원하고 장비 창고 입출고와 대상 장비의 등급 상승 구매를 추가했습니다.
- 아이템 잠금, 잠금 해제, 버리기와 창고 이동을 추가했습니다. 수량과 속성을 유지하고 사용 제한을 확인합니다.
- 캐릭터 승급, 만료된 임시 동료 정리, 코스튬 강화와 만능 강화, 해금한 스킬 잠재력의 잠금 및 해제를 추가했습니다.
- 함께 참여한 전투 결과에 따라 파트너 관계를 정산하고, 유대 단계 보상과 최대 관계 파트너 스토리 보상 진행도를 저장합니다.
- 미완료 전투 복구, 이전 아군 턴으로 되돌리기와 전투 포기를 추가했습니다. 마물 추적과 길드 레이드는 행동 기록과 최고 피해량을 저장합니다.
- 거울전쟁 다시보기가 실제 전투 턴을 사용하며 공격·방어 편성 전적을 각각 초기화할 수 있습니다. 승리 편성 추천, 전투력 정렬, 보유 캐릭터 필터와 공유 설정을 추가했습니다.
- 광장 일일 보상, 개인 및 서버 전체 조각상 타격 횟수, 정산된 시즌의 순위 조각상 표시와 일일 보상을 추가했습니다.
- 통발의 오프라인 어획 누적, 보상 미리보기, 물고기 수령 또는 자동 판매와 낚시 경험치 정산을 추가하고 판매 가격 반올림을 수정했습니다.

## client 2.36.7+client.0.2.1 · 2026-10-18

> type: update

### en_us: Launcher and checkpoints

- Added one-click Prepare to the launcher: it downloads required game files, installs BepInEx from the package, applies the patch and installs plugins.
- Fixed the launcher showing "Updating" while it was only checking before launch.
- Fixed battle and story checkpoints reporting the wrong battle mode and talk state.
- Bundled third-party notices for BepInEx, HarmonyX, Harmony, MonoMod and Mono.Cecil.

### zh_cn: 启动器与检查点

- 启动器新增“一键准备”：按需下载游戏文件、从自带包安装 BepInEx、打补丁并安装插件。
- 修复启动游戏前仅在检查时误显示“正在更新”的问题。
- 修复战斗与剧情检查点上报的战斗模式和对话状态不正确的问题。
- 随包附带 BepInEx、HarmonyX、Harmony、MonoMod 与 Mono.Cecil 的第三方许可声明。

### zh_tw: 啟動器與檢查點

- 啟動器新增「一鍵準備」：視需要下載遊戲檔案、從內建包安裝 BepInEx、套用修補並安裝插件。
- 修正啟動遊戲前僅在檢查時誤顯示「正在更新」的問題。
- 修正戰鬥與劇情檢查點回報的戰鬥模式和對話狀態不正確的問題。
- 隨包附上 BepInEx、HarmonyX、Harmony、MonoMod 與 Mono.Cecil 的第三方授權聲明。

### ja_jp: ランチャーとチェックポイント

- ランチャーに「まとめて準備」を追加しました。必要なゲームファイルのダウンロード、同梱パッケージからの BepInEx のインストール、パッチ適用、プラグインのインストールを行います。
- 起動前の確認中に「更新中」と表示される問題を修正しました。
- 戦闘とストーリーのチェックポイントが誤った戦闘モードと会話状態を送信する問題を修正しました。
- BepInEx、HarmonyX、Harmony、MonoMod、Mono.Cecil のサードパーティ通知を同梱しました。

### ko_kr: 런처 및 체크포인트

- 런처에 '한 번에 준비'를 추가했습니다. 필요한 게임 파일 다운로드, 패키지의 BepInEx 설치, 패치 적용, 플러그인 설치를 수행합니다.
- 실행 전 확인 중에 '업데이트 중'으로 표시되던 문제를 수정했습니다.
- 전투 및 스토리 체크포인트가 잘못된 전투 모드와 대화 상태를 보내던 문제를 수정했습니다.
- BepInEx, HarmonyX, Harmony, MonoMod, Mono.Cecil의 서드파티 고지문을 포함했습니다.

## client 2.36.7+client.0.2.0 · 2026-10-15

> type: update

### en_us: Battle recovery

- Added battle checkpoints to support unfinished battle recovery and turn rollback.

### zh_cn: 战斗恢复

- 补全战斗检查点上报，支持未完成战斗恢复与回合回退。

### zh_tw: 戰鬥恢復

- 補全戰鬥檢查點回報，支援未完成戰鬥恢復與回合回退。

### ja_jp: 戦闘の復元

- 戦闘チェックポイントの送信を追加し、未完了の戦闘の復元とターンの巻き戻しに対応しました。

### ko_kr: 전투 복구

- 전투 체크포인트 전송을 추가하여 미완료 전투 복구와 턴 되돌리기를 지원합니다.
