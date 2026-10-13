using System;
using System.Reflection;
using BD2.GameNames;
using Cysharp.Threading.Tasks;
using gamfs;
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

    internal static Button Button(GameObject template, Transform parent, string name, string title, float x, float y, Action click)
    {
        GameObject clone = UnityEngine.Object.Instantiate(template, parent, false);
        clone.name = name;
        RemoveShortcuts(clone);
        Place(clone.transform, parent, x, y, 160, 56);
        SetText(clone, title);
        Button button = clone.GetComponent<Button>();
        button.onClick = new Button.ButtonClickedEvent();
        button.onClick.AddListener(() => click());
        button.interactable = true;
        clone.SetActive(true);
        return button;
    }

    internal static TMP_InputField Input(TMP_InputField template, Transform parent, Sprite background, string name,
        string value, string placeholder, float x, float y, float width, bool password)
    {
        GameObject clone = UnityEngine.Object.Instantiate(template.transform.parent.gameObject, parent, false);
        clone.name = name;
        RemoveShortcuts(clone);
        foreach (Transform child in clone.GetComponentsInChildren<Transform>(true)) DisableLayout(child);
        Place(clone.transform, parent, x, y, width, 64);
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

    internal static void LoadPrefab(string name, Action<AddressableAsset<GameObject>> ready, Action<Exception> failed)
    {
        MethodInfo method = typeof(AssetManager).GetGameMethod("GetAssetWithFailure", BindingFlags.Public | BindingFlags.Static) ??
            throw new MissingMethodException(nameof(AssetManager), "GetAssetWithFailure");
        Action<AddressableAsset<GameObject>> complete = async asset =>
        {
            await UniTask.SwitchToMainThread();
            ready(asset);
        };
        Action<Exception> failure = async error =>
        {
            await UniTask.SwitchToMainThread();
            failed(error);
        };
        method.MakeGenericMethod(typeof(GameObject)).Invoke(null, ["UIPrefabs/" + name + ".prefab", complete, failure]);
    }

    internal static EquipmentFilterPopupUI.ToggleFilterOption Checkbox(GameObject template, Transform parent, string name, bool value, float x, float y)
    {
        GameObject clone = UnityEngine.Object.Instantiate(template, parent, false);
        clone.name = name;
        RemoveShortcuts(clone);
        foreach (Transform child in clone.GetComponentsInChildren<Transform>(true)) DisableLayout(child);
        foreach (TMP_Text label in clone.GetComponentsInChildren<TMP_Text>(true)) label.gameObject.SetActive(false);
        Place(clone.transform, parent, x, y, 48, 48);
        Transform indicator = clone.transform.Find("Button - Active");
        if (indicator == null) { UnityEngine.Object.Destroy(clone); throw new MissingMemberException("Native checkbox indicator unavailable."); }
        var checkbox = new EquipmentFilterPopupUI.ToggleFilterOption { root = clone, objActive = indicator.gameObject };
        foreach (Button button in clone.GetComponentsInChildren<Button>(true))
        {
            button.onClick = new Button.ButtonClickedEvent();
            button.onClick.AddListener(() => checkbox.OnClick(clone));
            button.interactable = true;
        }
        checkbox.SetActive(value);
        clone.SetActive(true);
        return checkbox;
    }

    internal static Button Question(Button template, Transform parent, string name, float x, float y, Action click)
    {
        Button button = UnityEngine.Object.Instantiate(template, parent, false);
        button.gameObject.name = name;
        RemoveShortcuts(button.gameObject);
        foreach (Transform child in button.GetComponentsInChildren<Transform>(true)) DisableLayout(child);
        Place(button.transform, parent, x, y, 44, 44);
        Transform icon = button.transform.Find("Image - Info");
        if (icon != null) Place(icon, button.transform, 0, 0, 32, 32);
        button.onClick = new Button.ButtonClickedEvent();
        button.onClick.AddListener(() => click());
        button.interactable = true;
        button.gameObject.SetActive(true);
        return button;
    }

    private static void RemoveShortcuts(GameObject control)
    {
        foreach (MultiPlatformInputBinder shortcut in control.GetComponentsInChildren<MultiPlatformInputBinder>(true))
        {
            shortcut.enabled = false;
            UnityEngine.Object.Destroy(shortcut);
        }
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

    internal static TMP_Text Label(GameUtilObjectPool<TMP_Text> pool, Transform parent, string name, string value, float x, float y, float width)
    {
        TMP_Text label = pool.GetObject();
        label.gameObject.name = name;
        Place(label.transform, parent, x, y, width, 34);
        label.fontSize = 22;
        label.enableAutoSizing = false;
        label.richText = false;
        label.alignment = TextAlignmentOptions.MidlineLeft;
        label.text = value;
        label.raycastTarget = false;
        return label;
    }
}
