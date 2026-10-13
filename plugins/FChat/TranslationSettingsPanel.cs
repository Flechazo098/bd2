using TextComponent = BD2.GameSdk.Component;
using System;
using System.Collections.Generic;
using System.Threading;
using Cysharp.Threading.Tasks;
using gamfs;
using TMPro;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FChat;

internal sealed class TranslationSettingsPanel : MonoBehaviour, IDisposable
{
    private GreetingPopupUI popup;
    private TMP_InputField endpoint, model, key, targetLanguage, languageBlacklist;
    private EquipmentFilterPopupUI.ToggleFilterOption translateOwnMessages;
    private TMP_Text status, title;
    private GameUtilObjectPool<TMP_Text> labelPool;
    private string originalTitle;
    private GameObject originalInput, originalSave, originalCancel;
    private Transform frame;
    private Vector2 originalSize;
    private Vector3 originalScale;
    private Vector2 titlePosition, titleSize, titleAnchorMin, titleAnchorMax, titlePivot;
    private readonly List<Behaviour> layouts = [];
    private readonly List<GameObject> hidden = [];
    private readonly List<GameObject> added = [];
    private readonly List<Button> actions = [];
    private CancellationTokenSource cancellation;
    private bool busy, disposed;

    internal void Initialize(GreetingPopupUI owner, GameObject nativeControls)
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
        originalScale = frameRect.localScale;
        foreach (Behaviour behaviour in frame.GetComponentsInChildren<Behaviour>(true))
            if (behaviour.enabled && NativeTranslationControls.IsLayout(behaviour)) { layouts.Add(behaviour); behaviour.enabled = false; }
        RectTransform titleRect = title.rectTransform;
        titlePosition = titleRect.anchoredPosition; titleSize = titleRect.sizeDelta; titleAnchorMin = titleRect.anchorMin; titleAnchorMax = titleRect.anchorMax; titlePivot = titleRect.pivot;
        NativeTranslationControls.Place(title.transform, frame, 0, 340, 1000, 44);
        frameRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, 1100);
        frameRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, 850);
        Canvas canvas = owner.GetComponentInParent<Canvas>();
        if (canvas != null && canvas.rootCanvas.transform is RectTransform canvasRect)
        {
            float scale = Mathf.Min(1, (canvasRect.rect.width - 48) / 1100, (canvasRect.rect.height - 48) / 850);
            frameRect.localScale = originalScale * Mathf.Max(.1f, scale);
        }
        TMP_Text info = FindText(frame, "Text - Info"); if (info != null) { hidden.Add(info.gameObject); info.gameObject.SetActive(false); }
        TranslationConfig config = NativeTranslationSettings.Load();
        endpoint = MakeInput(input, "Endpoint", config.ApiUrl, TextComponent.Translatable("fchat.panel.endpoint_hint").GetString(), 140, 225, false);
        model = MakeInput(input, "Model", config.Model, TextComponent.Translatable("fchat.panel.model_hint").GetString(), 140, 130, false);
        key = MakeInput(input, "API Key", string.Empty, NativeTranslationSettings.HasSavedKey ? TextComponent.Translatable("fchat.panel.key_saved").GetString() : TextComponent.Translatable("fchat.panel.key_hint").GetString(), 140, 35, true);
        targetLanguage = MakeInput(input, "Target Language", config.TargetLanguage, TextComponent.Translatable("fchat.panel.target_hint").GetString(), 140, -60, false);
        targetLanguage.characterLimit = 200;
        languageBlacklist = MakeInput(input, "Language Blacklist", config.LanguageBlacklist, TextComponent.Translatable("fchat.panel.blacklist_hint").GetString(), 140, -155, false);
        languageBlacklist.characterLimit = 2000;
        actions.Add(MakeButton("Cancel", TextComponent.Translatable("fchat.button.cancel").GetString(), -270, Close));
        actions.Add(MakeButton("Clear", TextComponent.Translatable("fchat.button.clear").GetString(), -90, Clear));
        actions.Add(MakeButton("Test", TextComponent.Translatable("fchat.button.test").GetString(), 90, Test));
        actions.Add(MakeButton("Save", TextComponent.Translatable("fchat.button.save").GetString(), 270, Save));
        originalInput.SetActive(false);
        originalSave.SetActive(false);
        originalCancel.SetActive(false);
        foreach (Button button in actions) if (button.gameObject.name != "Cancel") button.interactable = false;
        LoadNativeOptions(nativeControls, config.TranslateOwnMessages);
    }

    private void LoadNativeOptions(GameObject nativeControls, bool ownMessages)
    {
        string stage = "label-template";
        try
        {
            TMP_Text templateLabel = nativeControls.transform.Find("Button - background/Parent/Image - Background/OptionParent1/Option - Part/Text - OptionTitle")?.GetComponent<TMP_Text>() ??
                throw new MissingMemberException("Native settings label unavailable.");
            TMP_Text labelOrigin = UnityEngine.Object.Instantiate(templateLabel, frame, false);
            labelOrigin.gameObject.SetActive(false);
            added.Add(labelOrigin.gameObject);
            labelPool = new GameUtilObjectPool<TMP_Text>(labelOrigin, 1, false, "Translation Label", frame);
            CreateLabels();
            stage = "checkbox-template";
            GameObject checkboxTemplate = null;
            foreach (Transform child in nativeControls.GetComponentsInChildren<Transform>(true))
                if (child.gameObject.name == "SetupCheckBox") { checkboxTemplate = child.gameObject; break; }
            if (checkboxTemplate == null) throw new MissingMemberException("Native checkbox unavailable.");
            stage = "checkbox-initialization";
            translateOwnMessages = NativeTranslationControls.Checkbox(checkboxTemplate, frame, "Translate Own Messages", ownMessages, -486, -245);
            added.Add(translateOwnMessages.Root);
            stage = "question-template";
            Button template = null;
            foreach (Button button in nativeControls.GetComponentsInChildren<Button>(true))
                if (button.gameObject.name == "Button - Info") { template = button; break; }
            if (template == null) throw new MissingMemberException("Native question button unavailable.");
            stage = "target-help-initialization";
            MakeHelp(template, "Target Language Help", "fchat.panel.target_label", "fchat.help.target", -60);
            stage = "blacklist-help-initialization";
            MakeHelp(template, "Language Blacklist Help", "fchat.panel.blacklist_label", "fchat.help.blacklist", -155);
            ControlsLoaded();
        }
        catch (Exception error) { ControlsFailed(error, stage); throw; }
    }

    private void CreateLabels()
    {
        MakeFieldLabel("Endpoint Label", "fchat.panel.endpoint_label", 225);
        MakeFieldLabel("Model Label", "fchat.panel.model_label", 130);
        MakeFieldLabel("Key Label", "fchat.panel.key_label", 35);
        MakeFieldLabel("Target Language Label", "fchat.panel.target_label", -60);
        MakeFieldLabel("Language Blacklist Label", "fchat.panel.blacklist_label", -155);
        MakeLabel("Own Messages Label", TextComponent.Translatable("fchat.panel.own_messages").GetString(), 20, -245, 940);
        status = MakeLabel("Status", TextComponent.Translatable("fchat.panel.loading").GetString(), 0, -305, 1000);
        status.rectTransform.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, 48);
        status.fontSize = 20;
    }

    private void ControlsLoaded()
    {
        status.text = TextComponent.Translatable("fchat.panel.info").GetString();
        foreach (Button button in actions) button.interactable = true;
    }

    private void ControlsFailed(Exception error, string stage)
    {
        if (disposed) return;
        string message = TextComponent.Translatable("fchat.panel.controls_failed").GetString();
        if (status != null) status.text = message;
        else UIManager.OpenBarNotice(message, false, null);
        Plugin.Log?.LogWarning("Native translation options: stage=" + stage + " exception=" + error.GetType().Name + " stack=" + error.StackTrace);
    }

    private void MakeHelp(Button template, string name, string headingKey, string helpKey, float y)
    {
        Button help = NativeTranslationControls.Question(template, frame, name, -265, y, () =>
            UIManager.AddUI<SimpleGuideBookPopupUI>(guide =>
            {
                if (disposed) { guide.CloseUI(); return; }
                guide.SetTitle(TextComponent.Translatable("fchat.panel.title").GetString())
                    .SetPopup([TextComponent.Translatable(headingKey).GetString()], [TextComponent.Translatable(helpKey).GetString()])
                    .SetSimpleGuideItems();
                guide.SetSortingOrder(Math.Max(guide.SortingOrder, popup.SortingOrder + 1));
                TranslationGuideLayer layer = guide.GetComponent<TranslationGuideLayer>() ?? guide.gameObject.AddComponent<TranslationGuideLayer>();
                layer.Attach(guide, popup);
            }, "", false, ""));
        added.Add(help.gameObject);
    }

    internal void Click(GameObject button)
    {
        if (button == null) return;
        if (button == originalCancel || button.name == "Button - Back" || button.name == "Button - background") Close();
    }

    private TMP_InputField MakeInput(TMP_InputField template, string name, string value, string placeholder, float x, float y, bool password)
    {
        Sprite background = originalSave.transform.Find("Image - Bt").GetComponent<Image>().sprite;
        TMP_InputField input = NativeTranslationControls.Input(template, frame, background, name, value, placeholder, x, y, 740, password);
        added.Add(input.transform.parent.gameObject);
        return input;
    }

    private TMP_Text MakeLabel(string name, string value, float x, float y, float width = 480)
    {
        TMP_Text label = NativeTranslationControls.Label(labelPool, frame, name, value, x, y, width);
        added.Add(label.gameObject);
        return label;
    }

    private void MakeFieldLabel(string name, string key, float y)
    {
        TMP_Text label = MakeLabel(name, TextComponent.Translatable(key).GetString(), -405, y, 210);
        label.rectTransform.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, 64);
        label.enableAutoSizing = true;
        label.fontSizeMin = 18;
        label.fontSizeMax = 22;
        label.enableWordWrapping = true;
    }

    private Button MakeButton(string name, string value, float x, Action click)
    {
        Button button = NativeTranslationControls.Button(originalSave, frame, name, value, x, -365, click);
        added.Add(button.gameObject);
        return button;
    }

    private TranslationConfig Draft() => new()
    {
        Enabled = true,
        ApiUrl = endpoint.text.Trim(),
        Model = model.text.Trim(),
        ApiKey = key.text.Trim(),
        TranslateOwnMessages = translateOwnMessages != null && translateOwnMessages.IsActive(),
        TargetLanguage = targetLanguage.text.Trim(),
        LanguageBlacklist = languageBlacklist.text.Trim()
    };

    private void Save()
    {
        try { NativeTranslationSettings.Save(Draft()); key.text = string.Empty; UpdateKeyPlaceholder(); status.text = TextComponent.Translatable("fchat.panel.saved").GetString(); }
        catch (ArgumentException error) { status.text = error.Message; }
        catch (Exception) { status.text = TextComponent.Translatable("fchat.panel.save_failed").GetString(); }
    }

    private void Clear()
    {
        try { NativeTranslationSettings.Clear(); endpoint.text = model.text = key.text = targetLanguage.text = languageBlacklist.text = string.Empty; translateOwnMessages.SetActive(false); UpdateKeyPlaceholder(); status.text = TextComponent.Translatable("fchat.panel.cleared").GetString(); }
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
        if (disposed) return;
        disposed = true;
        cancellation?.Cancel();
        cancellation?.Dispose();
        cancellation = null;
        if (key != null) key.text = string.Empty;
        if (title != null) { title.text = originalTitle; RectTransform rect = title.rectTransform; rect.anchorMin = titleAnchorMin; rect.anchorMax = titleAnchorMax; rect.pivot = titlePivot; rect.sizeDelta = titleSize; rect.anchoredPosition = titlePosition; }
        if (frame != null) { ((RectTransform)frame).sizeDelta = originalSize; frame.localScale = originalScale; }
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

internal sealed class TranslationGuideLayer : MonoBehaviour
{
    private SimpleGuideBookPopupUI guide;
    private GreetingPopupUI parent;

    internal void Attach(SimpleGuideBookPopupUI owner, GreetingPopupUI settings) { guide = owner; parent = settings; }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void OnDisable()
    {
        if (guide == null) return;
        guide.ResetSortingOrder();
        if (parent != null && parent.isActiveAndEnabled) UIManager.SetUIBlur(parent, false);
        guide = null;
        parent = null;
    }
}
