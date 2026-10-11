using System;
using Bd2Login;

namespace Bd2LoginUI;

public static class ClientSession
{
    private static readonly Uri ConfiguredOrigin = ReadConfiguredOrigin();

    public static Uri ServerOrigin => LoginRuntime.ServerRoot ?? ConfiguredOrigin;
    public static int RecoveryGeneration => SessionRecovery.RecoveryGeneration;
    public static bool IsEstablished => SessionRecovery.EstablishedGameSession;
    public static bool IsQuitting => SessionRecovery.ApplicationQuitting;

    private static Uri ReadConfiguredOrigin()
    {
        string configured = Environment.GetEnvironmentVariable("BD2_CLIENT_SERVER_ORIGIN");
        if (string.IsNullOrEmpty(configured)) return null;
        if (!Uri.TryCreate(configured, UriKind.Absolute, out Uri origin) ||
            (origin.Scheme != Uri.UriSchemeHttps && !(origin.Scheme == Uri.UriSchemeHttp && origin.IsLoopback)) ||
            string.IsNullOrEmpty(origin.Host) || origin.UserInfo.Length != 0 ||
            origin.AbsolutePath != "/" || origin.Query.Length != 0 || origin.Fragment.Length != 0)
            throw new InvalidOperationException("Validated client server origin is unavailable");
        return new Uri(ServerLoginPreferences.NormalizeOrigin(origin) + "/");
    }
}
