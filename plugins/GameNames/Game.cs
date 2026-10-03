using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Linq.Expressions;
using System.Reflection;
using System.Runtime.Serialization.Json;
using System.Security.Cryptography;
using BD2.GameNames.Internal;

namespace BD2.GameNames;

/// <summary>Readable names for string-based reflection. Unknown names retain their literal spelling.</summary>
public static class Game
{
    private sealed class Index
    {
        internal readonly NameTable Table;
        internal readonly Dictionary<string, TypeName> Types;
        internal readonly Dictionary<int, MemberName> Tokens;
        internal Index()
        {
            using (var stream = typeof(Game).Assembly.GetManifestResourceStream("BD2.GameNames.names.json.gz"))
            using (var gzip = new GZipStream(stream ?? throw new InvalidDataException("BD2.GameNames: embedded table missing"), CompressionMode.Decompress))
                Table = (NameTable)new DataContractJsonSerializer(typeof(NameTable)).ReadObject(gzip);
            if (Table.schema_version != 1) throw new InvalidDataException("BD2.GameNames: unsupported table schema");
            Types = Table.types.ToDictionary(t => t.readable, StringComparer.Ordinal);
            Tokens = Table.members.ToDictionary(m => m.token);
        }
    }
    private static readonly Lazy<Index> Names = new Lazy<Index>(() => new Index());
    private static readonly ConcurrentDictionary<string, Type> TypeCache = new ConcurrentDictionary<string, Type>();
    private static readonly ConcurrentDictionary<string, MemberInfo> MemberCache = new ConcurrentDictionary<string, MemberInfo>();
    private static readonly object ValidationLock = new object();
    private static string ValidatedStamp;
    /// <summary>Gets the game version embedded in this package.</summary>
    public static string GameVersion => Names.Value.Table.game_version;

    /// <summary>Translates a full readable type name. Unknown names retain their literal spelling.</summary>
    public static string TypeName(string readableFullName)
    {
        if (readableFullName == null) throw new ArgumentNullException(nameof(readableFullName));
        return Names.Value.Types.TryGetValue(readableFullName.Replace('/', '+'), out var entry) ? entry.original : readableFullName;
    }

    /// <summary>Finds a readable type in Assembly-CSharp or loaded assemblies; returns null when absent.</summary>
    public static Type FindType(string readableFullName)
    {
        if (readableFullName == null) throw new ArgumentNullException(nameof(readableFullName));
        if (TypeCache.TryGetValue(readableFullName, out var cached)) return cached;
        string original = TypeName(readableFullName);
        Type found = GameAssembly().GetType(original, false);
        if (found == null)
            foreach (var assembly in AppDomain.CurrentDomain.GetAssemblies())
            {
                found = assembly.GetType(original, false);
                if (found != null) break;
            }
        if (found != null) TypeCache.TryAdd(readableFullName, found);
        return found;
    }

    private static Assembly GameAssembly() => Assembly.Load("Assembly-CSharp");
    private static bool Matches(MemberInfo member, string kind, string readable)
    {
        if (member == null) throw new ArgumentNullException(nameof(member));
        if (readable == null) throw new ArgumentNullException(nameof(readable));
        // Metadata tokens retain declaration identity through inheritance and closed generics.
        // A hidden literal member must not be mistaken for a renamed member of a base class.
        if (member.DeclaringType.Assembly.GetName().Name == Names.Value.Table.assembly_name && Names.Value.Tokens.TryGetValue(member.MetadataToken, out var entry))
            return entry.kind == kind && entry.readable == readable;
        return member.Name == readable;
    }

