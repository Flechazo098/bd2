using System;
using System.Collections.Generic;
using System.IO;
using System.Net;
using System.Net.Security;
using System.Net.Sockets;
using System.Reflection;
using System.Runtime.Serialization;
using System.Runtime.Serialization.Json;
using System.Security.Authentication;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using BD2.GameNames;
using HarmonyLib;
using NetworkHub.Client;

namespace Bd2LoginUI;

internal static class RealtimeBridge
{
    private static readonly object Sync = new();
    private static readonly Dictionary<string, TcpBridge> Bridges = new(StringComparer.Ordinal);
    private static UdpBridge udpBridge;
    private static int forceActionTCP;

    internal static void Install(Harmony harmony)
    {
        NativeConnectivity.Install(harmony);
        ConstructorInfo constructor = typeof(NetworkClient).GetConstructor([typeof(BDNetwork.NetworkTCPManager),
            typeof(string), typeof(string), typeof(int), typeof(IProcessingStrategy), typeof(PacketSequencer)])
            ?? throw new MissingMethodException("Native TCP constructor was not found");
        harmony.Patch(constructor, prefix: new HarmonyMethod(typeof(RealtimeBridge), nameof(TcpConstructorPrefix)));
        MethodInfo init = Game.Method<NetworkUDPClient>(client => client.Init(null, null))
            ?? throw new MissingMethodException("Native UDP initialization was not found");
        harmony.Patch(init, prefix: new HarmonyMethod(typeof(RealtimeBridge), nameof(UdpInitPrefix)));
        harmony.Patch(Game.Method<BDNetwork.NetworkUDPManager>(manager => manager.StartConnectNetUDP(default, null, null, null)),
            prefix: new HarmonyMethod(typeof(RealtimeBridge), nameof(UdpConnectPrefix)));
        harmony.Patch(Game.Method<BDNetwork.NetworkUDPManager>(manager => manager.ReconnectUDP(null)),
            prefix: new HarmonyMethod(typeof(RealtimeBridge), nameof(UdpConnectPrefix)));
        harmony.Patch(Game.Getter<BDNetwork.NetworkUDPManager, bool>(manager => manager.IsAvailableUDP),
            postfix: new HarmonyMethod(typeof(RealtimeBridge), nameof(UdpAvailablePostfix)));
    }

    private static void TcpConstructorPrefix(string __1, ref string __2, ref int __3)
    {
        if (LoginRuntime.ServerRoot == null || !string.Equals(__2, LoginRuntime.ServerRoot.Host, StringComparison.OrdinalIgnoreCase)) return;
        string channel = __1 switch { "Chat" => "matching", "Action" => "action", "RandomDefense" => "defense", "Fishing" => "fishing", _ => null };
        if (channel == null) return;
        if (channel == "action")
            Volatile.Write(ref forceActionTCP, NativeProxyPolicy.Resolve(new UriBuilder("https", __2, __3).Uri).ProxyUrl == null ? 0 : 1);
        lock (Sync)
        {
            if (Bridges.TryGetValue(channel, out TcpBridge previous)) previous.Dispose();
            var bridge = new TcpBridge(channel);
            Bridges[channel] = bridge;
            __2 = "127.0.0.1";
            __3 = bridge.Port;
        }
    }

    private static bool UdpConnectPrefix(BDNetwork.NetworkUDPManager __instance)
    {
        if (Volatile.Read(ref forceActionTCP) == 0) return true;
        if (__instance.NetClient != null)
        {
            __instance.NetClient.IsAvailableUDP = false;
            __instance.CloseUdpClient();
        }
        __instance.OnChangedAvailableStateUDP(false);
        return false;
    }

    private static void UdpAvailablePostfix(ref bool __result)
    {
        if (Volatile.Read(ref forceActionTCP) != 0) __result = false;
    }

    private static void UdpInitPrefix(ref string __0)
    {
        if (LoginRuntime.ServerRoot == null) return;
        lock (Sync)
        {
            if (!Bridges.TryGetValue("action", out TcpBridge action)) throw new InvalidOperationException("Authenticated action transport is unavailable");
            udpBridge?.Dispose();
            udpBridge = new UdpBridge(action);
            __0 = "127.0.0.1:" + udpBridge.Port.ToString(System.Globalization.CultureInfo.InvariantCulture);
        }
    }

