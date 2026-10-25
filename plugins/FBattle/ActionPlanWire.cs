using System.Collections.Generic;
using System.IO;
using System.Linq;
using Google.Protobuf;

namespace Bd2FBattle;

internal static class ActionPlanWire
{
    internal const int PlanField = 100;
    internal const int ActiveSlotField = 101;

    internal static byte[] ReplacePlan(byte[] preset, IEnumerable<ActionChoice> choices)
    {
        using var stream = new MemoryStream();
        using (var input = new CodedInputStream(preset))
        {
            while (!input.IsAtEnd)
            {
                int start = checked((int)input.Position);
                uint tag = input.ReadTag();
                if (tag == 0) break;
                input.SkipLastField();
                if (WireFormat.GetTagFieldNumber(tag) != PlanField)
                    stream.Write(preset, start, checked((int)input.Position) - start);
            }
        }
        byte[] extension = Extension(choices);
        stream.Write(extension, 0, extension.Length);
        return stream.ToArray();
    }

    internal static byte[] Extension(IEnumerable<ActionChoice> choices)
    {
        using var planStream = new MemoryStream();
        using (var writer = new CodedOutputStream(planStream, true))
        {
            writer.WriteTag(1, WireFormat.WireType.Varint);
            writer.WriteUInt32(1);
            foreach (ActionChoice choice in choices.OrderBy(c => c.CharacterIndex))
            {
                using var entryStream = new MemoryStream();
                using (var entry = new CodedOutputStream(entryStream, true))
                {
                    entry.WriteTag(1, WireFormat.WireType.Varint);
                    entry.WriteInt64(choice.CharacterIndex);
                    entry.WriteTag(2, WireFormat.WireType.Varint);
                    entry.WriteInt64(choice.CostumeIndex);
                    entry.WriteTag(3, WireFormat.WireType.Varint);
                    entry.WriteInt32(choice.BurstLevel);
                }
                writer.WriteTag(2, WireFormat.WireType.LengthDelimited);
                writer.WriteBytes(ByteString.CopyFrom(entryStream.ToArray()));
            }
        }
        using var stream = new MemoryStream();
        using (var output = new CodedOutputStream(stream, true))
        {
            output.WriteTag(PlanField, WireFormat.WireType.LengthDelimited);
            output.WriteBytes(ByteString.CopyFrom(planStream.ToArray()));
        }
        return stream.ToArray();
    }

    internal static bool TryRead(byte[] raw, out Dictionary<long, ActionChoice> choices, out int? activeSlot)
    {
        choices = null;
        activeSlot = null;
        using var input = new CodedInputStream(raw);
        uint tag;
        while ((tag = input.ReadTag()) != 0)
        {
            if (tag == WireFormat.MakeTag(PlanField, WireFormat.WireType.LengthDelimited))
            {
                if (choices != null) throw new InvalidDataException("Duplicate FBattle action plan");
                choices = DecodePlan(input.ReadBytes().ToByteArray());
            }
            else if (tag == WireFormat.MakeTag(ActiveSlotField, WireFormat.WireType.Varint))
            {
                activeSlot = checked((int)input.ReadUInt64());
            }
            else input.SkipLastField();
        }
        return choices != null;
    }

    private static Dictionary<long, ActionChoice> DecodePlan(byte[] raw)
    {
        var choices = new Dictionary<long, ActionChoice>();
        int schema = 0;
        using var input = new CodedInputStream(raw);
        uint tag;
        while ((tag = input.ReadTag()) != 0)
        {
            switch (tag)
            {
                case 8: schema = input.ReadInt32(); break;
                case 18:
                    ActionChoice choice = DecodeChoice(input.ReadBytes().ToByteArray());
                    if (choices.Count >= 12 || choices.ContainsKey(choice.CharacterIndex))
                        throw new InvalidDataException("Invalid FBattle action plan members");
                    choices.Add(choice.CharacterIndex, choice);
                    break;
                default: input.SkipLastField(); break;
            }
        }
        if (schema != 1) throw new InvalidDataException("Unsupported FBattle action plan format");
        return choices;
    }

    private static ActionChoice DecodeChoice(byte[] raw)
    {
        long character = 0, costume = 0;
        int burst = 0;
        using var input = new CodedInputStream(raw);
        uint tag;
        while ((tag = input.ReadTag()) != 0)
        {
            switch (tag)
            {
                case 8: character = input.ReadInt64(); break;
                case 16: costume = input.ReadInt64(); break;
                case 24: burst = input.ReadInt32(); break;
                default: input.SkipLastField(); break;
            }
        }
        if (character <= 0 || costume < -1 || burst < 0 || (costume <= 0 && burst != 0))
            throw new InvalidDataException("Invalid FBattle action choice");
        return new ActionChoice(character, costume, burst);
    }
}
