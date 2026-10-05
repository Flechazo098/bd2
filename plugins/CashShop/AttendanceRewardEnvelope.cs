using System;
using Google.Protobuf;

namespace Bd2CashShop;

// Owned-server additions stay in protobuf unknown fields. The frozen client
// AttendanceResponse/EventRewardResponse schemas and their handlers stay intact.
internal static class AttendanceRewardEnvelope
{
    internal static bool TryRead(IMessage response, out ByteString rewards, out string receipt)
    {
        rewards = null;
        receipt = null;
        if (response == null || response.CalculateSize() > 8 * 1024 * 1024) return false;
        using (var input = new CodedInputStream(response.ToByteArray()))
        {
            uint tag;
            while ((tag = input.ReadTag()) != 0)
            {
                if (tag == (1001u << 3 | 2u))
                {
                    if (rewards != null) return false;
                    rewards = input.ReadBytes();
                }
                else if (tag == (1002u << 3 | 2u))
                {
                    if (receipt != null) return false;
                    receipt = input.ReadString();
                }
                else input.SkipLastField();
            }
        }
        return rewards != null && !string.IsNullOrWhiteSpace(receipt) && receipt.Length <= 1024;
    }
}
