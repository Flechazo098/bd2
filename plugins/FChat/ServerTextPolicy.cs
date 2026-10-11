using TextComponent = BD2.GameSdk.Component;
using System;
using System.Reflection;
using BD2.GameNames;
using BDNetwork;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2FChat;

internal static class ServerTextPolicy
{
    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Type sqlManager = typeof(SQLManager);
        MethodInfo isBlockWord = sqlManager.GetGameMethod(
            nameof(SQLManager.IsBlockWord),
            BindingFlags.Static | BindingFlags.Public,
            null,
            [typeof(EBlockDBType), typeof(string), typeof(string).MakeByRefType()],
            null);
        MethodInfo wildBlockWord = sqlManager.GetGameMethod(
            nameof(SQLManager.WildBlockWord),
            BindingFlags.Static | BindingFlags.Public,
            null,
            [typeof(EBlockDBType), typeof(string)],
            null);
        MethodInfo packetErrorPopup = typeof(NetworkManager).GetGameMethod(
            nameof(NetworkManager.OpenPacketErrorPopup),
            BindingFlags.Instance | BindingFlags.Public,
            null,
            [typeof(int), typeof(int), typeof(bool)],
            null);
        if (isBlockWord == null || isBlockWord.ReturnType != typeof(bool) || !isBlockWord.GetParameters()[2].IsOut ||
            wildBlockWord == null || wildBlockWord.ReturnType != typeof(string) ||
            packetErrorPopup == null || packetErrorPopup.ReturnType != typeof(void))
        {
            throw new MissingMethodException("Client text-policy methods were not found (client version mismatch)");
        }

        harmony.Patch(isBlockWord, prefix: new HarmonyMethod(typeof(ServerTextPolicy), nameof(IsBlockWordPrefix)));
        harmony.Patch(wildBlockWord, prefix: new HarmonyMethod(typeof(ServerTextPolicy), nameof(WildBlockWordPrefix)));
        harmony.Patch(packetErrorPopup, prefix: new HarmonyMethod(typeof(ServerTextPolicy), nameof(PacketErrorPopupPrefix)));
        logger.LogInfo("Server text policy active: official word rejection and masking disabled");
    }

    private static bool IsBlockWordPrefix(string __1, ref string __2, ref bool __result)
    {
        if (__1 == null)
        {
            return true;
        }
        __2 = string.Empty;
        __result = false;
        return false;
    }

    private static bool WildBlockWordPrefix(string __1, ref string __result)
    {
        if (__1 == null)
        {
            return true;
        }
        __result = __1;
        return false;
    }

    private static bool PacketErrorPopupPrefix(int __1)
    {
        if (__1 != 9001 && __1 != 9002)
        {
            return true;
        }

        string message = TextComponent.Translatable(__1 == 9001 ? "fchat.policy.rejected" : "fchat.policy.unavailable").GetString();
        UIManager.OpenBarNotice(message, false, null);
        return false;
    }
}
