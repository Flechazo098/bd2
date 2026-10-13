using System;
using System.Collections.Generic;
using System.Reflection.Emit;
using System.Runtime.CompilerServices;
using BD2.GameNames;
using HarmonyLib;
using Proto.Net;
using Google.Protobuf;
using Network.TCP;
using Cysharp.Threading.Tasks;

namespace Bd2FChat;

internal static class NativeTranslationDisplay
{
    private static readonly object Sync = new();
    private static readonly ConditionalWeakTable<IMessage, LocalPublication> Publications = [];
    private static int generation;
    private static readonly List<OwnedCache> OwnedCaches = [];
    [ThreadStatic] private static LocalPublication activePublication;
    [ThreadStatic] private static bool localCallback;

    internal static void Install(Harmony harmony)
    {
        foreach (System.Reflection.MethodInfo response in new[]
        {
            Game.Method<GameChatManager>(chat => chat.ReceivedPacket_CSGuildChatTranslateResponse_Callback(null)),
            Game.Method<GameChatManager>(chat => chat.ReceivedPacket_CSRoomTranslateResponse_Callback(null))
        })
            harmony.Patch(response,
                prefix: new HarmonyMethod(typeof(NativeTranslationDisplay), nameof(EnterResponse)),
                finalizer: new HarmonyMethod(typeof(NativeTranslationDisplay), nameof(ExitResponse)));
        foreach (System.Reflection.MethodInfo receiver in new[]
        {
            Game.Method<GuildChat>(chat => chat.ReceivedTranslatedChatMessageCallback(null, EErrorCode.ENone)),
            Game.Method<MainChat>(chat => chat.ReceivedTranslatedChatMessageCallback(null, EErrorCode.ENone)),
            Game.Method<PartyChat>(chat => chat.ReceivedTranslatedChatMessageCallback(null, EErrorCode.ENone))
        })
            harmony.Patch(receiver,
                prefix: new HarmonyMethod(typeof(NativeTranslationDisplay), nameof(EnterCallback)),
                transpiler: new HarmonyMethod(typeof(NativeTranslationDisplay), nameof(AllowLocalTranslation)),
                finalizer: new HarmonyMethod(typeof(NativeTranslationDisplay), nameof(ExitCallback)));
    }

    internal static void Queue(IMessage packet, long owner, long at, string room, string text, bool automatic)
    {
        var publication = new LocalPublication(owner, at, room ?? string.Empty, text, generation, automatic);
        lock (Sync) Publications.Add(packet, publication);
    }

    private static bool EnterResponse(IMessage __0, ref UniTask __result, out LocalPublication __state)
    {
        __state = activePublication;
        lock (Sync) Publications.TryGetValue(__0, out activePublication);
        if (activePublication != null && (activePublication.Generation != generation || activePublication.Automatic && !ChatSettingPacket.IsOnAutoTranslateChat)) { activePublication = null; __result = UniTask.CompletedTask; return false; }
        return true;
    }

    private static void ExitResponse(LocalPublication __state) => activePublication = __state;

    private static void EnterCallback(object __instance, IChatMessage __0, EErrorCode __1, out bool __state)
    {
        __state = localCallback;
        localCallback = false;
        if (__1 != EErrorCode.ENone || __0 is not ChatTextMsgData text || !text.IsTranslated) return;
        LocalPublication publication = activePublication;
        if (publication == null || publication.Owner != text.SenderOwnerIndex || publication.At != text.SendTime ||
            publication.Room != (text.RoomCode ?? string.Empty) || publication.Text != text.message) return;
        localCallback = true;
        OwnedCache cache = TrackReceiver(__instance);
        (long SenderOwnerIndex, long SendTime) key = (text.SenderOwnerIndex, text.SendTime);
        if (cache.Messages.TryGetValue(key, out string previous) && SwapCache(__instance).TryGetValue(key, out (IChatMessage, IChatMessage) pair) &&
            pair.Item2 is ChatTextMsgData cached && cached.message == previous) RestoreOriginal(__instance, key);
        cache.Messages[key] = text.message;
    }

    internal static void Reset()
    {
        generation++;
        foreach (OwnedCache cache in OwnedCaches)
        {
            object receiver = cache.Receiver.Target;
            if (receiver == null || receiver is UnityEngine.Object unity && unity == null) continue;
            Dictionary<(long, long), (IChatMessage, IChatMessage)> swaps = SwapCache(receiver);
            foreach (KeyValuePair<(long, long), string> entry in cache.Messages)
                if (swaps.TryGetValue(entry.Key, out (IChatMessage, IChatMessage) pair) && pair.Item2 is ChatTextMsgData translated && translated.message == entry.Value)
                    RestoreOriginal(receiver, entry.Key);
        }
        OwnedCaches.Clear();
    }

