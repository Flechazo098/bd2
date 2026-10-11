using TextComponent = BD2.GameSdk.Component;
using TMPro;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FChat;

internal static class TranslationEntryLayout
{
    private const string EntryName = "BD2 AI Translation Settings";

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
    internal static void Ensure(ButtonOnOffComponent toggle, bool guild)
    {
        if (toggle == null) return;
        Transform parent = toggle.transform.parent;
        Transform existing = parent.Find(EntryName);
        if (existing != null)
        {
            NativeTranslationControls.SetText(existing.gameObject, TextComponent.Translatable("fchat.ai_settings").GetString());
            return;
        }
        var toggleRect = (RectTransform)toggle.transform;
        Vector2 position = toggleRect.anchoredPosition;
        GameObject entry = Object.Instantiate(toggle.gameObject, parent, false);
        entry.name = EntryName;
        foreach (ButtonOnOffComponent state in entry.GetComponents<ButtonOnOffComponent>()) state.enabled = false;
        Transform checkbox = entry.transform.Find("CheckBox");
        if (checkbox != null) checkbox.gameObject.SetActive(false);
        CanvasGroup canvas = entry.GetComponent<CanvasGroup>() ?? entry.AddComponent<CanvasGroup>();
        canvas.alpha = 1;
        canvas.ignoreParentGroups = true;
        canvas.interactable = canvas.blocksRaycasts = true;
        Button click = entry.GetComponent<Button>() ?? entry.AddComponent<Button>();
        click.onClick = new Button.ButtonClickedEvent();
        click.onClick.AddListener(TranslationSettingsUI.Open);
        click.interactable = true;
        if (guild)
        {
            Transform tab = parent.parent;
            NativeTranslationControls.DisableLayout(tab);
            ((RectTransform)tab).SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, 420);
            LayoutElement tabSize = tab.GetComponent<LayoutElement>() ?? tab.gameObject.AddComponent<LayoutElement>();
            tabSize.minWidth = tabSize.preferredWidth = 420;
            tabSize.flexibleWidth = 0;
            NativeTranslationControls.DisableLayout(parent);
            var caption = (RectTransform)parent;
            caption.anchorMin = caption.anchorMax = caption.pivot = new Vector2(0, .5f);
            caption.anchoredPosition = new Vector2(25, 0);
            caption.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, 100);
            NativeTranslationControls.Place(toggle.transform, parent, 140, 0, 126, 32);
            NativeTranslationControls.Place(entry.transform, parent, 255, 0, 90, 32);
        }
        else if (parent.name == "TopButton")
        {
            NativeTranslationControls.DisableLayout(toggle.transform);
            toggleRect.anchorMin = toggleRect.anchorMax = toggleRect.pivot = new Vector2(.5f, .5f);
            toggleRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, 130);
            toggleRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, 48);
            toggleRect.anchorMin = toggleRect.anchorMax = new Vector2(0, 1);
            toggleRect.anchoredPosition = new Vector2(190, -35);
            RectTransform rect = NativeTranslationControls.Place(entry.transform, parent, 0, 0, 90, 42);
            rect.anchorMin = rect.anchorMax = new Vector2(0, 1);
            rect.anchoredPosition = new Vector2(305, -35);
        }
        else
        {
            NativeTranslationControls.DisableLayout(toggle.transform);
            float originalWidth = toggleRect.rect.width;
            float left = position.x - toggleRect.pivot.x * originalWidth;
            float y = position.y + (.5f - toggleRect.pivot.y) * toggleRect.rect.height;
            left -= (240 - originalWidth) * toggleRect.anchorMin.x;
            toggleRect.pivot = new Vector2(0, .5f);
            toggleRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, 140);
            toggleRect.anchoredPosition = new Vector2(left, y);
            RectTransform rect = NativeTranslationControls.Place(entry.transform, parent, 0, 0, 90, 42);
            rect.anchorMin = toggleRect.anchorMin;
            rect.anchorMax = toggleRect.anchorMax;
            rect.anchoredPosition = new Vector2(left + 195, y);
        }
        foreach (TMP_Text label in entry.GetComponentsInChildren<TMP_Text>(true))
        {
            NativeTranslationControls.Place(label.transform, entry.transform, 0, 0, 86, 28);
            label.fontSize = 18;
            label.enableAutoSizing = false;
            label.alignment = TextAlignmentOptions.Center;
            label.richText = false;
            label.text = TextComponent.Translatable("fchat.ai_settings").GetString();
        }
        entry.SetActive(true);
    }
}
