using TextComponent = BD2.GameSdk.Component;
using System;
using System.Collections.Generic;
using System.Threading;
using Cysharp.Threading.Tasks;
using TMPro;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FChat;

internal sealed class TranslationSettingsPanel : MonoBehaviour, IDisposable
{
    private GreetingPopupUI popup;
    private TMP_InputField endpoint, model, key;
    private TMP_Text status, title;
    private string originalTitle;
    private GameObject originalInput, originalSave, originalCancel;
    private Transform frame;
    private Vector2 originalSize;
    private Vector2 titlePosition, titleSize, titleAnchorMin, titleAnchorMax, titlePivot;
    private readonly List<Behaviour> layouts = [];
    private readonly List<GameObject> hidden = [];
    private readonly List<GameObject> added = [];
    private readonly List<Button> actions = [];
    private CancellationTokenSource cancellation;
    private bool busy;

    internal void Initialize(GreetingPopupUI owner)
    {
        popup = owner;
        cancellation = new CancellationTokenSource();
        TMP_InputField input = TranslationSettingsUI.Field<TMP_InputField>(owner, "_inputField");
        originalSave = TranslationSettingsUI.Field<GameObject>(owner, "_objChangeButton");
        originalCancel = TranslationSettingsUI.Field<GameObject>(owner, "_objCancelButton");
        if (input == null || originalSave == null || originalCancel == null) throw new MissingMemberException("Native input popup controls unavailable.");
        originalInput = input.transform.parent.gameObject;
        frame = originalInput.transform.parent.parent;
        title = FindText(owner.transform, "Text - Title");
        if (title == null) throw new MissingMemberException("Native input popup title unavailable.");
        originalTitle = title.text;
        title.text = TextComponent.Translatable("fchat.panel.title").GetString();
        var frameRect = (RectTransform)frame;
        originalSize = frameRect.sizeDelta;
        foreach (Behaviour behaviour in frame.GetComponentsInChildren<Behaviour>(true))
            if (behaviour.enabled && NativeTranslationControls.IsLayout(behaviour)) { layouts.Add(behaviour); behaviour.enabled = false; }
        RectTransform titleRect = title.rectTransform;
        titlePosition = titleRect.anchoredPosition; titleSize = titleRect.sizeDelta; titleAnchorMin = titleRect.anchorMin; titleAnchorMax = titleRect.anchorMax; titlePivot = titleRect.pivot;
        NativeTranslationControls.Place(title.transform, frame, 0, 235, 680, 44);
        frameRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, 660);
        TMP_Text info = FindText(frame, "Text - Info"); if (info != null) { hidden.Add(info.gameObject); info.gameObject.SetActive(false); }
        TranslationConfig config = NativeTranslationSettings.Load();
        endpoint = MakeInput(input, "Endpoint", config.ApiUrl, TextComponent.Translatable("fchat.panel.endpoint_hint").GetString(), 125, false);
        model = MakeInput(input, "Model", config.Model, TextComponent.Translatable("fchat.panel.model_hint").GetString(), 15, false);
        key = MakeInput(input, "API Key", string.Empty, NativeTranslationSettings.HasSavedKey ? TextComponent.Translatable("fchat.panel.key_saved").GetString() : TextComponent.Translatable("fchat.panel.key_hint").GetString(), -95, true);
        MakeLabel("Endpoint Label", TextComponent.Translatable("fchat.panel.endpoint_label").GetString(), 176);
        MakeLabel("Model Label", TextComponent.Translatable("fchat.panel.model_label").GetString(), 66);
        MakeLabel("Key Label", TextComponent.Translatable("fchat.panel.key_label").GetString(), -44);
        status = MakeLabel("Status", TextComponent.Translatable("fchat.panel.info").GetString(), -180);
        status.rectTransform.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, 52);
        status.fontSize = 20;
        actions.Add(MakeButton("Cancel", TextComponent.Translatable("fchat.button.cancel").GetString(), -270, Close));
        actions.Add(MakeButton("Clear", TextComponent.Translatable("fchat.button.clear").GetString(), -90, Clear));
        actions.Add(MakeButton("Test", TextComponent.Translatable("fchat.button.test").GetString(), 90, Test));
        actions.Add(MakeButton("Save", TextComponent.Translatable("fchat.button.save").GetString(), 270, Save));
        originalInput.SetActive(false);
        originalSave.SetActive(false);
        originalCancel.SetActive(false);
    }

    internal void Click(GameObject button)
    {
        if (button == null) return;
        if (button == originalCancel || button.name == "Button - Back" || button.name == "Button - background") Close();
    }

    private TMP_InputField MakeInput(TMP_InputField template, string name, string value, string placeholder, float y, bool password)
    {
        Sprite background = originalSave.transform.Find("Image - Bt").GetComponent<Image>().sprite;
        TMP_InputField input = NativeTranslationControls.Input(template, frame, background, name, value, placeholder, y, password);
        added.Add(input.transform.parent.gameObject);
        return input;
    }

    private TMP_Text MakeLabel(string name, string value, float y)
    {
        TMP_Text label = NativeTranslationControls.Label(title, frame, name, value, y);
        added.Add(label.gameObject);
        return label;
    }

    private Button MakeButton(string name, string value, float x, Action click)
    {
        Button button = NativeTranslationControls.Button(originalSave, frame, name, value, x, click);
        added.Add(button.gameObject);
        return button;
    }

    private TranslationConfig Draft() => new() { Enabled = true, ApiUrl = endpoint.text.Trim(), Model = model.text.Trim(), ApiKey = key.text.Trim() };

    private void Save()
    {
        try { NativeTranslationSettings.Save(Draft()); key.text = string.Empty; UpdateKeyPlaceholder(); status.text = TextComponent.Translatable("fchat.panel.saved").GetString(); }
        catch (ArgumentException error) { status.text = error.Message; }
        catch (Exception) { status.text = TextComponent.Translatable("fchat.panel.save_failed").GetString(); }
    }

    private void Clear()
    {
        try { NativeTranslationSettings.Clear(); endpoint.text = model.text = key.text = string.Empty; UpdateKeyPlaceholder(); status.text = TextComponent.Translatable("fchat.panel.cleared").GetString(); }
        catch (Exception) { status.text = TextComponent.Translatable("fchat.panel.clear_failed").GetString(); }
    }

    private async void Test()
    {
        if (busy) return;
        busy = true;
        foreach (Button button in actions) if (button.gameObject.name != "Cancel") button.interactable = false;
        status.text = TextComponent.Translatable("fchat.panel.testing").GetString();
        try
        {
            TranslationTestResult result = await NativeChatTranslation.TestAsync(Draft(), cancellation.Token);
            await UniTask.SwitchToMainThread();
            if (this != null && isActiveAndEnabled) status.text = result.Message;
        }
        catch (OperationCanceledException) { }
        catch (ArgumentException error)
        {
            await UniTask.SwitchToMainThread();
            if (this != null && isActiveAndEnabled) status.text = error.Message;
        }
        catch (Exception)
        {
            await UniTask.SwitchToMainThread();
            if (this != null && isActiveAndEnabled) status.text = TextComponent.Translatable("fchat.panel.test_failed").GetString();
        }
        finally
        {
            await UniTask.SwitchToMainThread();
            if (this != null && isActiveAndEnabled) foreach (Button button in actions) if (button != null) button.interactable = true;
            busy = false;
        }
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
    private void Close() { cancellation?.Cancel(); if (popup != null) popup.CloseUI(); }

    private void UpdateKeyPlaceholder()
    {
        if (key.placeholder is TMP_Text label) label.text = NativeTranslationSettings.HasSavedKey ? TextComponent.Translatable("fchat.panel.key_saved").GetString() : TextComponent.Translatable("fchat.panel.key_hint").GetString();
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void OnDisable() => Dispose();

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
    public void Dispose()
    {
        cancellation?.Cancel();
        cancellation?.Dispose();
        cancellation = null;
        if (key != null) key.text = string.Empty;
        if (title != null) { title.text = originalTitle; RectTransform rect = title.rectTransform; rect.anchorMin = titleAnchorMin; rect.anchorMax = titleAnchorMax; rect.pivot = titlePivot; rect.sizeDelta = titleSize; rect.anchoredPosition = titlePosition; }
        if (frame != null) ((RectTransform)frame).sizeDelta = originalSize;
        if (originalInput != null) originalInput.SetActive(true);
        if (originalSave != null) originalSave.SetActive(true);
        if (originalCancel != null) originalCancel.SetActive(true);
        foreach (GameObject child in added) if (child != null) Destroy(child);
        foreach (GameObject child in hidden) if (child != null) child.SetActive(true);
        foreach (Behaviour behaviour in layouts) if (behaviour != null) behaviour.enabled = true;
        added.Clear(); hidden.Clear(); layouts.Clear();
        Destroy(this);
    }

    private static TMP_Text FindText(Transform root, string name)
    {
        foreach (TMP_Text text in root.GetComponentsInChildren<TMP_Text>(true)) if (text.gameObject.name == name) return text;
        return null;
    }
}
