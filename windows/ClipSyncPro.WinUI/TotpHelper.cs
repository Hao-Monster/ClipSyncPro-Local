using System;
using System.Security.Cryptography;
using System.Text.RegularExpressions;

namespace ClipSyncPro_WinUI;

public static class TotpHelper
{
    private const string Base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

    /// <summary>
    /// 根据 Base32 密钥即时计算当前 6 位 TOTP 动态码和剩余秒数（平时不后台轮询，仅在查看时调用）。
    /// </summary>
    public static bool TryGenerateTotp(string? secretBase32, out string code, out int remainingSeconds, out string errorMessage)
    {
        code = "------";
        remainingSeconds = 0;
        errorMessage = "";

        if (string.IsNullOrWhiteSpace(secretBase32))
        {
            errorMessage = "密钥为空";
            return false;
        }

        // 清洗输入：移除空格、制表符与填充等
        string clean = Regex.Replace(secretBase32.Trim().ToUpperInvariant(), @"[\s=]+", "");
        if (string.IsNullOrEmpty(clean))
        {
            errorMessage = "密钥为空";
            return false;
        }

        byte[] keyBytes;
        try
        {
            keyBytes = Base32ToBytes(clean);
        }
        catch
        {
            errorMessage = "无效的 Base32 密钥格式";
            return false;
        }

        try
        {
            long epoch = DateTimeOffset.UtcNow.ToUnixTimeSeconds();
            long timeStep = epoch / 30;
            remainingSeconds = 30 - (int)(epoch % 30);

            // 将 timeStep 转为大端序 8 字节 (64-bit big-endian)
            byte[] timeBuffer = BitConverter.GetBytes(timeStep);
            if (BitConverter.IsLittleEndian)
            {
                Array.Reverse(timeBuffer);
            }

            using var hmac = new HMACSHA1(keyBytes);
            byte[] hash = hmac.ComputeHash(timeBuffer);

            // 动态截取 (Truncate)
            int offset = hash[^1] & 0x0f;
            int binary = ((hash[offset] & 0x7f) << 24)
                       | ((hash[offset + 1] & 0xff) << 16)
                       | ((hash[offset + 2] & 0xff) << 8)
                       | (hash[offset + 3] & 0xff);

            int otp = binary % 1000000;
            code = otp.ToString("D6");
            return true;
        }
        catch (Exception ex)
        {
            errorMessage = ex.Message;
            return false;
        }
    }

    private static byte[] Base32ToBytes(string base32)
    {
        var bytes = new System.Collections.Generic.List<byte>();
        int bits = 0;
        int value = 0;

        foreach (char c in base32)
        {
            int index = Base32Alphabet.IndexOf(c);
            if (index == -1)
            {
                throw new FormatException("包含无效的 Base32 字符");
            }

            value = (value << 5) | index;
            bits += 5;

            if (bits >= 8)
            {
                bytes.Add((byte)((value >> (bits - 8)) & 0xff));
                bits -= 8;
            }
        }

        return bytes.ToArray();
    }
}
