using System;
using System.Collections.Generic;
using System.Globalization;

namespace Bd2CashShop;

internal static class CommercePayments
{
    private static readonly HashSet<long> Applied = new HashSet<long>();

    internal static string Receipt(long payment, CommerceProduct quote) =>
        "bd2-local-commerce-v1:" + payment.ToString(CultureInfo.InvariantCulture) + ":" +
        quote.ItemType.ToString(CultureInfo.InvariantCulture) + ":" + quote.Amount.ToString(CultureInfo.InvariantCulture);

    internal static Action AcceptOnce(long payment, Func<bool> isCurrentSession, Action debit, Action continuation)
    {
        return delegate
        {
            // The transport invokes this callback only on a successful response.
            // A replay must not charge the displayed client balance twice.
            if (isCurrentSession() && Applied.Add(payment)) debit();
            continuation?.Invoke();
        };
    }

    internal static void Clear() => Applied.Clear();
}
