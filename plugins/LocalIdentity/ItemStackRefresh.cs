using System;
using System.Collections;
using System.Reflection;
using System.Threading;
using BD2.GameNames;
using BDNetwork;
using BepInEx.Logging;
using gamfs;
using Google.Protobuf;
using HarmonyLib;
using UnityEngine;

namespace Bd2LocalIdentity;

// The server reshapes item stacks as soon as the console switches an account's
// stack limit, and flags the next reply with Notify field 1000. The native
// client ignores that field; here it reloads the inventories the flag names, so
// merged or split stacks show up without a new login.
internal static class ItemStackRefresh
{
    private const int NotifyField = 1000;
    private const int RefreshItems = 1 << 0;
    private const int RefreshLife = 1 << 1;
    private static readonly FieldInfo KeepItems = typeof(CommonPacket).GetGameField("_itemKeepInfoDic", BindingFlags.Static | BindingFlags.NonPublic);
    private static ManualLogSource Log;
    private static int pending;
    private static bool reloadingStorage;

    internal static void Install(Harmony harmony, MonoBehaviour host, ManualLogSource logger)
    {
        Log = logger;
        if (KeepItems == null)
        {
            throw new MissingFieldException(nameof(CommonPacket), "_itemKeepInfoDic");
        }
        harmony.Patch(typeof(NetworkManager).GetGameMethod("OnAfterResponse", BindingFlags.Instance | BindingFlags.NonPublic,
                null, [typeof(PacketData), typeof(ResponseData)], null),
            postfix: new HarmonyMethod(typeof(ItemStackRefresh), nameof(AfterResponse)));
        harmony.Patch(Game.Method(() => InventoryPacket.RecvItemStorageInfo(null, 0, 0)),
            prefix: new HarmonyMethod(typeof(ItemStackRefresh), nameof(BeforeStorage)));
        host.StartCoroutine(Reload());
    }

    private static void AfterResponse(ResponseData __1)
    {
        int flags = Flags(__1?.notify);
        for (int seen = Volatile.Read(ref pending); flags != 0 && (seen & flags) != flags;)
        {
            int previous = Interlocked.CompareExchange(ref pending, seen | flags, seen);
            if (previous == seen)
            {
                break;
            }
            seen = previous;
        }
    }

    private static int Flags(string notify)
    {
        if (string.IsNullOrEmpty(notify))
        {
            return 0;
        }
        try
        {
            using var input = new CodedInputStream(Convert.FromBase64String(notify));
            for (uint tag; (tag = input.ReadTag()) != 0;)
            {
                if (tag == WireFormat.MakeTag(NotifyField, WireFormat.WireType.Varint))
                {
                    return (int)input.ReadUInt64();
                }
                input.SkipLastField();
            }
        }
        catch (Exception ex) when (ex is FormatException or InvalidProtocolBufferException)
        {
            // The game itself reports a malformed notify.
        }
        return 0;
    }

    // Item storage replies add to the counts the client already holds, so the
    // reload starts storage over; the item info reply already does that itself.
    private static void BeforeStorage(int __2)
    {
        if (reloadingStorage && __2 <= 0)
        {
            ((IDictionary)KeepItems.GetValue(null)).Clear();
        }
    }

    private static IEnumerator Reload()
    {
        while (true)
        {
            yield return null;
            if (Volatile.Read(ref pending) == 0 || SG.Net == null ||
                SG.Net.IsBatchModeState != NetworkManager.BatchModeState.NotWorking)
            {
                continue;
            }
            int flags = Interlocked.Exchange(ref pending, 0);
            Log.LogInfo("Reloading inventory after an item stack change: flags=" + flags);
            if ((flags & RefreshItems) != 0)
            {
                reloadingStorage = true;
                using (new NetworkManager.BatchObject(() => reloadingStorage = false))
                {
                    CommonPacket.SendItemInfoRequest();
                    InventoryPacket.SendItemStorageInfoRequest();
                }
            }
            if ((flags & RefreshLife) != 0)
            {
                LifePacket.SendLifeInfoRequest(null);
            }
        }
    }
}
