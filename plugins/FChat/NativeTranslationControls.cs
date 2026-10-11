using System;
using TMPro;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FChat;

internal static class NativeTranslationControls
{
    internal static void SetText(GameObject control, string value)
    {
        foreach (TMP_Text text in control.GetComponentsInChildren<TMP_Text>(true)) text.text = value;
    }

    internal static RectTransform Place(Transform transform, Transform parent, float x, float y, float width, float height)
    {
        transform.SetParent(parent, false);
        var rect = (RectTransform)transform;
        DisableLayout(transform);
        rect.anchorMin = rect.anchorMax = rect.pivot = new Vector2(.5f, .5f);
        rect.anchoredPosition = new Vector2(x, y);
        rect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, width);
        rect.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, height);
        foreach (LayoutElement layout in transform.GetComponents<LayoutElement>()) layout.ignoreLayout = true;
        return rect;
    }

    internal static Button Button(GameObject template, Transform parent, string name, string title, float x, Action click)
    {
        GameObject clone = UnityEngine.Object.Instantiate(template, parent, false);
        clone.name = name;
        foreach (MultiPlatformInputBinder shortcut in clone.GetComponentsInChildren<MultiPlatformInputBinder>(true))
        {
            shortcut.enabled = false;
            UnityEngine.Object.Destroy(shortcut);
        }
        Place(clone.transform, parent, x, -255, 160, 56);
        SetText(clone, title);
        Button button = clone.GetComponent<Button>();
        button.onClick = new Button.ButtonClickedEvent();
        button.onClick.AddListener(() => click());
        button.interactable = true;
        clone.SetActive(true);
        return button;
    }

    internal static TMP_InputField Input(TMP_InputField template, Transform parent, Sprite background, string name,
        string value, string placeholder, float y, bool password)
    {
        GameObject clone = UnityEngine.Object.Instantiate(template.transform.parent.gameObject, parent, false);
        clone.name = name;
        foreach (Transform child in clone.GetComponentsInChildren<Transform>(true)) DisableLayout(child);
        Place(clone.transform, parent, 0, y, 680, 64);
        Image box = clone.GetComponent<Image>();
        box.sprite = background;
        box.type = Image.Type.Sliced;
        box.color = new Color(.18f, .22f, .28f, 1);
        box.raycastTarget = false;
        TMP_InputField input = clone.GetComponentInChildren<TMP_InputField>(true);
        Stretch((RectTransform)input.transform, 0, 0);
        input.GetComponent<Image>().color = Color.clear;
        Stretch(input.textViewport, 20, 10);
        Stretch(input.textComponent.rectTransform, 0, 0);
        FormatInputText(input.textComponent, new Color(.94f, .94f, .92f, 1));
        input.pointSize = 24;
        input.customCaretColor = true;
        input.caretColor = input.textComponent.color;
        input.caretWidth = 2;
        input.selectionColor = new Color(.35f, .55f, .8f, .5f);
        input.richText = false;
        input.onValueChanged = new TMP_InputField.OnChangeEvent();
        input.onEndEdit = new TMP_InputField.SubmitEvent();
        input.onSubmit = new TMP_InputField.SubmitEvent();
        input.characterLimit = password ? 2048 : 1024;
        input.contentType = password ? TMP_InputField.ContentType.Password : TMP_InputField.ContentType.Standard;
        input.lineType = TMP_InputField.LineType.SingleLine;
        input.text = value;
        if (input.placeholder is TMP_Text label)
        {
            Stretch(label.rectTransform, 0, 0);
            FormatInputText(label, new Color(.65f, .69f, .74f, 1));
            label.text = placeholder;
        }
        clone.SetActive(true);
        return input;
    }

    private static void Stretch(RectTransform rect, float horizontalPadding, float verticalPadding)
    {
        rect.anchorMin = Vector2.zero;
        rect.anchorMax = Vector2.one;
        rect.pivot = new Vector2(.5f, .5f);
        rect.localScale = Vector3.one;
        rect.offsetMin = new Vector2(horizontalPadding, verticalPadding);
        rect.offsetMax = new Vector2(-horizontalPadding, -verticalPadding);
    }

    private static void FormatInputText(TMP_Text text, Color color)
    {
        text.enableAutoSizing = false;
        text.fontSize = 24;
        text.alignment = TextAlignmentOptions.MidlineLeft;
        text.margin = Vector4.zero;
        text.richText = false;
        text.color = color;
        text.raycastTarget = false;
    }

    internal static void DisableLayout(Transform transform)
    {
        foreach (Behaviour behaviour in transform.GetComponents<Behaviour>())
            if (IsLayout(behaviour)) behaviour.enabled = false;
    }

    internal static bool IsLayout(Behaviour behaviour) => behaviour is LayoutGroup || behaviour is ContentSizeFitter ||
        behaviour.GetType().Namespace == "TheraBytes.BetterUi" && behaviour is not Graphic && behaviour is not Selectable;

    internal static TMP_Text Label(TMP_Text template, Transform parent, string name, string value, float y)
    {
        TMP_Text label = UnityEngine.Object.Instantiate(template, parent, false);
        label.gameObject.name = name;
        Place(label.transform, parent, 0, y, 680, 34);
        label.fontSize = 22;
        label.enableAutoSizing = false;
        label.richText = false;
        label.alignment = TextAlignmentOptions.MidlineLeft;
        label.text = value;
        label.raycastTarget = false;
        label.gameObject.SetActive(true);
        return label;
    }
}
