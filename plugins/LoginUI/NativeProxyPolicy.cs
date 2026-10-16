using System;
using Bd2.Client;
using System.Security.Cryptography;
using System.Text;

namespace Bd2LoginUI;

internal sealed class NativeProxyConfigurationException(string message) : InvalidOperationException(message)
{
}

internal sealed class NativeProxySettings
{
    public string ProxyUrl { get; }
    public string Key { get; }
    internal NativeProxySettings(string proxy, string configuration)
    {
        ProxyUrl = proxy;
        using var hash = SHA256.Create();
        Key = Convert.ToBase64String(hash.ComputeHash(Encoding.UTF8.GetBytes(configuration + "\n" + proxy)));
    }
}

internal static class NativeProxyPolicy
{
    public static NativeProxySettings Resolve(Uri destination)
    {
        if (destination == null || !destination.IsAbsoluteUri) throw new ArgumentException("Invalid proxy destination");
        if (destination.IsLoopback) return new NativeProxySettings(null, "loopback");
        string selected = ClientSettings.Current.proxy_url;
        if (string.IsNullOrWhiteSpace(selected)) return new NativeProxySettings(null, "client-direct");
        return new NativeProxySettings(selected, "client-manual");
    }

}
