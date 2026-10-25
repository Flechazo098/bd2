using System;
using System.Collections.Generic;
using System.Linq;
using BDNetwork;
using Proto.Design.common;
using Proto.Net;
using TextComponent = BD2.GameSdk.Component;

namespace Bd2FBattle;

internal sealed class ActionCatalog
{
    internal CharDBInfo Character { get; }
    internal CharTable Definition { get; }
    internal CostumeDBInfo Appearance { get; }
    internal CostumeInfoDTO AppearanceInfo { get; }
    internal SkillTable AppearanceSkill { get; }
    internal int Element => Definition.Element;
    internal string Name => LocalTextInfo.GetNameText(Definition.CharNameTextId);
    internal List<ActionOption> Options { get; } = [];

    internal ActionCatalog(DeckDBInfo deck, PresetDBInfo preset)
    {
        Character = CommonPacket.GetCharInfo(deck.CharInvenIndex) ?? throw new InvalidOperationException("Preset character is unavailable.");
        Definition = CharInfo.GetCharInfo(Character.Id);
        long costumeIndex = preset.DeckInfo.FirstOrDefault(item => item.DeckBaseInfo?.CharInvenIndex == Character.InvenIndex)?.CostumeInvenIndex ?? Character.UseCostume;
        Appearance = CostumePacket.GetCostumeDBInfo(costumeIndex) ?? CostumePacket.GetCostumeDBInfo(Character.UseCostume) ??
            throw new InvalidOperationException("Preset costume is unavailable.");
        AppearanceInfo = CostumeInfo.GetCostumeInfo(Appearance.Id);
        AppearanceSkill = SkillInfo.GetSkillInfo(AppearanceInfo.SkillGroupId, Appearance.Level);
        Options.Add(new ActionOption(long.MinValue, Text("fbattle.action.default")));
        Options.Add(new ActionOption(0, Text("fbattle.action.attack")));
        Options.Add(new ActionOption(-1, Text("fbattle.action.knockback")));
        CostumePacket.GetCostumeDBInfoList(Character, out List<CostumeDBInfo> costumes);
        foreach (CostumeDBInfo costume in costumes.OrderBy(item => item.InvenIndex == costumeIndex ? 0 : 1).ThenBy(item => item.Id))
            Options.Add(new ActionOption(costume));
    }

    internal static string Text(string key) => TextComponent.Translatable(key).GetString();
}

internal sealed class ActionOption
{
    internal long Index { get; }
    internal string Name { get; }
    internal CostumeDBInfo Costume { get; }
    internal CostumeInfoDTO Definition { get; }
    internal SkillTable Skill { get; }
    internal string SkillIcon { get; }
    internal bool IsSkill => Costume != null;
    internal bool IsDefault => Index == long.MinValue;
    internal int MaxBurst => Math.Max(0, Costume?.BurstLevel ?? 0);

    internal ActionOption(long index, string name) { Index = index; Name = name; }

    internal ActionOption(CostumeDBInfo costume)
    {
        Costume = costume;
        Index = costume.InvenIndex;
        Definition = CostumeInfo.GetCostumeInfo(costume.Id);
        Name = LocalTextInfo.GetNameText(Definition.CostumeNameTextId);
        Skill = SkillInfo.GetSkillInfo(Definition.SkillGroupId, costume.Level);
        SkillIcon = SkillInfo.GetSkillDesignTable(Skill.SkillDesignId).SkillIconSpriteName;
    }

    internal int Sp(int burst) => IsSkill ? SkillInfo.GetSpReqCount(Skill, Costume.Id, [.. Costume.PotentialId], burst, null) : 0;

    // SP the burst levels add on top of the skill, as the battle card counts its burst bubbles.
    internal int BurstSp(int burst) => IsSkill ? CostumeInfo.GetCostumeBurstTableNeedSp(Skill.GroupId, burst) : 0;

    internal string Range(int burst)
    {
        if (!IsSkill) return null;
        (int range, int count) = SkillInfo.GetAttackRangeOption(Skill, Costume.Id, [.. Costume.PotentialId], null, burst);
        return $"range{count}_{range}";
    }
}