    internal static void DisposeAll()
    {
        lock (Sync)
        {
            udpBridge?.Dispose(); udpBridge = null;
            foreach (TcpBridge bridge in Bridges.Values) bridge.Dispose();
            Bridges.Clear();
        }
    }

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

    [DataContract]
    internal sealed class TicketRequest { [DataMember(Name = "channel")] public string Channel { get; set; } }
    [DataContract]
    internal sealed class Ticket
    {
        [DataMember(Name = "ticket")] public string Value { get; set; }
        [DataMember(Name = "expires_at")] public long ExpiresAt { get; set; }
        [DataMember(Name = "channel")] public string Channel { get; set; }
        [DataMember(Name = "instance_id")] public string InstanceID { get; set; }
        [DataMember(Name = "tcp_host")] public string TCPHost { get; set; }
        [DataMember(Name = "tcp_port")] public int TCPPort { get; set; }
        [DataMember(Name = "udp_host")] public string UDPHost { get; set; }
        [DataMember(Name = "udp_port")] public int UDPPort { get; set; }
        [DataMember(Name = "tls_server_name")] public string TLSServerName { get; set; }
        [DataMember(Name = "development_plain")] public bool DevelopmentPlain { get; set; }
    }
    [DataContract]
    private sealed class Auth
    {
        [DataMember(Name = "ticket")] public string Ticket { get; set; }
        [DataMember(Name = "channel")] public string Channel { get; set; }
        [DataMember(Name = "instance_id")] public string InstanceID { get; set; }
    }
    [DataContract]
    private sealed class Ack
    {
        [DataMember(Name = "connection_id")] public string ConnectionID { get; set; }
        [DataMember(Name = "epoch")] public ulong Epoch { get; set; }
        [DataMember(Name = "udp_key")] public string UDPKey { get; set; }
    }

    private sealed class RemoteSession : IDisposable
    {
        internal readonly byte[] ID;
        internal readonly byte[] ClientKey;
        internal readonly byte[] ServerKey;
        internal readonly ulong Epoch;
        internal readonly IPEndPoint UDP;
        internal readonly CancellationTokenSource Lifetime = new();
        internal RemoteSession(Ack ack, Ticket ticket)
        {
            if (ack.ConnectionID == null || ack.ConnectionID.Length != 32 || ack.Epoch == 0) throw new InvalidDataException("Invalid realtime acknowledgment");
            ID = new byte[16];
            for (int i = 0; i < ID.Length; i++) ID[i] = Convert.ToByte(ack.ConnectionID.Substring(i * 2, 2), 16);
            Epoch = ack.Epoch;
            if (!string.IsNullOrEmpty(ack.UDPKey))
            {
                byte[] key = Convert.FromBase64String(ack.UDPKey);
                try
                {
                    if (key.Length != 32 || ticket.UDPPort <= 0 || ticket.UDPPort > 65535 || string.IsNullOrEmpty(ticket.UDPHost)) throw new InvalidDataException("Invalid UDP acknowledgment");
                    ClientKey = DirectionKey(key, "client"); ServerKey = DirectionKey(key, "server");
                    UDP = new IPEndPoint(Dns.GetHostAddresses(ticket.UDPHost)[0], ticket.UDPPort);
                }
                finally { Array.Clear(key, 0, key.Length); }
            }
        }
        public void Dispose()
        {
            Lifetime.Cancel();
        }
    }

    private sealed class TcpBridge : IDisposable
    {
        private readonly string channel;
        private readonly TcpListener listener;
        private readonly CancellationTokenSource lifetime = new();
        private readonly object connectionsLock = new();
        private readonly List<TcpClient> connections = [];
        private TaskCompletionSource<RemoteSession> ready = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int disposed;
        internal int Port => ((IPEndPoint)listener.LocalEndpoint).Port;
        internal Task<RemoteSession> Ready => Volatile.Read(ref ready).Task;

