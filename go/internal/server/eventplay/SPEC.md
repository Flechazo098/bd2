# Event play runtime policy

Registry dates and UIDs are provided by server configuration. No calendar is
invented or enabled here. Every mutable operation is saved with its session,
path, sequence and exact request, and retries replay the complete response.
Production runs the economy and snapshot writes in one repository transaction.

Event stories use installed PackEventStoryTable rows. Seasonal clears and
archive replay clears have separate once-only reward ledgers. Event battles
use mode 17 and the client's custom stage fields 8/9, not monster field 3.
Installed PackEventBattleTable supplies the matching deck, win cost and repeat
rewards. Previous stages must be cleared. Failed/abandoned battle closes the
run without victory cost or rewards. Only server-proven stage clears are
returned by PackEventBattleInfo. BattleEnd reward bundle is field 5 and event
stage info is field 16.

Client-simulated minigames own a persistent run per session and family. Start
locks schedule and stage; End requires that run and commits its record before
the run closes. Rankings project local records, not official global scores.
Run/Field use FieldMiniGameRewardTable score thresholds; higher daily records
pay per-resource positive differences. Quick reward is only available for a
stored best score not already paid for that UTC day.

Field scores are computed from installed pack FieldMiniGameObjectTable IDs;
objects may be scored only once per run. Rhythm stage and mode select the
installed maximum score. Hopscotch captured area is range checked. Survival
start validates character/map and supplies the installed starting skill;
play coin/experience is recorded and End cannot exceed committed progress.
Upgrade costs come from FieldMiniGameUpgradeTable and currency 43. Skill
offers come from installed FieldMiniGameSkillGroupTable IDs. Defense stages
pay installed MGDRewardTable wave rewards once per saved record.

EventHub settings are projected from static PackEventListTable and configured
child schedules. Mini hub slots use their distinct protobuf shape. A registry
with no configured events yields empty Info responses rather than activating
unconfigured content.

This package intentionally does not turn a plain RandomBox reward into an
immediate opening. Reward type 9 remains inventory; manual box opening belongs
to the shared economy's box-opening operation.
