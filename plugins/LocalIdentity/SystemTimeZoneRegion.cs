using System;
using System.Reflection;
using System.Runtime.InteropServices;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2LocalIdentity;

// Neo's device country is derived directly from the OS time-zone identifier.
// Maps an explicit zone identifier to its representative territory, never a UTC offset.
// Unmapped identifiers have no inferred country.
internal static class SystemTimeZoneRegion
{
    private static ManualLogSource log;
    private static string lastLoggedIdentifier;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        log = logger;
        // NeoMobilePlatform is internal in the original SDK; typeof cannot reference it.
        Type platform = Assembly.Load("Neo.Unity.Device").GetType("Neo.Unity.Platform.NeoMobilePlatform", false);
        MethodInfo method = platform?.GetMethod("GetDeviceCountryIso", BindingFlags.Instance | BindingFlags.Public,
            null, Type.EmptyTypes, null);
        if (method == null || method.ReturnType != typeof(string))
            throw new MissingMethodException("NeoMobilePlatform.GetDeviceCountryIso was not found");
        harmony.Patch(method, prefix: new HarmonyMethod(typeof(SystemTimeZoneRegion), nameof(GetDeviceCountryPrefix)));
        logger.LogInfo("Neo device country uses the operating system time zone");
    }

    private static bool GetDeviceCountryPrefix(ref string __result)
    {
        string identifier = ReadId();
        __result = SystemTimeZoneCountries.Resolve(identifier);
        if (!string.Equals(lastLoggedIdentifier, identifier, StringComparison.Ordinal))
        {
            lastLoggedIdentifier = identifier;
            if (__result.Length == 0)
                log?.LogWarning("OS time zone country is unknown: source=OS, timeZone=" + identifier);
            else
                log?.LogInfo("OS time zone country: source=OS, timeZone=" + identifier + ", country=" + __result);
        }
        return false;
    }

    public static string ReadId()
    {
        try
        {
            if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
            {
                DynamicTimeZoneInformation information;
                if (GetDynamicTimeZoneInformation(out information) == uint.MaxValue) return string.Empty;
                return information.TimeZoneKeyName ?? string.Empty;
            }
            if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
            {
                IntPtr zone = CFTimeZoneCopySystem();
                if (zone == IntPtr.Zero) return string.Empty;
                try
                {
                    IntPtr name = CFTimeZoneGetName(zone);
                    if (name == IntPtr.Zero) return string.Empty;
                    long length = CFStringGetLength(name).ToInt64();
                    if (length <= 0 || length > 1024) return string.Empty;
                    ushort[] characters = new ushort[(int)length];
                    CFStringGetCharacters(name, new CFRange { Length = new IntPtr(length) }, characters);
                    char[] text = new char[characters.Length];
                    for (int i = 0; i < text.Length; i++) text[i] = (char)characters[i];
                    return new string(text);
                }
                finally { CFRelease(zone); }
            }
        }
        catch (Exception)
        {
            log?.LogWarning("Could not read the operating system time zone");
        }
        return string.Empty;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct SystemTime { public ushort Year, Month, DayOfWeek, Day, Hour, Minute, Second, Milliseconds; }
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct DynamicTimeZoneInformation
    {
        public int Bias;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string StandardName;
        public SystemTime StandardDate;
        public int StandardBias;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string DaylightName;
        public SystemTime DaylightDate;
        public int DaylightBias;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string TimeZoneKeyName;
        public byte DynamicDaylightTimeDisabled;
    }
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode)]
    private static extern uint GetDynamicTimeZoneInformation(out DynamicTimeZoneInformation information);
    [StructLayout(LayoutKind.Sequential)]
    private struct CFRange { public IntPtr Location, Length; }
    private const string CoreFoundation = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation";
    [DllImport(CoreFoundation)] private static extern IntPtr CFTimeZoneCopySystem();
    [DllImport(CoreFoundation)] private static extern IntPtr CFTimeZoneGetName(IntPtr zone);
    [DllImport(CoreFoundation)] private static extern IntPtr CFStringGetLength(IntPtr value);
    [DllImport(CoreFoundation)] private static extern void CFStringGetCharacters(IntPtr value, CFRange range, [Out] ushort[] characters);
    [DllImport(CoreFoundation)] private static extern void CFRelease(IntPtr value);
}
