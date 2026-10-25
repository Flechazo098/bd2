using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using Helper;
using Proto.Net;
using TMPro;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FBattle;

// The action editor, hosted in the game's greeting popup.  Each preset member
// gets a row: the character list card (portrait, name, element), then the
// battle action cards it can pick from, laid out in up to two lines.
[System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
[System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes these lifecycle callbacks.")]
internal sealed class PresetActionEditor : MonoBehaviour, IDisposable
{
    private const float PanelWidth = 1560, PanelHeight = 1000, ListWidth = 1500, ListHeight = 740, ListY = -4;
    private const float Gap = 10, RowPadding = 12;
    private GreetingPopupUI popup;
    private CharManageUI owner;
    private NativeActionControls native;
    private PresetDBInfo preset;
    private int slot;
    private Dictionary<long, ActionChoice> original, draft;
    private Transform frame;
    private RectTransform root;
    private TMP_Text textTemplate, status;
    private GameObject originalSave, originalCancel;
    private Vector2 frameSize;
    private Vector3 frameScale;
    private readonly List<(GameObject Object, bool Active)> hidden = [];
    private readonly List<Behaviour> layouts = [];
    private readonly List<Button> buttons = [];
    private readonly List<Row> rows = [];
    private bool busy, disposed, confirming;
    internal bool AllowClose { get; private set; }

    internal void Initialize(GreetingPopupUI host, CharManageUI editor, NativeActionControls controls)
    {
        popup = host; owner = editor; native = controls;
        preset = NativeActionControls.Field<PresetDBInfo>(editor, "_presetInfo")?.Clone() ?? throw new MissingMemberException("Preset draft unavailable.");
        slot = NativeActionControls.Integer(editor, "_presetIndex");
        var members = new HashSet<long>(editor.DeckList.Select(item => item.CharInvenIndex));
        draft = PresetPlans.Get(slot).Where(item => members.Contains(item.Key)).ToDictionary(item => item.Key, item => item.Value.Clone());
        original = draft.ToDictionary(item => item.Key, item => item.Value.Clone());
        TMP_InputField input = NativeActionControls.Field<TMP_InputField>(host, "_inputField");
        originalSave = NativeActionControls.Field<GameObject>(host, "_objChangeButton");
        originalCancel = NativeActionControls.Field<GameObject>(host, "_objCancelButton");
        if (input == null || originalSave == null || originalCancel == null) throw new MissingMemberException("Native popup controls unavailable.");
        Transform nameBox = input.transform.parent;
        frame = nameBox.parent.parent;
        textTemplate = frame.GetComponentsInChildren<TMP_Text>(true).FirstOrDefault(text => text.gameObject.name == "Text - Title") ??
            throw new MissingMemberException("Native popup title unavailable.");
        BuildFrame(nameBox.parent.gameObject);
        Label(root, "Title", ActionCatalog.Text("fbattle.title") + " · " + preset.PresetName, 0, 440, 1300, 50, 34, TextAlignmentOptions.Midline);
        Label(root, "Explanation", ActionCatalog.Text("fbattle.info"), 0, 398, 1420, 36, 20, TextAlignmentOptions.Midline);
        ScrollRect list = NativeActionControls.Scroll<ScrollRect>(root, "Characters", 0, ListY, ListWidth, ListHeight, false);
        List<DeckDBInfo> ordered = [.. editor.DeckList.Where(item => item.CharInvenIndex > 0).OrderBy(item => item.Sequence)];
        float y = 0;
        for (int index = 0; index < ordered.Count; index++) y += BuildRow(list, ordered[index], index, y) + Gap;
        list.content.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, Math.Max(ListHeight, y - Gap));
        list.verticalNormalizedPosition = 1;
        status = Label(root, "Status", string.Empty, 0, -410, 1400, 34, 22, TextAlignmentOptions.Midline);
        var size = new Vector2(220, 64);
        Button cancel = NativeActionControls.Button(originalCancel, root, "Cancel", ActionCatalog.Text("fbattle.cancel"), size, RequestClose);
        Button save = NativeActionControls.Button(originalSave, root, "Save", ActionCatalog.Text("fbattle.save"), size, Save);
        ((RectTransform)cancel.transform).anchoredPosition = new Vector2(-150, -460);
        ((RectTransform)save.transform).anchoredPosition = new Vector2(150, -460);
        buttons.Add(cancel); buttons.Add(save);
        foreach (Row row in rows) RefreshRow(row);
        UpdateStatus();
    }

    // Turns the popup's frame into the editor's panel; Dispose puts it back.
    private void BuildFrame(GameObject renameBox)
    {
        var frameRect = (RectTransform)frame;
        frameSize = frameRect.sizeDelta; frameScale = frameRect.localScale;
        foreach (Behaviour layout in frame.GetComponentsInChildren<Behaviour>(true))
            if (layout.enabled && NativeActionControls.IsLayout(layout)) { layouts.Add(layout); layout.enabled = false; }
        Hide(renameBox); Hide(originalSave.transform.parent.gameObject);
        foreach (TMP_Text text in frame.GetComponentsInChildren<TMP_Text>(true))
            if (text.gameObject.name is "Text - Title" or "Text - Info") Hide(text.gameObject);
        frameRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, PanelWidth);
        frameRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, PanelHeight);
        root = NativeActionControls.Rect("FBattle Action Editor", frame, 0, 0, PanelWidth, PanelHeight);
        Rescale();
    }

    private void Hide(GameObject child) { if (child == null) return; hidden.Add((child, child.activeSelf)); child.SetActive(false); }

    private void Rescale()
    {
        if (frame == null || popup == null) return;
        Canvas canvas = popup.GetComponentInParent<Canvas>();
        if (canvas == null || canvas.rootCanvas.transform is not RectTransform canvasRect) return;
        float scale = Mathf.Min(1, (canvasRect.rect.width - 40) / PanelWidth, (canvasRect.rect.height - 40) / PanelHeight);
        frame.localScale = frameScale * Mathf.Max(.1f, scale);
    }

    private float BuildRow(ScrollRect list, DeckDBInfo deck, int order, float top)
    {
        var catalog = new ActionCatalog(deck, preset);
        Vector2 card = NativeActionControls.CardSize;
        Vector2 slotSize = native.SlotSize;
        int lines = 2 * card.y + Gap <= slotSize.y + 1 ? 2 : 1;
        float height = Math.Max(slotSize.y, lines * card.y + (lines - 1) * Gap) + RowPadding * 2;
        RectTransform rowRoot = NativeActionControls.Rect("Character " + deck.CharInvenIndex, list.content, 0, 0, ListWidth, height);
        rowRoot.anchorMin = rowRoot.anchorMax = rowRoot.pivot = new Vector2(0, 1);
        rowRoot.anchoredPosition = new Vector2(0, -top);
        NativeActionControls.Fill(rowRoot, "Row Background", new Color(0, 0, 0, .28f), 0, 0, ListWidth, height);
        var row = new Row(catalog);
        rows.Add(row);
        BuildIdentity(rowRoot, catalog, order, -ListWidth / 2 + RowPadding + slotSize.x / 2);

        float cardsLeft = RowPadding * 2 + slotSize.x;
        float cardsWidth = ListWidth - cardsLeft - RowPadding;
        float cardsHeight = lines * card.y + (lines - 1) * Gap;
        RowScroll actions = NativeActionControls.Scroll<RowScroll>(rowRoot, "Action Cards", -ListWidth / 2 + cardsLeft + cardsWidth / 2, 0, cardsWidth, cardsHeight, true);
        actions.Outer = list;
        int columns = (catalog.Options.Count + lines - 1) / lines;
        actions.content.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, Math.Max(cardsWidth, columns * (card.x + Gap) - Gap));
        draft.TryGetValue(catalog.Character.InvenIndex, out ActionChoice chosen);
        for (int index = 0; index < catalog.Options.Count; index++)
        {
            ActionOption option = catalog.Options[index];
            var view = new ActionCard(native, actions.content, catalog, option);
            view.Root.anchoredPosition = new Vector2(index % columns * (card.x + Gap), -(index / columns) * (card.y + Gap));
            if (option.IsSkill && chosen != null && chosen.CostumeIndex == option.Index) view.Burst = Math.Min(option.MaxBurst, Math.Max(0, chosen.BurstLevel));
            view.Choose.onClick.AddListener(() => Select(row, view));
            view.BurstDown?.onClick.AddListener(() => ChangeBurst(row, view, -1));
            view.BurstUp?.onClick.AddListener(() => ChangeBurst(row, view, 1));
            row.Cards.Add(view);
        }
        actions.horizontalNormalizedPosition = 0;
        LoadEffect(row, $"UIPrefabsParts/Eff_SkillCard_{catalog.Element}_NEW.prefab", effect => row.SelectEffect = effect);
        LoadEffect(row, "UIPrefabsParts/Eff_SkillCard_Burst.prefab", effect => row.BurstEffect = effect);
        return height;
    }

    // The character list card, so the row reads like the preset screen itself.
    private void BuildIdentity(RectTransform rowRoot, ActionCatalog catalog, int order, float x)
    {
        GameObject copy = NativeActionControls.Dormant(native.SlotTemplate, "Character Card");
        NativeActionControls.Strip(copy, script => script is Selectable);
        CanvasGroup group = copy.GetComponent<CanvasGroup>() ?? copy.AddComponent<CanvasGroup>();
        RectTransform rect = NativeActionControls.Place(NativeActionControls.Wake(copy, rowRoot), rowRoot, x, 0, native.SlotSize.x, native.SlotSize.y);
        group.interactable = false;
        group.blocksRaycasts = false;
        CharacterSlotSetting card = copy.GetComponentInChildren<CharacterSlotSetting>(true) ?? throw new MissingMemberException("Native character card unavailable.");
        CharDBInfo character = catalog.Character;
        card.SetCharDbInfo(character);
        card.SetCharacter(catalog.Appearance.Id, catalog.Appearance.CostumeDesignId, character.Level, catalog.Appearance.Level, catalog.Name,
            character.Hp, catalog.Definition.Type, ECharacterSlotMotionType.Idle, null);
        card.SetBlockState(true);
        card.SetSwap(false);
        card.SetSwap2(false);
        card.SetIncludeBattleDeckMark(false);
        card.SetActiveExhaustion(character.Hp <= 0);
        card.SetSequence(order);
        card.SetSequenceChange(false);
        rect.gameObject.SetActive(true);
    }

    private void LoadEffect(Row row, string address, Action<GameObject> loaded)
    {
        AssetManager.GetPrefabAsset(address, effect =>
        {
            if (effect == null) return;
            if (disposed || this == null) { Destroy(effect); return; }
            effect.SetActive(false);
            effect.transform.SetParent(root, false);
            loaded(effect);
            RefreshRow(row);
        });
    }

    // First-turn SP: the battle starts the player's team with this many
    // bubbles and adds the per-turn SP only after the first turn.
    private static int Budget => Math.Min(BattleDefaultInfo.SpStartPvECount, BattleDefaultInfo.SkillBubbleMaxPoint);

    private int Cost(Row row) => draft.TryGetValue(row.Catalog.Character.InvenIndex, out ActionChoice choice) ?
        row.Catalog.Options.FirstOrDefault(item => item.Index == choice.CostumeIndex)?.Sp(choice.BurstLevel) ?? 0 : 0;

    // SP left for this row once every other chosen action is paid for.
    private int Available(Row row) => Budget - rows.Where(other => other != row).Sum(Cost);

    private void Select(Row row, ActionCard card)
    {
        if (busy) return;
        if (card.Option.IsSkill && card.Option.Sp(card.Burst) > Available(row))
        {
            // As in battle: drop the burst until the skill fits, else refuse.
            int burst = card.Burst;
            while (burst > 0 && card.Option.Sp(burst) > Available(row)) burst--;
            if (card.Option.Sp(burst) > Available(row)) { UIManager.OpenBarNotice(LocalTextInfo.GetText(684), false, null); return; }
            card.Burst = burst;
        }
        long character = row.Catalog.Character.InvenIndex;
        if (card.Option.IsDefault) draft.Remove(character);
        else draft[character] = new ActionChoice(character, card.Option.Index, card.Option.IsSkill ? card.Burst : 0);
        RefreshRow(row);
        foreach (Row other in rows) if (other != row) RefreshRow(other);
        UpdateStatus();
    }

    private void ChangeBurst(Row row, ActionCard card, int delta)
    {
        if (busy) return;
        int burst = Math.Max(0, Math.Min(card.Option.MaxBurst, card.Burst + delta));
        if (burst == card.Burst) return;
        if (delta > 0 && card.Option.Sp(burst) > Available(row)) { UIManager.OpenBarNotice(LocalTextInfo.GetText(3100004), false, null); return; }
        card.Burst = burst;
        Select(row, card);
    }

    private void RefreshRow(Row row)
    {
        if (disposed) return;
        draft.TryGetValue(row.Catalog.Character.InvenIndex, out ActionChoice selected);
        int available = Available(row);
        ActionCard chosen = null;
        foreach (ActionCard card in row.Cards)
        {
            bool active = selected == null ? card.Option.IsDefault : selected.CostumeIndex == card.Option.Index;
            if (active) chosen = card;
            card.Refresh(active, busy, row.SelectEffect != null && card.SelectEffectParent != null, available);
        }
        Attach(row.SelectEffect, chosen?.SelectEffectParent, ref row.SelectHost, true);
        bool bursting = chosen != null && chosen.Option.IsSkill && chosen.Burst > 0;
        Attach(row.BurstEffect, bursting ? chosen.BurstEffectParent : null, ref row.BurstHost, false);
    }

    // Moves an effect onto the selected card the way the battle list does,
    // replaying it only when the selection actually moves.
    private static void Attach(GameObject effect, Transform host, ref Transform current, bool replay)
    {
        if (effect == null) return;
        if (current == host && effect.activeSelf == (host != null)) return;
        if (current != null && current != host) current.gameObject.SetActive(false);
        current = host;
        if (host == null) { effect.SetActive(false); return; }
        host.gameObject.SetActive(true);
        var rect = (RectTransform)effect.transform;
        rect.SetParent(host, false);
        rect.localScale = Vector3.one;
        rect.anchoredPosition = Vector2.zero;
        if (rect.anchorMin != rect.anchorMax) rect.offsetMin = rect.offsetMax = Vector2.zero;
        if (replay || !effect.activeSelf) { effect.SetActive(false); effect.SetActive(true); }
    }

    private bool Dirty => original.Count != draft.Count || draft.Any(pair => !original.TryGetValue(pair.Key, out ActionChoice before) ||
        before.CostumeIndex != pair.Value.CostumeIndex || before.BurstLevel != pair.Value.BurstLevel);

    private void UpdateStatus()
    {
        bool automatic = rows.Any(row => !draft.ContainsKey(row.Catalog.Character.InvenIndex));
        status.text = string.Format(CultureInfo.CurrentCulture, ActionCatalog.Text(automatic ? "fbattle.sp_partial" : "fbattle.sp_total"),
            rows.Sum(Cost), Budget) + "   ·   " + ActionCatalog.Text(Dirty ? "fbattle.unsaved" : "fbattle.saved");
    }

    private void SetBusy(bool value)
    {
        busy = value;
        foreach (Button button in buttons) if (button != null) button.interactable = !value;
        foreach (Row row in rows) RefreshRow(row);
    }

    private void Save()
    {
        if (busy || owner == null) return;
        SetBusy(true);
        status.text = ActionCatalog.Text("fbattle.saving");
        try
        {
            Dictionary<long, List<EquipDBInfo>> equipment = NativeActionControls.Field<Dictionary<long, List<EquipDBInfo>>>(owner, "_presetManagedEquipDataByChar") ??
                throw new MissingMemberException("Preset equipment draft unavailable.");
            PresetDBInfo saved = preset.Clone();
            Prset.RefreshPresetDBInfo(ref saved, owner.DeckList, equipment);
            PresetDBInfo complete = saved;
            PresetPlans.Save(saved, slot, draft.Values, () =>
            {
                if (disposed) return;
                draft = PresetPlans.Get(slot);
                original = draft.ToDictionary(item => item.Key, item => item.Value.Clone());
                preset = complete.Clone();
                if (owner != null)
                {
                    owner.SetConfigPresetData(complete.Clone(), slot);
                    owner.SetTabCharManageUI(owner.CurrentTab, true);
                }
                busy = false;
                Close();
                UIManager.OpenBarNotice(ActionCatalog.Text("fbattle.saved"), false, null);
            }, SaveFailed);
        }
        catch (Exception error) { Plugin.Log?.LogWarning("FBattle save: " + error); SaveFailed(error.Message); }
    }

    private void SaveFailed(string error)
    {
        if (disposed) return;
        SetBusy(false);
        status.text = ActionCatalog.Text("fbattle.save_failed");
        Plugin.Log?.LogWarning("FBattle preset save failed: " + error);
    }

    internal void Click(GameObject clicked)
    {
        if (clicked == null) return;
        if (clicked == originalCancel || clicked.name == "Button - Back" || clicked.name == "Button - background" || popup.IsClickedBackButton(clicked)) RequestClose();
    }

    // Esc, the back button and Cancel all land here.  Unsaved changes get the
    // deck editor's own save / discard prompt.
    internal void RequestClose()
    {
        if (busy || confirming || disposed) return;
        if (!Dirty) { Close(); return; }
        confirming = true;
        UIManager.AddUI<DeckSavePopupUI>(confirm =>
        {
            if (disposed) { confirm.CloseForceUI(true); return; }
            confirm.SetSortingOrder(Math.Max(confirm.SortingOrder, popup.SortingOrder + 1));
            confirm.AddCloseCallback(() => { confirming = false; });
            confirm.OpenPopup(ActionCatalog.Text("fbattle.confirm.message"), ActionCatalog.Text("fbattle.confirm.title"),
                ActionCatalog.Text("fbattle.save"), ActionCatalog.Text("fbattle.discard"), MessagePopupUI.EMessagePopupButtonStyle.OK_CANCEL,
                (ok, dialog) => { if (ok) { confirming = false; Save(); } }, true,
                () => { confirm.CloseUI(); confirming = false; Close(); });
        }, "", false, "");
    }

    private void Close() { AllowClose = true; if (popup != null) popup.CloseUI(); }

    private TMP_Text Label(Transform parent, string name, string value, float x, float y, float width, float height, float size, TextAlignmentOptions alignment) =>
        NativeActionControls.Label(textTemplate, parent, name, value, x, y, width, height, size, alignment);

    private void LateUpdate() => Rescale();
    private void OnDisable() => Dispose();

    public void Dispose()
    {
        if (disposed) return;
        disposed = true;
        foreach (Row row in rows) { if (row.SelectEffect != null) Destroy(row.SelectEffect); if (row.BurstEffect != null) Destroy(row.BurstEffect); }
        if (root != null) Destroy(root.gameObject);
        if (frame != null) { ((RectTransform)frame).sizeDelta = frameSize; frame.localScale = frameScale; }
        foreach ((GameObject child, bool active) in hidden) if (child != null) child.SetActive(active);
        foreach (Behaviour layout in layouts) if (layout != null) layout.enabled = true;
        rows.Clear(); buttons.Clear(); hidden.Clear(); layouts.Clear();
        Destroy(this);
    }

    private sealed class Row(ActionCatalog catalog)
    {
        internal ActionCatalog Catalog { get; } = catalog;
        internal List<ActionCard> Cards { get; } = [];
        internal GameObject SelectEffect, BurstEffect;
        internal Transform SelectHost, BurstHost;
    }
}