        internal TcpBridge(string channel)
        {
            this.channel = channel;
            listener = new TcpListener(IPAddress.Loopback, 0); listener.Start(4);
            _ = AcceptLoop();
        }
        private async Task AcceptLoop()
        {
            while (!lifetime.IsCancellationRequested)
            {
                TcpClient local;
                try { local = await listener.AcceptTcpClientAsync().ConfigureAwait(false); }
                catch (Exception) when (lifetime.IsCancellationRequested) { return; }
                lock (connectionsLock)
                {
                    if (connections.Count >= 4) { local.Dispose(); continue; }
                    if (connections.Count > 0)
                    {
                        ready.TrySetCanceled();
                        Volatile.Write(ref ready, new TaskCompletionSource<RemoteSession>(TaskCreationOptions.RunContinuationsAsynchronously));
                    }
                    foreach (TcpClient previous in connections) previous.Dispose();
                    connections.Add(local);
                }
                TaskCompletionSource<RemoteSession> completion = Volatile.Read(ref ready);
                if (completion.Task.IsCompleted)
                {
                    completion = new TaskCompletionSource<RemoteSession>(TaskCreationOptions.RunContinuationsAsynchronously);
                    Volatile.Write(ref ready, completion);
                }
                _ = Serve(local, completion);
            }
        }
        private async Task Serve(TcpClient local, TaskCompletionSource<RemoteSession> completion)
        {
            RemoteSession session = null;
            string phase = "ticket";
            try
            {
                using (local)
                using (var deadline = CancellationTokenSource.CreateLinkedTokenSource(lifetime.Token))
                using (var remote = new TcpClient())
                {
                    deadline.CancelAfter(TimeSpan.FromSeconds(15));
                    using (deadline.Token.Register(() => remote.Dispose()))
                    {
                        Ticket ticket = await SessionRecovery.RequestRealtimeTicket(channel, deadline.Token).ConfigureAwait(false);
                        if (ticket.Channel != channel || string.IsNullOrEmpty(ticket.Value) || ticket.ExpiresAt <= DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() ||
                            string.IsNullOrEmpty(ticket.TCPHost) || ticket.TCPPort <= 0 || ticket.TCPPort > 65535) throw new InvalidDataException("Invalid realtime ticket");
                        if (ticket.DevelopmentPlain && (!IPAddress.TryParse(ticket.TCPHost, out IPAddress ip) || !IPAddress.IsLoopback(ip))) throw new InvalidDataException("Plain realtime requires loopback");
                        phase = "connect";
                        Uri target = new UriBuilder("https", ticket.TCPHost, ticket.TCPPort).Uri;
                        NativeProxySettings proxy = NativeProxyPolicy.Resolve(target);
                        if (channel == "action") Volatile.Write(ref forceActionTCP, proxy.ProxyUrl == null ? 0 : 1);
                        if (proxy.ProxyUrl == null)
                            await remote.ConnectAsync(ticket.TCPHost, ticket.TCPPort).ConfigureAwait(false);
                        else
                        {
                            Uri proxyUri = new(proxy.ProxyUrl);
                            await remote.ConnectAsync(proxyUri.Host, proxyUri.Port).ConfigureAwait(false);
                            await OpenProxyTunnel(remote.GetStream(), target.Authority, deadline.Token).ConfigureAwait(false);
                        }
                        remote.NoDelay = true;
                        Stream upstream = remote.GetStream();
                        if (!ticket.DevelopmentPlain)
                        {
                            phase = "tls";
                            if (string.IsNullOrEmpty(ticket.TLSServerName)) throw new InvalidDataException("Realtime TLS server name is missing");
                            var tls = new SslStream(upstream, false);
                            upstream = tls;
                            await tls.AuthenticateAsClientAsync(ticket.TLSServerName, null, SslProtocols.Tls12, true).ConfigureAwait(false);
                        }
                        using (upstream)
                        {
                            phase = "authenticate";
                            byte[] auth = Serialize(new Auth { Ticket = ticket.Value, Channel = channel, InstanceID = ticket.InstanceID });
                            ticket.Value = null;
                            try { await WritePreface(upstream, "BD2RT001", auth, deadline.Token).ConfigureAwait(false); }
                            finally { Array.Clear(auth, 0, auth.Length); }
                            byte[] acknowledgment = await ReadPreface(upstream, "BD2RA001", deadline.Token).ConfigureAwait(false);
                            try
                            {
                                Ack ack = Deserialize<Ack>(acknowledgment);
                                if (proxy.ProxyUrl != null)
                                {
                                    ack.UDPKey = null;
                                    LoginRuntime.Log?.LogInfo("Native realtime uses the selected HTTP proxy; action traffic uses TCP. UDP requires direct routing or a TUN.");
                                }
                                if (channel == "action" && string.IsNullOrEmpty(ack.UDPKey)) Volatile.Write(ref forceActionTCP, 1);
                                session = new RemoteSession(ack, ticket);
                            }
                            finally { Array.Clear(acknowledgment, 0, acknowledgment.Length); }
                            completion.TrySetResult(session);
                            LoginRuntime.Log?.LogInfo("Native realtime authenticated: channel=" + channel);
                            phase = "relay";
                            deadline.CancelAfter(Timeout.InfiniteTimeSpan);
                            NetworkStream native = local.GetStream();
                            Task outbound = native.CopyToAsync(upstream, 32768, lifetime.Token);
                            Task inbound = upstream.CopyToAsync(native, 32768, lifetime.Token);
                            await Task.WhenAny(outbound, inbound).ConfigureAwait(false);
                            local.Dispose(); remote.Dispose();
                            try { await Task.WhenAll(outbound, inbound).ConfigureAwait(false); } catch (Exception) when (outbound.IsFaulted || inbound.IsFaulted || lifetime.IsCancellationRequested) { }
                        }
                    }
                }
            }
            catch (Exception ex)
            {
                completion.TrySetCanceled();
                LoginRuntime.Log?.LogWarning("Native realtime bridge failed: channel=" + channel + ", phase=" + phase + ", reason=" + ex.GetType().Name);
            }
            finally
            {
                session?.Dispose();
                Interlocked.CompareExchange(ref ready, new TaskCompletionSource<RemoteSession>(TaskCreationOptions.RunContinuationsAsynchronously), completion);
                lock (connectionsLock) connections.Remove(local);
                local.Dispose();
            }
        }
        public void Dispose()
        {
            if (Interlocked.Exchange(ref disposed, 1) != 0) return;
            lifetime.Cancel(); listener.Stop(); ready.TrySetCanceled();
            lock (connectionsLock) foreach (TcpClient client in connections) client.Dispose();
        }
    }

