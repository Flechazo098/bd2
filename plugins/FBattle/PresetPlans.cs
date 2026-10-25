using System;
using System.Collections.Generic;
using System.Linq;
using BD2.GameNames;
using BDNetwork;
using gamfs;
using Google.Protobuf;
using Helper;
using HarmonyLib;
using Proto.Net;
using TextComponent = BD2.GameSdk.Component;

namespace Bd2FBattle;

internal static class PresetPlans
{
    private static readonly Dictionary<int, Dictionary<long, ActionChoice>> Plans = [];
    private static readonly Dictionary<int, HashSet<long>> Members = [];
    private static bool loaded;
    private static bool supported;
    private static int epoch;
    private static int? activeSlot;

    internal static void Install(Harmony harmony)
    {
        harmony.Patch(Game.Method<NetworkManager>(network => network.Send(null, null, true, null, false, null)),
            prefix: new HarmonyMethod(typeof(PresetPlans), nameof(BeforeSend)));
        harmony.Patch(Game.Method(() => CommonPacket.Reset()),
            prefix: new HarmonyMethod(typeof(PresetPlans), nameof(Reset)));
    }

    internal static Dictionary<long, ActionChoice> Get(int slot) => Plans.TryGetValue(slot, out Dictionary<long, ActionChoice> plan)
        ? plan.ToDictionary(pair => pair.Key, pair => pair.Value.Clone()) : [];

    internal static bool TryActive(IEnumerable<long> team, out Dictionary<long, ActionChoice> choices)
    {
        choices = null;
        if (!loaded || !supported || activeSlot == null || !Members.TryGetValue(activeSlot.Value, out HashSet<long> members) ||
            !members.SetEquals(team) || !Plans.TryGetValue(activeSlot.Value, out choices) || choices.Count == 0)
            return false;
        return true;
    }

    internal static void Load(Action success, Action<string> failure)
    {
        if (loaded)
        {
            if (supported) success?.Invoke();
            else failure?.Invoke(ServerRequired());
            return;
        }
        int account = epoch;
        SG.Net.Send(new PresetInfoRequest { Seq = Sequence.ReqSeq }, (data, _, error) =>
        {
            if (account != epoch) { failure?.Invoke(Failed()); return false; }
            if (error != 0 || !supported) { failure?.Invoke(error != 0 ? Failed() : ServerRequired()); return false; }
            success?.Invoke();
            return true;
        }, true, null, false, () => failure?.Invoke(Failed()));
    }

    internal static void Save(PresetDBInfo preset, int slot, IEnumerable<ActionChoice> choices, Action success, Action<string> failure)
    {
        if (!supported) { failure?.Invoke(ServerRequired()); return; }
        PresetDBInfo snapshot = PresetDBInfo.Parser.ParseFrom(ActionPlanWire.ReplacePlan(preset.ToByteArray(), choices));
        snapshot.Slot = slot;
        var request = new PresetSaveRequest { Seq = Sequence.ReqSeq, PresetInfo = snapshot };
        int account = epoch;
        SG.Net.Send(request, (data, packetCode, error) =>
        {
            if (account != epoch || error != 0) { failure?.Invoke(Failed()); return false; }
            if (!ActionPlanWire.TryRead(data, out Dictionary<long, ActionChoice> saved, out _)) { failure?.Invoke(ServerRequired()); return false; }
            Plans[slot] = saved;
            RememberMembers(snapshot);
            loaded = supported = true;
            BattleExecution.Reset();
            if (slot < CommonPacket.PresetInfoList.Count)
            {
                CommonPacket.PresetInfoList[slot] = snapshot;
                Prset.SyncCachedIntegratedPreset(EPresetMode.Normal, slot);
            }
            success?.Invoke();
            return true;
        }, true, null, false, () => failure?.Invoke(Failed()));
    }

    private static string ServerRequired() => TextComponent.Translatable("fbattle.server_required").GetString();
    private static string Failed() => TextComponent.Translatable("fbattle.request_failed").GetString();

