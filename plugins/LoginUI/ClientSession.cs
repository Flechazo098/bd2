using System;
using Bd2.Client;

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
        return new Uri(ClientSettings.Current.server_origin + "/");
    }
}