    private sealed class UdpBridge : IDisposable
    {
        private readonly UdpClient native = new(new IPEndPoint(IPAddress.Loopback, 0));
        private readonly UdpClient remote = new();
        private readonly CancellationTokenSource lifetime = new();
        private IPEndPoint nativeEndpoint;
        private RemoteSession active;
        private int disposed;
        internal int Port => ((IPEndPoint)native.Client.LocalEndPoint).Port;
        internal UdpBridge(TcpBridge action) { _ = Run(action); }
        private async Task Run(TcpBridge action)
        {
            try
            {
                RemoteSession session = await WaitSession(action).ConfigureAwait(false);
                Volatile.Write(ref active, session);
                using (lifetime.Token.Register(() => { native.Dispose(); remote.Dispose(); }))
                {
                    remote.Connect(session.UDP);
                    Task receive = Receive();
                    ulong sequence = 0;
                    while (!lifetime.IsCancellationRequested)
                    {
                        UdpReceiveResult packet = await native.ReceiveAsync().ConfigureAwait(false);
                        if (!IPAddress.IsLoopback(packet.RemoteEndPoint.Address) || packet.Buffer.Length < 20 || packet.Buffer.Length > 60000) continue;
                        if (session.Lifetime.IsCancellationRequested)
                        {
                            session = await WaitSession(action).ConfigureAwait(false);
                            remote.Connect(session.UDP);
                            Volatile.Write(ref active, session);
                            sequence = 0;
                        }
                        nativeEndpoint = packet.RemoteEndPoint;
                        if (++sequence == 0) throw new InvalidDataException("UDP sequence exhausted");
                        byte[] envelope = EncodeDatagram(session, sequence, packet.Buffer);
                        await remote.SendAsync(envelope, envelope.Length).ConfigureAwait(false);
                    }
                    await receive.ConfigureAwait(false);
                }
            }
            catch (Exception ex) { if (!lifetime.IsCancellationRequested) LoginRuntime.Log?.LogWarning("Native UDP bridge failed: " + ex.GetType().Name); }
            finally { Dispose(); }
        }
        private async Task<RemoteSession> WaitSession(TcpBridge action)
        {
            DateTime deadline = DateTime.UtcNow.AddSeconds(15);
            while (!lifetime.IsCancellationRequested && DateTime.UtcNow < deadline)
            {
                lock (Sync)
                {
                    if (Bridges.TryGetValue("action", out TcpBridge current)) action = current;
                }
                Task<RemoteSession> ready = action.Ready;
                Task completed = await Task.WhenAny(ready, Task.Delay(100, lifetime.Token)).ConfigureAwait(false);
                if (completed != ready || ready.IsCanceled || ready.IsFaulted) continue;
                RemoteSession session = await ready.ConfigureAwait(false);
                if (session.Lifetime.IsCancellationRequested) { await Task.Delay(50, lifetime.Token).ConfigureAwait(false); continue; }
                if (session.UDP == null) throw new InvalidDataException("UDP transport is unavailable");
                return session;
            }
            throw new IOException("Action authentication deadline exceeded");
        }
        private async Task Receive()
        {
            ulong highest = 0, bitmap = 0;
            RemoteSession previous = null;
            while (!lifetime.IsCancellationRequested)
            {
                UdpReceiveResult received = await remote.ReceiveAsync().ConfigureAwait(false);
                RemoteSession session = Volatile.Read(ref active);
                if (session == null || session.Lifetime.IsCancellationRequested) continue;
                if (!ReferenceEquals(previous, session)) { highest = 0; bitmap = 0; previous = session; }
                byte[] b = received.Buffer;
                if (b.Length < 76 || b.Length > 60076 || Encoding.ASCII.GetString(b, 0, 8) != "BD2UDP01" ||
                    !Equal(b, 8, session.ID) || ReadUInt64(b, 24) != session.Epoch || ReadUInt32(b, 40) != b.Length - 76) continue;
                using var mac = new HMACSHA256(session.ServerKey);
                byte[] expected = mac.ComputeHash(b, 0, b.Length - 32);
                if (!Equal(b, b.Length - 32, expected)) continue;
                ulong sequence = ReadUInt64(b, 32);
                if (sequence == 0) continue;
                if (sequence > highest) { ulong shift = sequence - highest; bitmap = shift >= 64 ? 0 : bitmap << (int)shift; bitmap |= 1; highest = sequence; }
                else { ulong shift = highest - sequence; if (shift >= 64 || (bitmap & (1UL << (int)shift)) != 0) continue; bitmap |= 1UL << (int)shift; }
                IPEndPoint endpoint = nativeEndpoint;
                if (endpoint == null) continue;
                byte[] payload = new byte[b.Length - 76]; Buffer.BlockCopy(b, 44, payload, 0, payload.Length);
                await native.SendAsync(payload, payload.Length, endpoint).ConfigureAwait(false);
            }
        }
        public void Dispose() { if (Interlocked.Exchange(ref disposed, 1) != 0) return; lifetime.Cancel(); native.Dispose(); remote.Dispose(); }
    }

