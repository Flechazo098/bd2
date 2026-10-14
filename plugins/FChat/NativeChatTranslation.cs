using Bd2LoginUI;
using TextComponent = BD2.GameSdk.Component;
using System;
using System.Collections.Generic;
using System.Net;
using System.Runtime.Serialization;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using BD2.GameNames;
using Cysharp.Threading.Tasks;
using HarmonyLib;
using Network.TCP;
using NetworkHub.Client;
using Proto.Net;
using gamfs;

namespace Bd2FChat;

internal sealed class TranslationTestResult
{
    public bool Success;
    public string Message;
}

internal static class NativeChatTranslation
{
    private static readonly object Sync = new();
    private static CancellationTokenSource generation = new();
    private static int automaticGeneration;

    internal static void Install(Harmony harmony)
    {
        ChatTranslationProvider.Diagnostics = message => Plugin.Log?.LogInfo(message);
        NativeTranslationDisplay.Install(harmony);
        harmony.Patch(typeof(ChatSettingPacket).GetGameProperty(nameof(ChatSettingPacket.IsOnAutoTranslateChat),
                System.Reflection.BindingFlags.Public | System.Reflection.BindingFlags.Static)?.GetSetMethod() ??
            throw new MissingMethodException(nameof(ChatSettingPacket), nameof(ChatSettingPacket.IsOnAutoTranslateChat)),
            prefix: new HarmonyMethod(typeof(NativeChatTranslation), nameof(BeforeAutomaticChanged)),
            postfix: new HarmonyMethod(typeof(NativeChatTranslation), nameof(AutomaticChanged)));
        harmony.Patch(Game.Method(() => Guild.SendGuildChatTranslateRequest(0, null, 0, 0)),
            prefix: new HarmonyMethod(typeof(NativeChatTranslation), nameof(TranslatePrefix)));
        harmony.Patch(Game.Method<GuildChat>(chat => chat.ReceivedChatMessageCallback(null)),
            postfix: new HarmonyMethod(typeof(NativeChatTranslation), nameof(ReceivedGuildMessages)));
        harmony.Patch(Game.Method(() => Chat.SendRoomChatTranslateRequest(null, 0, null, 0, 0)),
            prefix: new HarmonyMethod(typeof(NativeChatTranslation), nameof(RoomTranslatePrefix)));
        harmony.Patch(Game.Method<MainChat>(chat => chat.ReceivedChatMessageCallback(null)),
            postfix: new HarmonyMethod(typeof(NativeChatTranslation), nameof(ReceivedGuildMessages)));
        harmony.Patch(Game.Method<PartyChat>(chat => chat.ReceivedChatMessageCallback(null)),
            postfix: new HarmonyMethod(typeof(NativeChatTranslation), nameof(ReceivedGuildMessages)));
    }

    private static void ReceivedGuildMessages(object __instance, List<IChatMessage> __0)
    {
        NativeTranslationDisplay.Track(__instance);
        AutoTranslate(__0).Forget();
    }

    private static void BeforeAutomaticChanged(out bool __state) => __state = ChatSettingPacket.IsOnAutoTranslateChat;

    private static void AutomaticChanged(bool __state)
    {
        bool enabled = ChatSettingPacket.IsOnAutoTranslateChat;
        if (enabled == __state || ClientSession.ServerOrigin == null) return;
        automaticGeneration++;
        foreach (object receiver in NativeTranslationDisplay.SetAutomatic(enabled))
            if (enabled) AutoTranslate(NativeTranslationDisplay.Messages(receiver), true).Forget();
    }

    private static async UniTask AutoTranslate(List<IChatMessage> messages, bool entirePage = false)
    {
        await UniTask.SwitchToMainThread();
        try
        {
            if (messages == null || ClientSession.ServerOrigin == null || !ChatSettingPacket.IsOnAutoTranslateChat) return;
            messages = new List<IChatMessage>(messages);
            TranslationConfig config = NativeTranslationSettings.Load();
            if (!config.Enabled) return;
            int automaticVersion = automaticGeneration;
            long self = BDNetwork.CommonPacket.UserInfo?.OwnerIndex ?? 0;
            for (int i = messages.Count - 1; i >= (entirePage ? 0 : Math.Max(0, messages.Count - 8)); i--)
            {
                if (automaticVersion != automaticGeneration || !ChatSettingPacket.IsOnAutoTranslateChat) return;
                if (messages[i] is ChatTextMsgData text && !text.IsTranslated && ChatTranslationProvider.MayTranslateOwner(config, text.SenderOwnerIndex, self) && !string.IsNullOrWhiteSpace(text.message))
                    await TranslateAndPublish(text.SenderOwnerIndex, text.message, text.SendTime, string.IsNullOrEmpty(text.RoomCode) ? null : text.RoomCode, true);
            }
        }
        catch (Exception ex) { Plugin.Log?.LogWarning("Automatic client translation unavailable: " + ex.GetType().Name); }
    }

    private static bool RoomTranslatePrefix(string __0, long __1, string __2, long __3, ref UniTask __result)
    {
        if (ClientSession.ServerOrigin == null) return true;
        __result = TranslateAndPublish(__1, __2, __3, __0 ?? string.Empty);
        return false;
    }

    private static bool TranslatePrefix(long __0, string __1, long __2, ref UniTask __result)
    {
        if (ClientSession.ServerOrigin == null) return true;
        __result = TranslateAndPublish(__0, __1, __2);
        return false;
    }

