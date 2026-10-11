using Bd2LoginUI;
using TextComponent = BD2.GameSdk.Component;
using System;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FChat;

internal static class TranslationSettingsUI
{
    private const BindingFlags Fields = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;

    internal static void Install(Harmony harmony)
    {
        harmony.Patch(Declared(typeof(ChatSettingPopupUI), "SetChatSetting"),
            postfix: new HarmonyMethod(typeof(TranslationSettingsUI), nameof(ChatSettingsReady)));
        harmony.Patch(Declared(typeof(GreetingPopupUI), "OnClickUI", typeof(GameObject)),
            prefix: new HarmonyMethod(typeof(TranslationSettingsUI), nameof(ConfigClick)));
        foreach (Type owner in new[] { typeof(GuildChat), typeof(MainChat), typeof(PartyChat), typeof(ActionPartyPopupUI), typeof(DefensePartyPopupUI), typeof(AvatarFishingPartyPopupUI) })
            harmony.Patch(Declared(owner, "SetAutoTranslateButton"),
                postfix: new HarmonyMethod(typeof(TranslationSettingsUI), nameof(TranslationRowReady)));
    }

    private static MethodInfo Declared(Type type, string method, params Type[] parameters) =>
        type.GetGameMethod(method, Fields | BindingFlags.DeclaredOnly, null, parameters, null) ??
        throw new MissingMethodException(type.Name, method);

    private static void TranslationRowReady(Component __instance)
    {
        if (ClientSession.ServerOrigin == null) return;
        try
        {
            object field = __instance.GetType().GetGameField("_objAutoTranslateButton", Fields)?.GetValue(__instance) ??
                __instance.GetType().GetGameField("_objAutoTranslationButton", Fields)?.GetValue(__instance);
            if (field is ButtonOnOffComponent[] buttons) foreach (ButtonOnOffComponent button in buttons) TranslationEntryLayout.Ensure(button, __instance is GuildChat);
            else if (field is ButtonOnOffComponent button) TranslationEntryLayout.Ensure(button, false);
            else throw new MissingMemberException(__instance.GetType().Name, "translation button");
        }
        catch (Exception error)
        {
            Plugin.Log?.LogWarning("Native translation row: " + __instance.GetType().Name + ": " + error.GetType().Name);
        }
    }

    internal static T Field<T>(object target, string name) where T : class =>
        target.GetType().GetGameField(name, Fields)?.GetValue(target) as T;

    private static void ChatSettingsReady(ChatSettingPopupUI __instance)
    {
        if (ClientSession.ServerOrigin == null) return;
        try
        {
            Transform existing = __instance.transform.Find("BD2 Translation Settings");
            if (existing != null) { NativeTranslationControls.SetText(existing.gameObject, TextComponent.Translatable("fchat.ai_settings").GetString()); return; }
            ButtonOnOffComponent toggle = Field<ButtonOnOffComponent>(__instance, "_chatAutoTranslateButton");
            GameObject template = Field<GameObject>(__instance, "_objBlockListButton");
            if (toggle == null || template == null) throw new MissingMemberException("Chat translation controls are unavailable.");
            GameObject entry = UnityEngine.Object.Instantiate(template, __instance.transform, false);
            entry.name = "BD2 Translation Settings";
            var rect = (RectTransform)entry.transform;
            NativeTranslationControls.DisableLayout(entry.transform);
            rect.anchorMin = rect.anchorMax = rect.pivot = new Vector2(.5f, .5f);
            rect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, 130);
            NativeTranslationControls.DisableLayout(toggle.transform);
            var toggleRect = (RectTransform)toggle.transform;
            toggleRect.anchoredPosition = new Vector2(-185, toggleRect.anchoredPosition.y);
            entry.AddComponent<TranslationSettingsEntry>().Follow(toggle.transform);
            Transform rowTitle = toggle.transform.parent.Find("Text - Title");
            if (rowTitle != null) { NativeTranslationControls.DisableLayout(rowTitle); ((RectTransform)rowTitle).SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, 220); }
            NativeTranslationControls.SetText(entry, TextComponent.Translatable("fchat.ai_settings").GetString());
            Button button = entry.GetComponent<Button>();
            button.onClick = new Button.ButtonClickedEvent();
            button.onClick.AddListener(Open);
            button.interactable = true;
            entry.SetActive(true);
        }
        catch (Exception error)
        {
            Plugin.Log?.LogWarning("Native translation settings entry: " + __instance.GetType().Name + ": " + error.GetType().Name);
        }
    }

    internal static void Open()
    {
        if (ClientSession.ServerOrigin == null) return;
        UIManager.AddUI<GreetingPopupUI>(popup =>
    {
        try { popup.gameObject.AddComponent<TranslationSettingsPanel>().Initialize(popup); }
        catch (Exception error)
        {
            popup.CloseUI();
            Plugin.Log?.LogWarning("Native translation settings panel: " + error.GetType().Name);
        }
    }, "GreetingPopupUI", true, "");
    }

    private static bool ConfigClick(GreetingPopupUI __instance, GameObject __0)
    {
        TranslationSettingsPanel panel = __instance.GetComponent<TranslationSettingsPanel>();
        if (panel == null) return true;
        panel.Click(__0);
        return false;
    }
}

internal sealed class TranslationSettingsEntry : MonoBehaviour
{
    private Transform toggle;
    internal void Follow(Transform target) { toggle = target; LateUpdate(); }
    private void LateUpdate()
    {
        if (toggle == null) return;
        transform.position = toggle.position + toggle.right * (120 * toggle.lossyScale.x);
    }
}
