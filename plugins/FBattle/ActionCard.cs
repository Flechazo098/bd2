using System;
using System.Collections.Generic;
using TMPro;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2FBattle;

// One copy of the battle's action card, drawn from preset data instead of a
// live BattleCharData: the same artwork, range grid, SP and burst bubbles,
// burst arrows and element selection effect the player sees in battle.
[System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
internal sealed class ActionCard
{
    private static readonly Color32[] DefaultElementColors =
        [new(56, 104, 180, 255), new(156, 56, 56, 255), new(45, 96, 57, 255), new(237, 173, 103, 255), new(187, 29, 226, 255), new(191, 191, 191, 255)];

    private readonly ActionCatalog catalog;
    private readonly Color32[] elementColors;
    private readonly GameObject normal, skill, focus, burstArrows;
    private readonly GameObject statsObject, bubbleTemplate;
    private readonly UISprite stats, attackRange, skillRange, skillIcon, skillLevel;
    private readonly GameObject attackArrow, targetParent;
    private readonly Transform knockBackIcon, bubbles;
    private readonly TMP_Text damage, attackTarget, skillTarget;
    private readonly RawImage artwork;
    private readonly TextureLease art;
    private readonly (GameObject On, GameObject Off) left, right;
    private readonly List<GameObject> bubblePool = [];
    private bool? skillArt;

    internal ActionOption Option { get; }
    internal RectTransform Root { get; }
    internal Transform SelectEffectParent { get; }
    internal Transform BurstEffectParent { get; }
    internal Button Choose { get; }
    internal Button BurstDown { get; }
    internal Button BurstUp { get; }
    internal int Burst { get; set; }

    internal ActionCard(NativeActionControls native, Transform parent, ActionCatalog owner, ActionOption option)
    {
        catalog = owner;
        Option = option;
        GameObject copy = NativeActionControls.Dormant(native.CardTemplate, "Action " + option.Index);
        BattleUIComponent_CharActionSelectItem card = copy.GetComponent<BattleUIComponent_CharActionSelectItem>();
        elementColors = NativeActionControls.Field<Color32[]>(card, "_targetImgElementColors") is { Length: >= 6 } colors ? colors : DefaultElementColors;
        normal = Required<GameObject>(card, "_objDefault");
        skill = Required<GameObject>(card, "_objSkill");
        targetParent = Required<GameObject>(card, "_objTargetParent");
        stats = Required<UISprite>(card, "_imgAttackIcon");
        statsObject = stats.gameObject;
        damage = Required<TMP_Text>(card, "_textDefaultDamage");
        knockBackIcon = Required<Transform>(card, "_transformKnockBackIcon");
        object attack = Required<object>(card, "_attackTargetRange");
        attackTarget = NativeActionControls.Field<TMP_Text>(attack, "_textTargetType");
        attackRange = Required<UISprite>(attack, "_imgTargetRange");
        attackArrow = NativeActionControls.Field<GameObject>(attack, "_objKnockBackArrow");
        object range = Required<object>(card, "_skillTargetRange");
        skillTarget = NativeActionControls.Field<TMP_Text>(range, "_textTargetType");
        skillRange = Required<UISprite>(range, "_imgTargetRange");
        skillIcon = Required<UISprite>(card, "_imgSkillIcon");
        skillLevel = NativeActionControls.Field<UISprite>(card, "_imgSkillLevel");
        artwork = Required<RawImage>(card, "_imgChar");
        bubbles = Required<GameObject>(card, "_objBubbleLayout").transform;
        bubbleTemplate = Required<Component>(card, "_originBubble").gameObject;
        SelectEffectParent = NativeActionControls.Field<Transform>(card, "_transformSelectEffect");
        BurstEffectParent = NativeActionControls.Field<Transform>(card, "_transformBurstEffect");
        focus = NativeActionControls.Field<GameObject>(card, "_objCostumeUseSequenceFocus");
        Component burst = NativeActionControls.Field<Component>(card, "_burstUI");
        burstArrows = burst?.gameObject;
        left = Arrow(NativeActionControls.Field<Component>(burst, "_btnLeft"));
        right = Arrow(NativeActionControls.Field<Component>(burst, "_btnRight"));
        Hide(card, "_objMonsterCharIcon", "_objDisable", "_objCoolTime", "_objSilence", "_objInterrupt", "_objBan", "_objInfinite");
        Hide(card, "_spritePrestigeSkinIcon");
        object priority = NativeActionControls.Field<object>(card, "_priorityToggle");
        Hide(priority, "_objRoot");
        object golem = NativeActionControls.Field<object>(card, "_golemToggle");
        Hide(golem, "_objRoot");
        NativeActionControls.Strip(copy, script => script is BattleCharSkillItemBubble || script is BattleCharSkillItemBubbleBurst ||
            script is ButtonOnOffComponent || script is BattleUIComponent_CharActionSelectItem_BurstUI);
        bubbleTemplate.SetActive(false);
        Root = NativeActionControls.Place(NativeActionControls.Wake(copy, parent), parent, 0, 0, NativeActionControls.CardSize.x, NativeActionControls.CardSize.y);
        Root.anchorMin = Root.anchorMax = Root.pivot = new Vector2(0, 1);
        art = artwork.gameObject.AddComponent<TextureLease>();
        Choose = Clickable(copy, copy.GetComponentInChildren<Graphic>(true));
        BurstDown = left.On == null ? null : Clickable(left.On.transform.parent.gameObject, left.On.GetComponentInChildren<Graphic>(true));
        BurstUp = right.On == null ? null : Clickable(right.On.transform.parent.gameObject, right.On.GetComponentInChildren<Graphic>(true));
        if (SelectEffectParent != null) SelectEffectParent.gameObject.SetActive(false);
        if (BurstEffectParent != null) BurstEffectParent.gameObject.SetActive(false);
        Draw();
    }

    private static T Required<T>(object owner, string name) where T : class =>
        NativeActionControls.Field<T>(owner, name) ?? throw new MissingMemberException("Native action card " + name + " unavailable.");

    private static void Hide(object owner, params string[] names)
    {
        foreach (string name in names)
        {
            object value = NativeActionControls.Field<object>(owner, name);
            GameObject target = value as GameObject ?? (value as Component)?.gameObject;
            if (target != null) target.SetActive(false);
        }
    }

    private static (GameObject, GameObject) Arrow(Component button) =>
        (NativeActionControls.Field<GameObject>(button, "_objOn"), NativeActionControls.Field<GameObject>(button, "_objOff"));

    private static Button Clickable(GameObject target, Graphic surface)
    {
        if (surface != null) surface.raycastTarget = true;
        Button button = target.AddComponent<Button>();
        button.transition = Selectable.Transition.None;
        button.targetGraphic = surface;
        return button;
    }

    // Draws the parts that never change: what the action is and how it hits.
    private void Draw()
    {
        Color color = elementColors[Math.Max(0, Math.Min(elementColors.Length - 1, catalog.Element))];
        bool isSkill = Option.IsSkill;
        normal.SetActive(!isSkill);
        skill.SetActive(isSkill);
        if (burstArrows != null) burstArrows.SetActive(false);
        if (focus != null) focus.SetActive(false);
        if (isSkill)
        {
            skillIcon.SetSprite(Option.SkillIcon);
            if (skillLevel != null)
            {
                skillLevel.gameObject.SetActive(Option.Costume.Level > 0);
                if (Option.Costume.Level > 0) skillLevel.SetSprite($"upgrade_{Option.Costume.Level}");
            }
            if (skillTarget != null)
                skillTarget.text = LocalTextInfo.GetText(CostumeInfo.GetTargetTypeTextId(Option.Skill, Option.Skill.BuffId.Count > 0 ? Option.Skill.BuffId[0] : 0));
            skillRange.color = color;
            return;
        }
        knockBackIcon.gameObject.SetActive(false);
        if (attackArrow != null) attackArrow.SetActive(false);
        damage.text = Option.Name;
        if (Option.IsDefault)
        {
            statsObject.SetActive(false);
            targetParent.SetActive(false);
            return;
        }
        CostumeInfoDTO costume = catalog.AppearanceInfo;
        targetParent.SetActive(true);
        attackRange.color = color;
        attackRange.gameObject.SetActive(true);
        if (Option.Index == 0)
        {
            statsObject.SetActive(true);
            stats.SetSprite(costume.AttackType == (int)ECostumeAttackType.PHYSICAL ? "icon_stats_physical" : "icon_stats_magical");
            attackRange.SetSprite($"range{costume.AttackRangeCount}_{costume.AttackRange}");
            if (attackTarget != null) attackTarget.text = LocalTextInfo.GetText(CostumeInfo.GetTargetTypeTextId(catalog.AppearanceSkill, 0));
            return;
        }
        statsObject.SetActive(false);
        attackRange.SetSprite(SkillInfo.GetKnockBackType(costume.NorSubAttackBuffId).GetKnockBackImageName());
        if (attackArrow != null) attackArrow.SetActive(true);
        if (attackTarget != null) attackTarget.text = LocalTextInfo.GetText(2052);
        knockBackIcon.rotation = Quaternion.Euler(0, 0, KnockBackRotation(SkillInfo.GetBuffTable(costume.NorSubAttackBuffId)?.SubType ?? 0));
        knockBackIcon.gameObject.SetActive(true);
    }

    private static float KnockBackRotation(int subType) => subType switch
    {
        2 => 180,
        3 => 270,
        4 => 90,
        5 => 135,
        6 => 45,
        7 => 225,
        8 => 315,
        _ => 0,
    };

    // Draws what depends on the player's choice, as the battle card does for
    // its reserved action: skill artwork, filled burst bubbles and arrows.
    internal void Refresh(bool selected, bool busy, bool effectShown, int available)
    {
        Choose.interactable = !busy;
        if (focus != null) focus.SetActive(selected && !effectShown);
        if (!Option.IsSkill) return;
        bool useSkillArt = selected;
        if (skillArt != useSkillArt)
        {
            skillArt = useSkillArt;
            art.Load(artwork, Option.Costume.CostumeDesignId, useSkillArt);
        }
        skillRange.SetSprite(Option.Range(Burst));
        int burstSp = Option.BurstSp(Burst);
        int baseSp = Math.Max(0, Option.Sp(Burst) - burstSp);
        int slots = Option.BurstSp(Option.MaxBurst);
        int index = 0;
        // Dimmed bubbles mark a skill the first turn's SP cannot pay for.
        BattleCharSkillItemBubble.States baseState = selected ? BattleCharSkillItemBubble.States.SELECT :
            Option.Sp(0) > available ? BattleCharSkillItemBubble.States.NOT_ENOUGHT : BattleCharSkillItemBubble.States.NORMAL;
        for (int i = 0; i < baseSp; i++) Bubble(index++, baseState);
        for (int i = 0; i < slots; i++)
            Bubble(index++, selected && i < burstSp ? BattleCharSkillItemBubble.States.BURST : BattleCharSkillItemBubble.States.BURST_EMPTY);
        for (; index < bubblePool.Count; index++) bubblePool[index].SetActive(false);
        bool arrows = selected && Option.MaxBurst > 0 && burstArrows != null;
        if (burstArrows != null) burstArrows.SetActive(arrows);
        SetArrow(left, BurstDown, arrows && Burst > 0, busy);
        SetArrow(right, BurstUp, arrows && Burst < Option.MaxBurst, busy);
    }

    private static void SetArrow((GameObject On, GameObject Off) arrow, Button button, bool enabled, bool busy)
    {
        if (arrow.On != null) arrow.On.SetActive(enabled);
        if (arrow.Off != null) arrow.Off.SetActive(!enabled);
        if (button != null) button.interactable = enabled && !busy;
    }

    // Mirrors BattleCharSkillItemBubble.Set on a copy of the card's bubble.
    private void Bubble(int index, BattleCharSkillItemBubble.States state)
    {
        while (bubblePool.Count <= index)
        {
            GameObject copy = UnityEngine.Object.Instantiate(bubbleTemplate, bubbles, false);
            copy.name = "Image - Bubble " + bubblePool.Count;
            bubblePool.Add(copy);
        }
        Transform bubble = bubblePool[index].transform;
        bool burst = state is BattleCharSkillItemBubble.States.BURST or BattleCharSkillItemBubble.States.BURST_EMPTY;
        Show(bubble, "Image - BubbleNormal", state == BattleCharSkillItemBubble.States.NORMAL);
        Show(bubble, "Image - BubbleSelect", state == BattleCharSkillItemBubble.States.SELECT);
        Show(bubble, "Image - BubbleDimd", state == BattleCharSkillItemBubble.States.NOT_ENOUGHT);
        Show(bubble, "Image - BubbleNormal - Plus", false);
        Show(bubble, "Image - BubbleNormal - Minus", false);
        Transform slot = bubble.Find("BurstSlot");
        if (slot != null)
        {
            slot.gameObject.SetActive(burst);
            Show(slot, "Image - BubbleBurstBg", state == BattleCharSkillItemBubble.States.BURST_EMPTY);
            Show(slot, "Image - BubbleBurst", state == BattleCharSkillItemBubble.States.BURST);
        }
        bubble.gameObject.SetActive(true);
    }

    private static void Show(Transform parent, string name, bool active)
    {
        Transform child = parent.Find(name);
        if (child != null) child.gameObject.SetActive(active);
    }
}