    private static OwnedCache TrackReceiver(object receiver)
    {
        OwnedCaches.RemoveAll(entry => !entry.Receiver.IsAlive);
        OwnedCache cache = OwnedCaches.Find(entry => ReferenceEquals(entry.Receiver.Target, receiver));
        if (cache == null) { cache = new OwnedCache(receiver); OwnedCaches.Add(cache); }
        return cache;
    }

    internal static void Track(object receiver)
    {
        OwnedCache cache = TrackReceiver(receiver);
        var current = new HashSet<(long, long)>();
        foreach (IChatMessage message in Messages(receiver)) current.Add((message.SenderOwnerIndex, message.SendTime));
        var expired = new List<(long, long)>();
        foreach ((long, long) key in cache.Messages.Keys) if (!current.Contains(key)) expired.Add(key);
        foreach ((long, long) key in expired) cache.Messages.Remove(key);
    }

    internal static List<IChatMessage> Messages(object receiver)
    {
        var messages = (List<IChatMessage>)receiver.GetType().GetGameField("_chatMsgList",
            System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic).GetValue(receiver);
        return new List<IChatMessage>(messages);
    }

    internal static List<object> SetAutomatic(bool enabled)
    {
        generation++;
        var receivers = new List<object>();
        foreach (OwnedCache cache in OwnedCaches)
        {
            object receiver = cache.Receiver.Target;
            if (receiver == null || receiver is UnityEngine.Object unity && unity == null) continue;
            receivers.Add(receiver);
            Dictionary<(long, long), (IChatMessage, IChatMessage)> swaps = SwapCache(receiver);
            foreach (KeyValuePair<(long, long), string> entry in cache.Messages)
                if (swaps.TryGetValue(entry.Key, out (IChatMessage, IChatMessage) pair) && pair.Item2 is ChatTextMsgData translated && translated.message == entry.Value)
                    receiver.GetType().GetGameMethod(enabled ? "ChangeChatMessageToTranslated" : "ChangeChatMessageToOrin",
                        System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic, null,
                        [typeof(long), typeof(long)], null).Invoke(receiver, [entry.Key.Item1, entry.Key.Item2]);
        }
        return receivers;
    }

    private static Dictionary<(long, long), (IChatMessage, IChatMessage)> SwapCache(object receiver) =>
        (Dictionary<(long, long), (IChatMessage, IChatMessage)>)receiver.GetType().GetGameField("_chatTranslatedAndOrinSwapDic",
            System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic).GetValue(receiver);

    private static void RestoreOriginal(object receiver, (long Owner, long At) key)
    {
        Dictionary<(long, long), (IChatMessage, IChatMessage)> swaps = SwapCache(receiver);
        if (!swaps.ContainsKey(key)) return;
        receiver.GetType().GetGameMethod("ChangeChatMessageToOrin",
            System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic, null,
            [typeof(long), typeof(long)], null).Invoke(receiver, [key.Owner, key.At]);
        swaps.Remove(key);
    }

    private static void ExitCallback(bool __state) => localCallback = __state;

    private static bool SameNativeLanguage(Define_LanguageType original, Define_LanguageType translated) =>
        !localCallback && original == translated;

    private static List<CodeInstruction> AllowLocalTranslation(IEnumerable<CodeInstruction> instructions)
    {
        var code = new List<CodeInstruction>(instructions);
        System.Reflection.FieldInfo field = AccessTools.Field(typeof(ChatTextMsgData), Game.MemberName(typeof(ChatTextMsgData), "MessageLanguage", GameMemberKind.Field));
        int matches = 0;
        for (int i = 4; i < code.Count; i++)
        {
            if (!code[i - 3].LoadsField(field) || !code[i - 1].LoadsField(field) ||
                (code[i].opcode != OpCodes.Bne_Un && code[i].opcode != OpCodes.Bne_Un_S)) continue;
            // Keep the native original/translated swap handling; bypass only its enum equality guard for our response.
            var compare = new CodeInstruction(OpCodes.Call, AccessTools.Method(typeof(NativeTranslationDisplay), nameof(SameNativeLanguage)));
            code[i].MoveLabelsTo(compare);
            code[i].MoveBlocksTo(compare);
            code[i].opcode = OpCodes.Brfalse;
            code.Insert(i++, compare);
            matches++;
        }
        if (matches != 1) throw new InvalidOperationException("Native chat translation language guard changed.");
        return code;
    }

    private sealed class LocalPublication(long owner, long at, string room, string text, int version, bool automatic)
    {
        internal readonly long Owner = owner;
        internal readonly long At = at;
        internal readonly string Room = room;
        internal readonly string Text = text;
        internal readonly int Generation = version;
        internal readonly bool Automatic = automatic;
    }

    private sealed class OwnedCache(object receiver)
    {
        internal readonly WeakReference Receiver = new(receiver);
        internal readonly Dictionary<(long, long), string> Messages = [];
    }
}
