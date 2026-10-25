using System;
using System.Linq;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FBattle;

[System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
internal static class PresetEditorEntry
{
    private static AddressableAsset<GameObject> battleAsset;
    private static bool loading, quitting;
    private static readonly string[] BottomButtons = ["_buttonReset", "_buttonCancel", "_buttonOk"];

    internal static void Install(Harmony harmony)
    {
        Application.quitting += Release;
        harmony.Patch(Method(typeof(CharManageUI), "OpenCharManageUI"), postfix: new HarmonyMethod(typeof(PresetEditorEntry), nameof(Ready)));
        harmony.Patch(Method(typeof(CharManageUI), "SetTabCharManageUI"), postfix: new HarmonyMethod(typeof(PresetEditorEntry), nameof(Ready)));
        harmony.Patch(Method(typeof(GreetingPopupUI), "OnClickUI", typeof(GameObject)), prefix: new HarmonyMethod(typeof(PresetEditorEntry), nameof(PopupClick)));
        harmony.Patch(Method(typeof(UIBase), "CloseUI"), prefix: new HarmonyMethod(typeof(PresetEditorEntry), nameof(PopupClose)));
    }

    private static MethodInfo Method(Type owner, string name, params Type[] parameters)
    {
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.DeclaredOnly;
        return (parameters.Length == 0 ? owner.GetGameMethod(name, flags) : owner.GetGameMethod(name, flags, null, parameters, null)) ??
            throw new MissingMethodException(owner.Name, name);
    }

    private static void Ready(CharManageUI __instance)
    {
        if (__instance.OpenFromType != CharManageUI.ECharManageUIOpenFrom.FromPresetSetting) return;
        try
        {
            PresetEditorButton current = __instance.GetComponent<PresetEditorButton>();
            if (current != null) { current.Refresh(); return; }
            Button[] row = BottomButtons.Select(name => NativeActionControls.Field<Button>(NativeActionControls.Field<object>(__instance, name), "_button")).ToArray();
            Button template = row[2] ?? row[0];
            if (template == null || row[0] == null) throw new MissingMemberException("Preset bottom buttons unavailable.");
            Vector2 size = ((RectTransform)template.transform).rect.size;
            Button entry = NativeActionControls.Button(template.gameObject, template.transform.parent, "FBattle Edit Actions",
                ActionCatalog.Text("fbattle.entry"), size, () => Open(__instance));
            IconFree(entry.transform);
            __instance.gameObject.AddComponent<PresetEditorButton>().Initialize(__instance, entry, row);
        }
        catch (Exception error) { Plugin.Log?.LogWarning("FBattle preset entry: " + error); }
    }

    // The bottom buttons carry an icon (reset arrow, check mark) that would
    // mislabel the entry, so drop it and centre the title in its place.
    private static void IconFree(Transform button)
    {
        foreach (Transform state in button)
        {
            if (state.name == "Image - Disable") { state.gameObject.SetActive(false); continue; }
            if (state.name != "Image - Enable") continue;
            Transform icon = state.Find("Image - Bg");
            if (icon != null) icon.gameObject.SetActive(false);
        }
        foreach (Transform child in button)
        {
            if (child.name == "Image - Bg") child.gameObject.SetActive(false);
            if (child.name != "Text - Title" || child is not RectTransform title) continue;
            title.offsetMin = new Vector2(12, title.offsetMin.y);
            title.offsetMax = new Vector2(-12, title.offsetMax.y);
        }
    }

    private static void Open(CharManageUI owner)
    {
        if (quitting || loading || UIManager.IsShowingNetworkErrorPopup) return;
        if (owner.DeckList.Count == 0) { UIManager.OpenBarNotice(ActionCatalog.Text("fbattle.empty"), false, null); return; }
        loading = true;
        PresetPlans.Load(() =>
        {
            if (quitting || owner == null || !owner.isActiveAndEnabled) { loading = false; return; }
            if (battleAsset != null) { loading = false; Show(owner); return; }
            try
            {
                NativeActionControls.LoadBattlePrefab(asset =>
                {
                    loading = false;
                    if (quitting) { asset.Release(); return; }
                    battleAsset = asset;
                    if (owner != null && owner.isActiveAndEnabled) Show(owner);
                }, Failed);
            }
            catch (Exception error) { Failed(error); }
        }, error => Failed(new InvalidOperationException(error)));
    }

    private static void Show(CharManageUI owner)
    {
        try
        {
            var controls = new NativeActionControls(battleAsset.Asset, owner);
            UIManager.AddUI<GreetingPopupUI>(popup =>
            {
                if (quitting || owner == null || !owner.isActiveAndEnabled) { popup.CloseForceUI(true); return; }
                try { popup.gameObject.AddComponent<PresetActionEditor>().Initialize(popup, owner, controls); }
                catch (Exception error) { popup.CloseForceUI(true); Failed(error); }
            }, "GreetingPopupUI", true, "");
        }
        catch (Exception error) { Failed(error); }
    }

    private static void Failed(Exception error)
    {
        loading = false;
        if (quitting) return;
        UIManager.OpenBarNotice(ActionCatalog.Text("fbattle.open_failed"), false, null);
        Plugin.Log?.LogWarning("FBattle preset editor: " + error);
    }

    private static bool PopupClick(GreetingPopupUI __instance, GameObject __0)
    {
        PresetActionEditor panel = __instance.GetComponent<PresetActionEditor>();
        if (panel == null) return true;
        panel.Click(__0);
        return false;
    }

    private static bool PopupClose(UIBase __instance)
    {
        if (__instance is not GreetingPopupUI) return true;
        PresetActionEditor panel = __instance.GetComponent<PresetActionEditor>();
        if (panel == null || panel.AllowClose) return true;
        panel.RequestClose();
        return false;
    }

    private static void Release() { quitting = true; battleAsset?.Release(); battleAsset = null; }
}

// Keeps the entry just left of the leftmost visible bottom button; which of
// them shows depends on the editor's state and layout.
[System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
[System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes these lifecycle callbacks.")]
internal sealed class PresetEditorButton : MonoBehaviour
{
    private const float Spacing = 20;
    private CharManageUI owner;
    private Button entry;
    private Button[] row;

    internal void Initialize(CharManageUI editor, Button button, Button[] bottom)
    { owner = editor; entry = button; row = bottom; Refresh(); }

    internal void Refresh()
    {
        if (entry == null || owner == null) return;
        RectTransform anchor = null;
        foreach (Button button in row)
        {
            if (button == null || !button.gameObject.activeInHierarchy) continue;
            var rect = (RectTransform)button.transform;
            if (anchor == null || rect.position.x < anchor.position.x) anchor = rect;
        }
        bool show = owner.OpenFromType == CharManageUI.ECharManageUIOpenFrom.FromPresetSetting && anchor != null;
        if (entry.gameObject.activeSelf != show) entry.gameObject.SetActive(show);
        if (!show) return;
        var own = (RectTransform)entry.transform;
        if (own.parent != anchor.parent) own.SetParent(anchor.parent, false);
        own.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, anchor.rect.width);
        own.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, anchor.rect.height);
        own.position = anchor.position - anchor.right * (anchor.rect.width + Spacing) * anchor.lossyScale.x;
        entry.interactable = true;
    }

    private void LateUpdate() => Refresh();
    private void OnDestroy() { if (entry != null) Destroy(entry.gameObject); }
}
