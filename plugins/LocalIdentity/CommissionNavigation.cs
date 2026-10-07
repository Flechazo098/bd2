using System.Reflection;
using BD2.GameNames;
using BepInEx.Logging;
using HarmonyLib;
using Proto.Net;
using gamfs;
using static BD2.GameNames.Game;

namespace Bd2LocalIdentity;

// Cancel the leftover arrival callback after a commission is turned in manually;
// the game's ClearQuestNav(false) would otherwise replay its dialogue.
internal static class CommissionNavigation
{
    private static ManualLogSource Log;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Log = logger;
        MethodInfo target = typeof(QuestPacket).GetGameMethod("RefreshQuestNavOnQuestClear", BindingFlags.Static | BindingFlags.NonPublic);
        harmony.Patch(target, prefix: new HarmonyMethod(typeof(CommissionNavigation), nameof(CancelCompletedTalkDestination)));
    }

    private static void CancelCompletedTalkDestination(QuestDTO __0)
    {
        if (__0 == null || __0.Type != (int)EQuestType.Today || __0.ConditionType != (int)Define_QuestConditionType.TalkManual)
        {
            return;
        }
        QuestNavigationManager navigation = Singleton<QuestNavigationManager>.Instance;
        if (navigation.IsNavigate && navigation.NavigateQuestId == __0.Id)
        {
            navigation.ClearQuestNav(true);
            Log.LogInfo("Cancelled completed commission talk destination: quest=" + __0.Id);
        }
    }
}
