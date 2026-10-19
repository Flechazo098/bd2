# 更新日志

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