    /// <summary>Translate coroutine/member strings. Overloads with different runtime names require Method/MethodName(MethodInfo).</summary>
    public static string MemberName(Type type, string readable, GameMemberKind kind = GameMemberKind.Method)
    {
        if (type == null) throw new ArgumentNullException(nameof(type));
        if (readable == null) throw new ArgumentNullException(nameof(readable));
        string key = kind switch
        {
            GameMemberKind.Method => "method",
            GameMemberKind.Field => "field",
            GameMemberKind.Property => "property",
            GameMemberKind.Event => "event",
            _ => throw new ArgumentOutOfRangeException(nameof(kind))
        };
        const BindingFlags all = BindingFlags.Instance | BindingFlags.Static | BindingFlags.Public | BindingFlags.NonPublic;
        var reflected = kind switch
        {
            GameMemberKind.Method => type.GetMethods(all).Cast<MemberInfo>(),
            GameMemberKind.Field => type.GetFields(all).Cast<MemberInfo>(),
            GameMemberKind.Property => type.GetProperties(all).Cast<MemberInfo>(),
            GameMemberKind.Event => type.GetEvents(all).Cast<MemberInfo>(),
            _ => throw new ArgumentOutOfRangeException(nameof(kind))
        };
        var names = reflected.Where(m => Matches(m, key, readable)).Select(m => m.Name).Distinct().ToArray();
        if (names.Length == 0) return readable;
        if (names.Length != 1) throw new AmbiguousMatchException(type.FullName + "." + readable + ": supply the member signature");
        return names[0];
    }
    /// <summary>Returns the actual CLR method name for APIs that accept strings.</summary>
    public static string MethodName(MethodInfo method) => method?.Name ?? throw new ArgumentNullException(nameof(method));
    /// <summary>Tests a reflected method against its readable name, preserving overload identity.</summary>
    public static bool IsGameMethod(this MethodInfo method, string readable) => Matches(method, "method", readable);
    /// <summary>Translates a readable parameter name using the method declaration; unknown names remain unchanged.</summary>
    public static string ParameterName(MethodBase method, string readable)
    {
        if (method == null) throw new ArgumentNullException(nameof(method));
        if (readable == null) throw new ArgumentNullException(nameof(readable));
        if (method.DeclaringType.Assembly.GetName().Name == Names.Value.Table.assembly_name && Names.Value.Tokens.TryGetValue(method.MetadataToken, out var entry))
        {
            var p = entry.parameters.SingleOrDefault(n => n.readable == readable);
            if (p != null) return p.original;
        }
        return readable;
    }

    /// <summary>Extracts a MethodInfo from a call expression without executing the expression.</summary>
    public static MethodInfo Method<T>(Expression<Action<T>> expression) => Call(expression);
    /// <summary>Extracts a MethodInfo from a call expression without executing the expression.</summary>
    public static MethodInfo Method(Expression<Action> expression) => Call(expression);
    /// <summary>Extracts a property getter from an expression without evaluating the property.</summary>
    public static MethodInfo Getter<T, TResult>(Expression<Func<T, TResult>> expression) => PropertyGetter(expression);
    /// <summary>Extracts a property getter from an expression without evaluating the property.</summary>
    public static MethodInfo Getter<TResult>(Expression<Func<TResult>> expression) => PropertyGetter(expression);

    private static MethodInfo Call(LambdaExpression expression)
    {
        if (expression == null) throw new ArgumentNullException(nameof(expression));
        return expression.Body is MethodCallExpression call ? call.Method : throw new ArgumentException("Expected a method call", nameof(expression));
    }
    private static MethodInfo PropertyGetter(LambdaExpression expression)
    {
        if (expression == null) throw new ArgumentNullException(nameof(expression));
        return expression.Body is MemberExpression member && member.Member is PropertyInfo property
            ? property.GetGetMethod(true) : throw new ArgumentException("Expected a property access", nameof(expression));
    }

    private static T Cached<T>(Type type, string kind, string readable, BindingFlags flags, Type[] parameters, Func<T> resolve) where T : MemberInfo
    {
        if (readable == null) throw new ArgumentNullException(nameof(readable));
        if (type == null) return null;
        string key = type.AssemblyQualifiedName + "|" + kind + "|" + readable + "|" + (int)flags + "|" + (parameters == null ? "*" : string.Join(";", parameters.Select(p => p.AssemblyQualifiedName)));
        if (MemberCache.TryGetValue(key, out var cached)) return (T)cached;
        T found = resolve();
        if (found != null) MemberCache.TryAdd(key, found);
        return found;
    }
    /// <summary>Finds a method by readable name and binding flags. Returns null when absent; ambiguous matches throw.</summary>
    public static MethodInfo GetGameMethod(this Type type, string readable, BindingFlags flags) => Cached(type, "method", readable, flags, null,
        () => Single(type.GetMethods(flags).Where(m => Matches(m, "method", readable)), type, readable));
    /// <summary>Finds a method by readable name and binding flags. Returns null when absent; ambiguous matches throw.</summary>
    public static MethodInfo GetGameMethod(this Type type, string readable, BindingFlags flags, Binder binder, Type[] parameters, ParameterModifier[] modifiers)
    {
        if (readable == null) throw new ArgumentNullException(nameof(readable));
        if (parameters == null) throw new ArgumentNullException(nameof(parameters));
        if (parameters.Any(p => p == null)) throw new ArgumentException("Parameter types cannot contain null", nameof(parameters));
        if (type == null) return null;
        MethodInfo Resolve()
        {
            var candidates = type.GetMethods(flags).Where(m => Matches(m, "method", readable)).ToArray();
            if (candidates.Length == 0) return null;
            return (MethodInfo)(binder ?? Type.DefaultBinder).SelectMethod(flags, candidates, parameters, modifiers);
        }
        // Custom binders/modifiers can affect resolution; do not cache those calls.
        if (binder != null || modifiers != null) return Resolve();
        return Cached(type, "method", readable, flags, parameters, Resolve);
    }
    private static T Single<T>(IEnumerable<T> members, Type type, string readable) where T : MemberInfo
    {
        var candidates = members.Take(2).ToArray();
        if (candidates.Length > 1) throw new AmbiguousMatchException(type.FullName + "." + readable + ": supply the member signature");
        return candidates.FirstOrDefault();
    }
    /// <summary>Finds a field by readable name and binding flags. Returns null when absent.</summary>
    public static FieldInfo GetGameField(this Type type, string readable, BindingFlags flags) => Cached(type, "field", readable, flags, null,
        () => Single(type.GetFields(flags).Where(m => Matches(m, "field", readable)), type, readable));
    /// <summary>Finds a property by readable name and binding flags. Returns null when absent; ambiguous matches throw.</summary>
    public static PropertyInfo GetGameProperty(this Type type, string readable, BindingFlags flags) => Cached(type, "property", readable, flags, null,
        () => Single(type.GetProperties(flags).Where(m => Matches(m, "property", readable)), type, readable));
    /// <summary>Finds an event by readable name and binding flags. Returns null when absent.</summary>
    public static EventInfo GetGameEvent(this Type type, string readable, BindingFlags flags) => Cached(type, "event", readable, flags, null,
        () => Single(type.GetEvents(flags).Where(m => Matches(m, "event", readable)), type, readable));