    private static async UniTask TranslateAndPublish(long owner, string text, long at, string roomCode = null, bool automatic = false)
    {
        await UniTask.SwitchToMainThread();
        int gameGeneration = ClientSession.RecoveryGeneration;
        int automaticVersion = automaticGeneration;
        CancellationToken configGeneration; lock (Sync) configGeneration = generation.Token;
        Define_LanguageType language = UserSettingInfo.LanguageGame;
        string translated = string.Empty;
        EErrorCode code = EErrorCode.ETranslateFail;
        try
        {
            TranslationConfig settings = NativeTranslationSettings.Load();
            long self = BDNetwork.CommonPacket.UserInfo?.OwnerIndex ?? 0;
            if (ChatTranslationProvider.MayTranslateOwner(settings, owner, self))
            {
                TranslationConfig config = NativeTranslationSettings.ForRequest(settings);
                TranslationResult result = await ChatTranslationProvider.TranslateCached(config, text,
                    ChatTranslationProvider.ResolveTarget(config, LanguageName(language)), configGeneration);
                if (result.Translated) { translated = result.Text; code = EErrorCode.ENone; }
                else if (automatic) return;
            }
            else if (automatic) return;
        }
        catch (Exception ex)
        {
            Plugin.Log?.LogWarning("Client chat translation unavailable: " + ex.GetType().Name);
        }
        await UniTask.SwitchToMainThread();
        if (ClientSession.IsQuitting || gameGeneration != ClientSession.RecoveryGeneration || configGeneration.IsCancellationRequested) return;
        if (automaticVersion != automaticGeneration || automatic && !ChatSettingPacket.IsOnAutoTranslateChat) return;
        if (language != UserSettingInfo.LanguageGame) { language = UserSettingInfo.LanguageGame; code = EErrorCode.ETranslateFail; translated = string.Empty; }
        byte[] encoded = ChatJson.Serialize(new[] { new NativeLanguageMessage { Lang = LanguageCode(language), Message = WebUtility.HtmlEncode(translated) } });
        string nativeMessage;
        try { nativeMessage = Encoding.UTF8.GetString(encoded); }
        finally { Array.Clear(encoded, 0, encoded.Length); }
        var header = new CommonHeader { Code = code, Now = DateTimeOffset.UtcNow.ToUnixTimeSeconds(), UserInfo = new UserInfo { OwnerIndex = owner } };
        if (SG.NetTCP != null)
        {
            if (roomCode == null)
            {
                var packet = new CSGuildChatTranslateResponse { Header = header, Message = nativeMessage, ServerNow = at };
                NativeTranslationDisplay.Queue(packet, owner, at, roomCode, translated, automatic);
                await SG.NetTCP.PublishReceivedPacket(ENetMsg.CSGuildChatTranslateResponse, packet);
            }
            else
            {
                var packet = new CSRoomTranslateResponse { Header = header, Message = nativeMessage, ServerNow = at, RoomCode = roomCode };
                NativeTranslationDisplay.Queue(packet, owner, at, roomCode, translated, automatic);
                await SG.NetTCP.PublishReceivedPacket(ENetMsg.CSRoomTranslateResponse, packet);
            }
        }
    }

    internal static async Task<TranslationTestResult> TestAsync(TranslationConfig draft, CancellationToken cancellation)
    {
        try
        {
            await UniTask.SwitchToMainThread(cancellation);
            TranslationConfig config = NativeTranslationSettings.ForRequest(draft);
            string output = await ChatTranslationProvider.Probe(config, cancellation).ConfigureAwait(false);
            await UniTask.SwitchToMainThread(CancellationToken.None);
            return new TranslationTestResult { Success = true, Message = TextComponent.Translatable("fchat.test.connected", output).GetString() };
        }
        catch (OperationCanceledException) { await UniTask.SwitchToMainThread(CancellationToken.None); return new TranslationTestResult { Message = TextComponent.Translatable("fchat.test.canceled").GetString() }; }
        catch (TranslationFailure ex) { await UniTask.SwitchToMainThread(CancellationToken.None); return new TranslationTestResult { Message = TextComponent.Translatable(ex.Message, ex.Arguments).GetString() }; }
        catch (ArgumentException ex) { await UniTask.SwitchToMainThread(CancellationToken.None); return new TranslationTestResult { Message = ex.Message }; }
        catch (Exception) { await UniTask.SwitchToMainThread(CancellationToken.None); return new TranslationTestResult { Message = TextComponent.Translatable("fchat.test.connection_failed").GetString() }; }
    }

    internal static void Reset(bool stopping = false)
    {
        automaticGeneration++;
        List<object> receivers = NativeTranslationDisplay.SetAutomatic(false);
        CancellationTokenSource previous;
        lock (Sync)
        {
            previous = generation;
            generation = new CancellationTokenSource();
        }
        previous.Cancel();
        previous.Dispose();
        ChatTranslationProvider.Reset(stopping || ClientSession.IsQuitting);
        NativeTranslationDisplay.Reset();
        if (!stopping && !ClientSession.IsQuitting)
            foreach (object receiver in receivers)
            {
                NativeTranslationDisplay.Track(receiver);
                AutoTranslate(NativeTranslationDisplay.Messages(receiver), true).Forget();
            }
    }

    private static string LanguageName(Define_LanguageType language) => language switch { Define_LanguageType.Korea => "Korean", Define_LanguageType.Japan => "Japanese", Define_LanguageType.Taiwan => "Traditional Chinese", Define_LanguageType.China => "Simplified Chinese", _ => "English" };
    private static string LanguageCode(Define_LanguageType language) => language switch { Define_LanguageType.Korea => "ko", Define_LanguageType.Japan => "ja", Define_LanguageType.Taiwan => "zh-TW", Define_LanguageType.China => "zh-CN", _ => "en" };
    [DataContract] private sealed class NativeLanguageMessage { [DataMember(Name = "lang")] public string Lang { get; set; } [DataMember(Name = "message")] public string Message { get; set; } }
}
