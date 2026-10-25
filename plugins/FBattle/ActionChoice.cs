using System;

namespace Bd2FBattle;

internal sealed class ActionChoice(long characterIndex, long costumeIndex, int burstLevel) : IEquatable<ActionChoice>
{
    internal long CharacterIndex { get; } = characterIndex;
    internal long CostumeIndex { get; } = costumeIndex;
    internal int BurstLevel { get; } = burstLevel;

    internal ActionChoice Clone() => new(CharacterIndex, CostumeIndex, BurstLevel);

    public bool Equals(ActionChoice other) => other != null && CharacterIndex == other.CharacterIndex &&
        CostumeIndex == other.CostumeIndex && BurstLevel == other.BurstLevel;

    public override bool Equals(object obj) => obj is ActionChoice other && Equals(other);
    public override int GetHashCode() => HashCode.Combine(CharacterIndex, CostumeIndex, BurstLevel);
}
