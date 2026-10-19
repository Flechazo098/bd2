using System;
using System.Buffers;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Threading;

namespace Bd2BrownDustXAccel;

/// <summary>RGBA32 pixels laid out the way Texture2D expects: bottom row first, mip levels back to back.</summary>
internal sealed class DecodedImage(int width, int height, int mipCount, byte[] pixels, int length)
{
    internal int Width { get; } = width;
    internal int Height { get; } = height;
    internal int MipCount { get; } = mipCount;
    internal byte[] Pixels { get; } = pixels;
    internal int Length { get; } = length;

    internal int MipOffset(int level) => MipOffset(Width, Height, level);

    internal static int MipOffset(int width, int height, int level)
    {
        int offset = 0;
        for (int i = 0; i < level; i++)
        {
            offset += MipWidth(width, i) * MipWidth(height, i) * 4;
        }
        return offset;
    }

    internal static int MipWidth(int size, int level) => Math.Max(1, size >> level);
}

/// <summary>Pooled scratch buffers; the pool is replaced after each load burst so idle memory is returned to the GC.</summary>
internal static class BufferPool
{
    private const int MaxPooledLength = 1 << 28;
    private static ArrayPool<byte> pool = Create();

    internal static byte[] Rent(int length) => length > MaxPooledLength ? new byte[length] : Volatile.Read(ref pool).Rent(length);

    internal static void Return(byte[] buffer)
    {
        if (buffer != null && buffer.Length <= MaxPooledLength)
        {
            Volatile.Read(ref pool).Return(buffer);
        }
    }

    internal static void Trim() => Volatile.Write(ref pool, Create());

    private static ArrayPool<byte> Create() => ArrayPool<byte>.Create(MaxPooledLength, DecodeScheduler.WorkerCount + 2);
}

/// <summary>Thread-safe PNG decoder for the formats Spine exports; anything else is left to ImageConversion.LoadImage.</summary>
internal static unsafe class PngDecoder
{
    private const uint ChunkIhdr = 0x49484452;
    private const uint ChunkPlte = 0x504C5445;
    private const uint ChunkTrns = 0x74524E53;
    private const uint ChunkIdat = 0x49444154;
    private const uint ChunkIend = 0x49454E44;
    private static readonly byte[] Signature = [137, 80, 78, 71, 13, 10, 26, 10];
    private static volatile bool unavailable;

    /// <summary>Bytes the decoded image will occupy, read from IHDR; falls back to the file size when the header is unusable.</summary>
    internal static long EstimateBytes(byte[] file, int length, bool mipmaps)
    {
        if (length < 33 || !HasSignature(file) || ReadUInt(file, 12) != ChunkIhdr)
        {
            return length;
        }
        long pixels = (long)ReadUInt(file, 16) * ReadUInt(file, 20) * 4;
        return mipmaps ? pixels * 4 / 3 : pixels;
    }

    internal static bool TryDecode(byte[] file, int length, bool mipmaps, out DecodedImage image)
    {
        image = null;
        if (unavailable || length < 33 || !HasSignature(file))
        {
            return false;
        }
        byte[] pixels = null;
        try
        {
            image = Decode(file, length, mipmaps, out pixels);
            return image != null;
        }
        catch (Exception error) when (error is DllNotFoundException or EntryPointNotFoundException or TypeInitializationException)
        {
            unavailable = true;
            Plugin.Log.LogWarning("PNG decoder unavailable, using Unity decoding: " + error.Message);
        }
        catch (Exception error) when (error is not OutOfMemoryException and not ThreadAbortException)
        {
            // Corrupt or unusual data: ImageConversion.LoadImage handles it exactly as BrownDustX would.
        }
        BufferPool.Return(pixels);
        image = null;
        return false;
    }

