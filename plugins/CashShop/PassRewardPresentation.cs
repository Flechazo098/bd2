using System;
using System.Collections;
using System.Collections.Generic;
using System.Linq;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;

namespace Bd2CashShop;

// Native pass callbacks save rewards but discard their presentation list.
// Capture that list after the native save and feed it to the native popup.
internal static class PassRewardPresentation
{
    private const BindingFlags All = BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Instance | BindingFlags.Static;
    [ThreadStatic] private static Capture current;
    private static Batch batch;

    private sealed class Capture
    {
        internal Capture Previous;
        internal readonly List<ItemBaseInfo> Rewards = [];
    }
    private sealed class Batch
    {
        internal readonly List<ItemBaseInfo> Rewards = [];
    }

    internal static void Install(Harmony harmony)
    {
        MethodInfo receiver = typeof(MissionPassPacket).GetNestedTypes(All)
            .SelectMany(type => type.GetMethods(All | BindingFlags.DeclaredOnly))
            .Single(method => method.IsGameMethod("<SendPassRewardRequest>b__1") && method.ReturnType == typeof(bool) &&
                method.GetParameters().Select(parameter => parameter.ParameterType)
                    .SequenceEqual((Type[])[typeof(byte[]), typeof(int), typeof(int)]));
        harmony.Patch(receiver,
            prefix: new HarmonyMethod(typeof(PassRewardPresentation), nameof(BeginReceive)),
            postfix: new HarmonyMethod(typeof(PassRewardPresentation), nameof(EndReceive)),
            finalizer: new HarmonyMethod(typeof(PassRewardPresentation), nameof(FinishReceive)));
        MethodInfo addRewards = typeof(DataManager).GetGameMethod("AddRewardInfoBundle", All, null,
            [typeof(Proto.Net.RewardDBInfoBundle), typeof(List<ItemBaseInfo>).MakeByRefType(), typeof(bool)], null) ?? throw new MissingMethodException("DataManager.AddRewardInfoBundle");
        harmony.Patch(addRewards, postfix: new HarmonyMethod(typeof(PassRewardPresentation), nameof(CaptureRewards)));
        MethodInfo receiveAll = typeof(PassRootUI).GetGameMethod("ReceiveAllReward", All) ?? throw new MissingMethodException("PassRootUI.ReceiveAllReward");
        harmony.Patch(receiveAll, postfix: new HarmonyMethod(typeof(PassRewardPresentation), nameof(WrapReceiveAll)));
        MethodInfo popup = typeof(MissionPassPacket).GetGameMethod("ShowRewardPopup", All) ?? throw new MissingMethodException("MissionPassPacket.ShowRewardPopup");
        harmony.Patch(popup, prefix: new HarmonyMethod(typeof(PassRewardPresentation), nameof(MergeBatch)));
    }

    private static void BeginReceive(out Capture __state)
    {
        __state = new Capture { Previous = current };
        current = __state;
    }
    private static void CaptureRewards(List<ItemBaseInfo> __1, bool __2)
    {
        if (current != null && __2 && __1 != null) current.Rewards.AddRange(__1);
    }
    private static void EndReceive(bool __result, int __2, Capture __state)
    {
        current = __state.Previous;
        if (!__result || __2 != 0 || __state.Rewards.Count == 0) return;
        if (batch != null) batch.Rewards.AddRange(__state.Rewards);
        else MissionPassPacket.ShowRewardPopup(__state.Rewards, "");
    }
    private static Exception FinishReceive(Exception __exception, Capture __state)
    {
        if (__state != null) current = __state.Previous;
        return __exception;
    }
    private static void WrapReceiveAll(ref IEnumerator __result) => __result = ReceiveAll(__result);
    private static IEnumerator ReceiveAll(IEnumerator original)
    {
        var owner = new Batch();
        Batch previous = batch;
        batch = owner;
        try
        {
            while (original.MoveNext()) yield return original.Current;
        }
        finally
        {
            try { (original as IDisposable)?.Dispose(); }
            finally { batch = previous; }
        }
    }
    private static void MergeBatch(ref List<ItemBaseInfo> __0)
    {
        if (batch == null || batch.Rewards.Count == 0) return;
        List<ItemBaseInfo> combined = __0 == null ? [] : new List<ItemBaseInfo>(__0);
        combined.AddRange(batch.Rewards);
        batch.Rewards.Clear();
        __0 = combined;
    }
}
