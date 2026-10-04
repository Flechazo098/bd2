using System;
using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace Bd2LoginUI;

internal sealed class NativeProxyConfigurationException : InvalidOperationException
{
    public NativeProxyConfigurationException(string message) : base(message) { }
}

internal sealed class NativeProxySettings
{
    public string ProxyUrl { get; }
    public string Key { get; }
    internal NativeProxySettings(string proxy, string configuration)
    {
        ProxyUrl = proxy;
        using (var hash = SHA256.Create())
            Key = Convert.ToBase64String(hash.ComputeHash(Encoding.UTF8.GetBytes(configuration + "\n" + proxy)));
    }
}

// Only the player's persisted client choice controls owned HTTP requests.
// Local Identity and Client Studio supply this process-local value; inherited
// system, Unity and standard HTTP proxy settings are never used as a fallback.
internal static class NativeProxyPolicy
{
    private const string ProxyVariable = "BD2_CLIENT_PROXY_URL";

    public static NativeProxySettings Resolve(Uri destination)
    {
        if (destination == null || !destination.IsAbsoluteUri) throw new ArgumentException("Invalid proxy destination");
        if (destination.IsLoopback) return new NativeProxySettings(null, "loopback");
        string selected = Environment.GetEnvironmentVariable(ProxyVariable);
        if (string.IsNullOrWhiteSpace(selected)) return new NativeProxySettings(null, "client-direct");
        string proxy = NormalizeProxy(selected);
        return new NativeProxySettings(proxy, "client-manual");
    }

    private static string NormalizeProxy(string raw)
    {
        raw = raw.Trim();
        if (raw.IndexOfAny(new[] { '?', '#', '@', '%' }) >= 0) throw InvalidProxy();
        foreach (char value in raw)
            if (char.IsWhiteSpace(value) || char.IsControl(value)) throw InvalidProxy();
        if (!Uri.TryCreate(raw, UriKind.Absolute, out Uri proxy) || proxy.Scheme != "http" ||
            string.IsNullOrEmpty(proxy.Host) || !string.IsNullOrEmpty(proxy.UserInfo) ||
            !string.IsNullOrEmpty(proxy.Query) || !string.IsNullOrEmpty(proxy.Fragment) || proxy.AbsolutePath != "/")
            throw InvalidProxy();
        string authority = raw.Substring(raw.IndexOf("://", StringComparison.Ordinal) + 3).TrimEnd('/');
        int separator = authority.LastIndexOf(':');
        if (separator <= 0) throw InvalidProxy();
        string portText = authority.Substring(separator + 1);
        foreach (char value in portText) if (value < '0' || value > '9') throw InvalidProxy();
        if (!int.TryParse(portText, NumberStyles.None, CultureInfo.InvariantCulture, out int port) || port < 1 || port > 65535)
            throw InvalidProxy();
        string host = proxy.IdnHost.Trim('[', ']');
        if (proxy.HostNameType == UriHostNameType.IPv6) host = "[" + host + "]";
        return "http://" + host + ":" + port.ToString(CultureInfo.InvariantCulture);
    }

    private static NativeProxyConfigurationException InvalidProxy() =>
        new NativeProxyConfigurationException("Client proxy must be an HTTP proxy address with an explicit port and no credentials or path");
}