    private static DecodedImage Decode(byte[] file, int length, bool mipmaps, out byte[] pixels)
    {
        pixels = null;
        int width = 0, height = 0, bitDepth = 0, colorType = 0, interlace = 0;
        byte[] palette = null;
        byte[] paletteAlpha = null;
        bool colorKey = false;
        var idat = new List<(int Offset, int Length)>();
        int position = 8;
        while (position + 8 <= length)
        {
            int chunkLength = (int)ReadUInt(file, position);
            uint type = ReadUInt(file, position + 4);
            int body = position + 8;
            if (chunkLength < 0 || body + chunkLength > length)
            {
                return null;
            }
            switch (type)
            {
                case ChunkIhdr:
                    width = (int)ReadUInt(file, body);
                    height = (int)ReadUInt(file, body + 4);
                    bitDepth = file[body + 8];
                    colorType = file[body + 9];
                    if (file[body + 10] != 0 || file[body + 11] != 0)
                    {
                        return null;
                    }
                    interlace = file[body + 12];
                    break;
                case ChunkPlte:
                    palette = new byte[chunkLength];
                    Buffer.BlockCopy(file, body, palette, 0, chunkLength);
                    break;
                case ChunkTrns:
                    if (colorType == 3)
                    {
                        paletteAlpha = new byte[chunkLength];
                        Buffer.BlockCopy(file, body, paletteAlpha, 0, chunkLength);
                    }
                    else
                    {
                        colorKey = true;
                    }
                    break;
                case ChunkIdat:
                    idat.Add((body, chunkLength));
                    break;
                default:
                    break;
            }
            if (type == ChunkIend)
            {
                break;
            }
            position = body + chunkLength + 4;
        }
        int channels = colorType switch { 0 => 1, 2 => 3, 3 => 1, 4 => 2, 6 => 4, _ => 0 };
        bool supportedDepth = colorType == 3 ? bitDepth is 1 or 2 or 4 or 8 : bitDepth == 8;
        if (width <= 0 || height <= 0 || interlace != 0 || idat.Count == 0 || channels == 0 || !supportedDepth || colorKey
            || (colorType == 3 && palette == null) || width > 16384 || height > 16384)
        {
            return null;
        }
        int mipCount = mipmaps ? MipCount(width, height) : 1;
        long total = 0;
        for (int level = 0; level < mipCount; level++)
        {
            total += (long)DecodedImage.MipWidth(width, level) * DecodedImage.MipWidth(height, level) * 4;
        }
        if (total > int.MaxValue)
        {
            return null;
        }
        int bitsPerPixel = channels * bitDepth;
        int filterStride = Math.Max(1, bitsPerPixel / 8);
        int rowBytes = (int)(((long)width * bitsPerPixel + 7) / 8);
        pixels = BufferPool.Rent((int)total);
        byte[] current = BufferPool.Rent(rowBytes);
        byte[] previous = BufferPool.Rent(rowBytes);
        try
        {
            Array.Clear(previous, 0, rowBytes);
            using var inflater = new DeflateStream(new IdatStream(file, idat), CompressionMode.Decompress);
            byte[] filter = new byte[1];
            int destinationStride = width * 4;
            fixed (byte* output = pixels)
            {
                for (int y = 0; y < height; y++)
                {
                    ReadExactly(inflater, filter, 1);
                    ReadExactly(inflater, current, rowBytes);
                    fixed (byte* cur = current, prev = previous)
                    {
                        Unfilter(cur, prev, rowBytes, filterStride, filter[0]);
                        byte* row = output + (long)(height - 1 - y) * destinationStride;
                        ConvertRow(cur, row, width, colorType, bitDepth, palette, paletteAlpha);
                    }
                    (current, previous) = (previous, current);
                }
                for (int level = 1; level < mipCount; level++)
                {
                    int sourceWidth = DecodedImage.MipWidth(width, level - 1), sourceHeight = DecodedImage.MipWidth(height, level - 1);
                    Downsample(output + DecodedImage.MipOffset(width, height, level - 1), sourceWidth, sourceHeight,
                        output + DecodedImage.MipOffset(width, height, level), DecodedImage.MipWidth(width, level), DecodedImage.MipWidth(height, level));
                }
            }
        }
        finally
        {
            BufferPool.Return(current);
            BufferPool.Return(previous);
        }
        return new DecodedImage(width, height, mipCount, pixels, (int)total);
    }

    /// <summary>Matches Unity's full mip chain length for a texture of this size.</summary>
    internal static int MipCount(int width, int height)
    {
        int count = 1;
        for (int size = Math.Max(width, height); size > 1; size >>= 1)
        {
            count++;
        }
        return count;
    }

    private static void Unfilter(byte* cur, byte* prev, int length, int stride, int filter)
    {
        switch (filter)
        {
            case 0:
                return;
            case 1:
                for (int i = stride; i < length; i++)
                {
                    cur[i] = (byte)(cur[i] + cur[i - stride]);
                }
                return;
            case 2:
                for (int i = 0; i < length; i++)
                {
                    cur[i] = (byte)(cur[i] + prev[i]);
                }
                return;
            case 3:
                for (int i = 0; i < stride; i++)
                {
                    cur[i] = (byte)(cur[i] + (prev[i] >> 1));
                }
                for (int i = stride; i < length; i++)
                {
                    cur[i] = (byte)(cur[i] + ((cur[i - stride] + prev[i]) >> 1));
                }
                return;
            case 4:
                for (int i = 0; i < stride; i++)
                {
                    cur[i] = (byte)(cur[i] + prev[i]);
                }
                for (int i = stride; i < length; i++)
                {
                    int a = cur[i - stride], b = prev[i], c = prev[i - stride];
                    int p = b - c, q = a - c;
                    int pa = p < 0 ? -p : p, pb = q < 0 ? -q : q, pc = p + q < 0 ? -(p + q) : p + q;
                    cur[i] = (byte)(cur[i] + (pa <= pb && pa <= pc ? a : pb <= pc ? b : c));
                }
                return;
            default:
                throw new InvalidDataException("Unknown PNG filter");
        }
    }