    /// <summary>Validate plugin/table version, binary fingerprint and known metadata entries before installing patches.</summary>
    public static void Validate(Assembly plugin, Action<string> log = null) => Validate(plugin, GameVersion, log);

    /// <summary>Validates only the game binary against the embedded table for plugins that do not use the compiler SDK.</summary>
    public static void ValidateGame(Action<string> log = null) => ValidateCore(null, GameVersion, log);

    /// <summary>Validates a compiler SDK plugin, its expected game version, the table and the game binary. Throws on mismatch.</summary>
    public static void Validate(Assembly plugin, string expectedVersion, Action<string> log)
    {
        if (plugin == null) throw new ArgumentNullException(nameof(plugin));
        if (expectedVersion == null) throw new ArgumentNullException(nameof(expectedVersion));
        ValidateCore(plugin, expectedVersion, log);
    }

    private static void ValidateCore(Assembly plugin, string expectedVersion, Action<string> log)
    {
        try
        {
            var table = Names.Value.Table;
            string stamp = table.game_version + "|" + table.assembly_sha256 + "|" + table.mapping_sha256;
            var metadata = plugin?.GetCustomAttributes<AssemblyMetadataAttribute>().SingleOrDefault(a => a.Key == "BD2.GameNames");
            if (expectedVersion != table.game_version || plugin != null && metadata?.Value != stamp)
                throw new InvalidDataException("plugin/table mismatch; rebuild the plugin and BD2.GameNames together");
            lock (ValidationLock)
            {
                if (ValidatedStamp != stamp)
                {
                    var assembly = GameAssembly();
                    if (assembly.ManifestModule.ModuleVersionId.ToString() != table.assembly_mvid)
                        throw new InvalidDataException("Assembly-CSharp MVID mismatch");
                    using (var sha = SHA256.Create())
                    using (var stream = File.OpenRead(assembly.Location))
                    {
                        string actual = BitConverter.ToString(sha.ComputeHash(stream)).Replace("-", "").ToLowerInvariant();
                        if (actual != table.assembly_sha256) throw new InvalidDataException("Assembly-CSharp SHA-256 mismatch");
                    }
                    const BindingFlags all = BindingFlags.Instance | BindingFlags.Static | BindingFlags.Public | BindingFlags.NonPublic;
                    var app = FindType("AppManager") ?? throw new TypeLoadException("AppManager");
                    var intro = FindType("IntroUI") ?? throw new TypeLoadException("IntroUI");
                    var network = FindType("BDNetwork.NetworkManager") ?? throw new TypeLoadException("BDNetwork.NetworkManager");
                    if (app.GetGameProperty("IsPlatformLogin", all)?.PropertyType != typeof(bool) ||
                        intro.GetGameMethod("SendMaintenanceInfo", all, null, new[] { typeof(bool) }, null) == null ||
                        network.GetGameMethod("GetPachedGameDataPath", all, null, Type.EmptyTypes, null)?.ReturnType != typeof(string))
                        throw new MissingMemberException("known game-name probes failed");
                    ValidatedStamp = stamp;
                }
            }
            log?.Invoke("BD2.GameNames self-check passed: game=" + table.game_version + ", MVID=" + table.assembly_mvid);
        }
        catch (Exception ex)
        {
            log?.Invoke("BD2.GameNames SELF-CHECK FAILED: expected game=" + expectedVersion + "; " + ex.Message + "; game patches will not be installed");
            throw;
        }
    }
}
