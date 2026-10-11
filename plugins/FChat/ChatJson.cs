using System.IO;
using System.Runtime.Serialization.Json;

namespace Bd2FChat;

internal static class ChatJson
{
    internal static byte[] Serialize<T>(T value)
    {
        using var stream = new MemoryStream();
        new DataContractJsonSerializer(typeof(T)).WriteObject(stream, value);
        return stream.ToArray();
    }

    internal static T Deserialize<T>(byte[] value)
    {
        using var stream = new MemoryStream(value, false);
        return (T)new DataContractJsonSerializer(typeof(T)).ReadObject(stream);
    }
}