    private static void Reset()
    {
        epoch++;
        loaded = supported = false;
        activeSlot = null;
        Plans.Clear();
        Members.Clear();
        BattleExecution.Reset();
    }

    private static void BeforeSend(IMessage __0, ref PacketRecvCallback __1, ref BatchPacketRecvCallback __3)
    {
        if (__0 is not (PresetInfoRequest or PresetSaveRequest or PresetUseRequest or PresetDeleteRequest)) return;
        IMessage request = __0;
        int account = epoch;
        if (__1 != null)
        {
            PacketRecvCallback original = __1;
            __1 = (data, code, error) =>
            {
                if (account == epoch && error == 0) ReceiveSafely(request, data);
                return original(data, code, error);
            };
        }
        if (__3 != null)
        {
            BatchPacketRecvCallback original = __3;
            __3 = (data, code, error) =>
            {
                if (account == epoch && error == 0) ReceiveSafely(request, data.ToByteArray());
                return original(data, code, error);
            };
        }
    }

    private static void ReceiveSafely(IMessage request, byte[] data)
    {
        try
        {
            switch (request)
            {
                case PresetInfoRequest:
                    ReceiveInfo(data);
                    break;
                case PresetSaveRequest save:
                    if (ActionPlanWire.TryRead(data, out Dictionary<long, ActionChoice> saved, out _))
                    {
                        Plans[save.PresetInfo.Slot] = saved;
                        RememberMembers(save.PresetInfo);
                        supported = true;
                        BattleExecution.Reset();
                    }
                    break;
                case PresetUseRequest use:
                    activeSlot = null;
                    if (ActionPlanWire.TryRead(data, out Dictionary<long, ActionChoice> applied, out int? slot))
                    {
                        if (slot != use.Slot) throw new InvalidOperationException("Preset action response slot mismatch");
                        activeSlot = slot;
                        Plans[use.Slot] = applied;
                        PresetDBInfo preset = CommonPacket.PresetInfoList.FirstOrDefault(p => p.Slot == use.Slot);
                        if (preset != null) RememberMembers(preset);
                        supported = true;
                        loaded = true;
                    }
                    BattleExecution.Reset();
                    break;
                case PresetDeleteRequest delete:
                    foreach (int deletedSlot in delete.Slot)
                    {
                        Plans.Remove(deletedSlot);
                        Members.Remove(deletedSlot);
                        if (activeSlot == deletedSlot) activeSlot = null;
                    }
                    BattleExecution.Reset();
                    break;
            }
        }
        catch (Exception error)
        {
            Reset();
            Plugin.Log?.LogWarning("Preset action configuration rejected: " + error.Message);
        }
    }

    private static void ReceiveInfo(byte[] data)
    {
        PresetInfoResponse response = PresetInfoResponse.Parser.ParseFrom(data);
        var next = new Dictionary<int, Dictionary<long, ActionChoice>>();
        bool available = false;
        foreach (PresetDBInfo preset in response.PresetInfo)
        {
            if (ActionPlanWire.TryRead(preset.ToByteArray(), out Dictionary<long, ActionChoice> plan, out _))
            {
                next.Add(preset.Slot, plan);
                available = true;
            }
        }
        ActionPlanWire.TryRead(data, out Dictionary<long, ActionChoice> capability, out int? selected);
        Plans.Clear();
        Members.Clear();
        foreach (KeyValuePair<int, Dictionary<long, ActionChoice>> pair in next) Plans.Add(pair.Key, pair.Value);
        foreach (PresetDBInfo preset in response.PresetInfo) RememberMembers(preset);
        activeSlot = selected;
        supported = available || capability != null;
        loaded = true;
    }

    private static void RememberMembers(PresetDBInfo preset) => Members[preset.Slot] = new HashSet<long>(
        preset.DeckInfo.Select(entry => entry.DeckBaseInfo.CharInvenIndex).Where(index => index > 0));
}
