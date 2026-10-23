using System;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using gamfs;
using TMPro;
using TextComponent = BD2.GameSdk.Component;

namespace Bd2FChat;

internal static class FriendPresence
{
    private static FieldInfo friendInfo;
    private static FieldInfo introduction;

    internal static void Install(Harmony harmony)
    {
        const BindingFlags fields = BindingFlags.Instance | BindingFlags.NonPublic;
        friendInfo = typeof(FriendItem).GetGameField("_friendInfoData", fields)
            ?? throw new MissingFieldException(nameof(FriendItem), "_friendInfoData");
        introduction = typeof(FriendItem).GetGameField("_textIntroduction", fields)
            ?? throw new MissingFieldException(nameof(FriendItem), "_textIntroduction");
        MethodInfo refresh = typeof(FriendItem).GetGameMethod("RefreshLoginUI", fields)
            ?? throw new MissingMethodException(nameof(FriendItem), "RefreshLoginUI");
        harmony.Patch(refresh, prefix: new HarmonyMethod(typeof(FriendPresence), nameof(RefreshLoginPrefix)));
    }

    private static bool RefreshLoginPrefix(FriendItem __instance)
    {
        if (friendInfo.GetValue(__instance) is not FriendPacket.FriendInfoData info) return true;
        var label = introduction.GetValue(__instance) as TMP_Text;
        if (label == null || SG.Time.UnixTimeStampToDateTime(info.LastLoginDate) <= SG.Time.Now()) return true;
        label.richText = true;
        label.text = "<color=#63D189>● " + TextComponent.Translatable("fchat.friend.online").GetString() + "</color>";
        return false;
    }
}