    private static void ConvertRow(byte* source, byte* destination, int width, int colorType, int bitDepth, byte[] palette, byte[] paletteAlpha)
    {
        switch (colorType)
        {
            case 6:
                Buffer.MemoryCopy(source, destination, width * 4L, width * 4L);
                return;
            case 2:
                for (int x = 0; x < width; x++, source += 3, destination += 4)
                {
                    destination[0] = source[0];
                    destination[1] = source[1];
                    destination[2] = source[2];
                    destination[3] = 255;
                }
                return;
            case 4:
                for (int x = 0; x < width; x++, source += 2, destination += 4)
                {
                    destination[0] = destination[1] = destination[2] = source[0];
                    destination[3] = source[1];
                }
                return;
            case 0:
                for (int x = 0; x < width; x++, destination += 4)
                {
                    destination[0] = destination[1] = destination[2] = source[x];
                    destination[3] = 255;
                }
                return;
            default:
                int perByte = 8 / bitDepth, mask = (1 << bitDepth) - 1, colors = palette.Length / 3;
                for (int x = 0; x < width; x++, destination += 4)
                {
                    int index = bitDepth == 8 ? source[x] : (source[x / perByte] >> ((perByte - 1 - (x % perByte)) * bitDepth)) & mask;
                    if (index >= colors)
                    {
                        throw new InvalidDataException("PNG palette index out of range");
                    }
                    destination[0] = palette[index * 3];
                    destination[1] = palette[(index * 3) + 1];
                    destination[2] = palette[(index * 3) + 2];
                    destination[3] = paletteAlpha != null && index < paletteAlpha.Length ? paletteAlpha[index] : (byte)255;
                }
                return;
        }
    }

    private static void Downsample(byte* source, int sourceWidth, int sourceHeight, byte* destination, int width, int height)
    {
        for (int y = 0; y < height; y++)
        {
            byte* row0 = source + ((long)Math.Min(2 * y, sourceHeight - 1) * sourceWidth * 4);
            byte* row1 = source + ((long)Math.Min((2 * y) + 1, sourceHeight - 1) * sourceWidth * 4);
            for (int x = 0; x < width; x++, destination += 4)
            {
                int x0 = Math.Min(2 * x, sourceWidth - 1) * 4, x1 = Math.Min((2 * x) + 1, sourceWidth - 1) * 4;
                for (int c = 0; c < 4; c++)
                {
                    destination[c] = (byte)((row0[x0 + c] + row0[x1 + c] + row1[x0 + c] + row1[x1 + c] + 2) >> 2);
                }
            }
        }
    }

    private static void ReadExactly(DeflateStream stream, byte[] buffer, int count)
    {
        for (int read = 0; read < count;)
        {
            int n = stream.Read(buffer, read, count - read);
            if (n <= 0)
            {
                throw new EndOfStreamException();
            }
            read += n;
        }
    }

    private static bool HasSignature(byte[] file)
    {
        for (int i = 0; i < Signature.Length; i++)
        {
            if (file[i] != Signature[i])
            {
                return false;
            }
        }
        return true;
    }

    private static uint ReadUInt(byte[] data, int offset) =>
        ((uint)data[offset] << 24) | ((uint)data[offset + 1] << 16) | ((uint)data[offset + 2] << 8) | data[offset + 3];

    /// <summary>Concatenated IDAT payload without the two-byte zlib header, fed to DeflateStream.</summary>
    private sealed class IdatStream : Stream
    {
        private readonly byte[] file;
        private readonly List<(int Offset, int Length)> segments;
        private int segment;
        private int offset;

        internal IdatStream(byte[] file, List<(int Offset, int Length)> segments)
        {
            this.file = file;
            this.segments = segments;
            (int start, int length) = segments[0];
            if (length < 2 || (file[start] & 0x0F) != 8 || (file[start + 1] & 0x20) != 0 || ((file[start] << 8) | file[start + 1]) % 31 != 0)
            {
                throw new InvalidDataException("Unsupported zlib header");
            }
            offset = 2;
        }

        public override bool CanRead => true;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => throw new NotSupportedException();
        public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }

        public override int Read(byte[] buffer, int bufferOffset, int count)
        {
            int written = 0;
            while (written < count && segment < segments.Count)
            {
                (int start, int length) = segments[segment];
                int available = length - offset;
                if (available <= 0)
                {
                    segment++;
                    offset = 0;
                    continue;
                }
                int n = Math.Min(available, count - written);
                Buffer.BlockCopy(file, start + offset, buffer, bufferOffset + written, n);
                offset += n;
                written += n;
            }
            return written;
        }

        public override void Flush()
        {
        }

        public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
        public override void SetLength(long value) => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    }
}