    private static byte[] DirectionKey(byte[] master, string direction)
    { using var mac = new HMACSHA256(master); return mac.ComputeHash(Encoding.ASCII.GetBytes("bd2-realtime-udp:" + direction)); }
    private static byte[] EncodeDatagram(RemoteSession s, ulong sequence, byte[] payload)
    {
        byte[] b = new byte[76 + payload.Length]; Encoding.ASCII.GetBytes("BD2UDP01").CopyTo(b, 0); s.ID.CopyTo(b, 8);
        WriteUInt64(b, 24, s.Epoch); WriteUInt64(b, 32, sequence); WriteUInt32(b, 40, (uint)payload.Length); payload.CopyTo(b, 44);
        using var mac = new HMACSHA256(s.ClientKey); mac.ComputeHash(b, 0, b.Length - 32).CopyTo(b, b.Length - 32); return b;
    }
    private static bool Equal(byte[] value, int offset, byte[] expected)
    { if (offset < 0 || value.Length - offset < expected.Length) return false; int difference = 0; for (int i = 0; i < expected.Length; i++) difference |= value[offset + i] ^ expected[i]; return difference == 0; }
    private static ulong ReadUInt64(byte[] b, int offset) { ulong n = 0; for (int i = 7; i >= 0; i--) n = (n << 8) | b[offset + i]; return n; }
    private static uint ReadUInt32(byte[] b, int offset) => (uint)ReadUInt64Padded(b, offset, 4);
    private static ulong ReadUInt64Padded(byte[] b, int offset, int count) { ulong n = 0; for (int i = count - 1; i >= 0; i--) n = (n << 8) | b[offset + i]; return n; }
    private static void WriteUInt64(byte[] b, int offset, ulong n) { for (int i = 0; i < 8; i++) { b[offset + i] = (byte)n; n >>= 8; } }
    private static void WriteUInt32(byte[] b, int offset, uint n) { for (int i = 0; i < 4; i++) { b[offset + i] = (byte)n; n >>= 8; } }
    private static async Task OpenProxyTunnel(Stream stream, string authority, CancellationToken cancellation)
    {
        byte[] request = Encoding.ASCII.GetBytes("CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\n\r\n");
        await stream.WriteAsync(request.AsMemory(), cancellation).ConfigureAwait(false);
        byte[] response = new byte[8192];
        int size = 0;
        while (size < response.Length)
        {
            int read = await stream.ReadAsync(response.AsMemory(size, 1), cancellation).ConfigureAwait(false);
            if (read == 0) throw new EndOfStreamException("Realtime proxy closed the tunnel");
            size++;
            if (size < 4 || response[size - 4] != 13 || response[size - 3] != 10 || response[size - 2] != 13 || response[size - 1] != 10) continue;
            string line = Encoding.ASCII.GetString(response, 0, size).Split('\r')[0];
            string[] parts = line.Split(' ');
            if (parts.Length < 2 || !parts[0].StartsWith("HTTP/1.", StringComparison.Ordinal) || parts[1] != "200") throw new IOException("Realtime proxy refused CONNECT");
            return;
        }
        throw new InvalidDataException("Realtime proxy response headers exceeded limit");
    }
    private static async Task WritePreface(Stream stream, string magic, byte[] payload, CancellationToken cancellation)
    {
        if (payload.Length > 2048) throw new InvalidDataException("Realtime authentication limit");
        byte[] prefix = new byte[10]; Encoding.ASCII.GetBytes(magic).CopyTo(prefix, 0); prefix[8] = (byte)payload.Length; prefix[9] = (byte)(payload.Length >> 8);
        await stream.WriteAsync(prefix.AsMemory(), cancellation).ConfigureAwait(false);
        await stream.WriteAsync(payload.AsMemory(), cancellation).ConfigureAwait(false);
    }
    private static async Task<byte[]> ReadPreface(Stream stream, string magic, CancellationToken cancellation)
    {
        byte[] header = new byte[10]; await ReadExact(stream, header, cancellation).ConfigureAwait(false);
        int size = header[8] | (header[9] << 8);
        if (Encoding.ASCII.GetString(header, 0, 8) != magic || size <= 0 || size > 2048) throw new InvalidDataException("Invalid realtime acknowledgment");
        byte[] payload = new byte[size]; await ReadExact(stream, payload, cancellation).ConfigureAwait(false); return payload;
    }
    private static async Task ReadExact(Stream stream, byte[] b, CancellationToken cancellation)
    {
        for (int offset = 0; offset < b.Length;) { int read = await stream.ReadAsync(b.AsMemory(offset), cancellation).ConfigureAwait(false); if (read <= 0) throw new EndOfStreamException(); offset += read; }
    }
}
