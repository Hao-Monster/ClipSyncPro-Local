using System.ComponentModel;
using System.Diagnostics;
using System.Text.RegularExpressions;

namespace ClipSyncPro_WinUI;

public readonly record struct LocalMySqlHealResult(string ServiceName, string State, bool StartedOrRunning);

public static class AgentLauncher
{
    private static readonly HttpClient ProbeClient = new() { Timeout = TimeSpan.FromSeconds(1.5) };
    private static readonly SemaphoreSlim StartGate = new(1, 1);
    private static readonly Regex SafeServiceNameRegex = new(@"^[A-Za-z0-9_.-]+$", RegexOptions.Compiled);
    private static readonly Regex ScStateRegex = new(@"^\s*STATE\s*:\s*([1-7])\b", RegexOptions.Multiline | RegexOptions.Compiled);
    private static Process? _ownedProcess;
    private static volatile bool _wasUnreachable;

    private static Uri GetStatusUri() => new(AgentClient.ResolveBaseUrl().TrimEnd('/') + "/api/status");

    public static event Action? AgentRecovered;

    public static string? LastError { get; private set; }

    public static async Task EnsureRunningAsync(CancellationToken cancellationToken)
    {
        var notifyRecovered = false;
        await StartGate.WaitAsync(cancellationToken);
        try
        {
            if (await IsAgentReadyAsync(cancellationToken))
            {
                LastError = null;
                if (_wasUnreachable)
                {
                    _wasUnreachable = false;
                    notifyRecovered = true;
                }
                return;
            }

            _wasUnreachable = true;
            var agentPath = FindAgentPath();
            if (agentPath is null)
            {
                LastError = "找不到本地 agent 文件。";
                return;
            }

            if (!IsProcessRunning(_ownedProcess))
            {
                if (_ownedProcess is not null)
                {
                    try
                    {
                        _ownedProcess.Dispose();
                    }
                    catch
                    {
                        // Ignore dispose errors on exited process handle.
                    }
                    _ownedProcess = null;
                }
                _ownedProcess = Process.Start(new ProcessStartInfo
                {
                    FileName = agentPath,
                    WorkingDirectory = Path.GetDirectoryName(agentPath) ?? AppContext.BaseDirectory,
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    WindowStyle = ProcessWindowStyle.Hidden,
                });
            }

            var deadline = DateTimeOffset.UtcNow.AddSeconds(8);
            while (DateTimeOffset.UtcNow < deadline && !cancellationToken.IsCancellationRequested)
            {
                if (await IsAgentReadyAsync(cancellationToken))
                {
                    LastError = null;
                    _wasUnreachable = false;
                    notifyRecovered = true;
                    return;
                }
                await Task.Delay(350, cancellationToken);
            }
            LastError = "本地 agent 启动超时，系统将自动重试。";
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            throw;
        }
        catch (Exception ex)
        {
            _wasUnreachable = true;
            LastError = $"本地 agent 启动失败：{ex.Message}";
        }
        finally
        {
            StartGate.Release();
            if (notifyRecovered)
            {
                try
                {
                    AgentRecovered?.Invoke();
                }
                catch
                {
                    // Ignore subscriber exceptions so supervision loop never aborts.
                }
            }
        }
    }

    public static async Task WatchAsync(CancellationToken cancellationToken)
    {
        var retryDelay = TimeSpan.FromSeconds(2);
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                await EnsureRunningAsync(cancellationToken);
                retryDelay = TimeSpan.FromSeconds(2);
                await Task.Delay(TimeSpan.FromSeconds(3), cancellationToken);
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                return;
            }
            catch
            {
                await Task.Delay(retryDelay, cancellationToken);
                retryDelay = retryDelay < TimeSpan.FromSeconds(30)
                    ? retryDelay + retryDelay
                    : TimeSpan.FromSeconds(30);
            }
        }
    }

    public static async Task<LocalMySqlHealResult> TryStartLocalMySqlServiceAsync(string? preferredServiceName, bool allowElevationPrompt, CancellationToken cancellationToken)
    {
        var candidates = new List<string>();
        void AddCandidate(string? raw)
        {
            var trimmed = raw?.Trim();
            if (!string.IsNullOrEmpty(trimmed) && SafeServiceNameRegex.IsMatch(trimmed) && !candidates.Contains(trimmed, StringComparer.OrdinalIgnoreCase))
            {
                candidates.Add(trimmed);
            }
        }

        AddCandidate(preferredServiceName);
        AddCandidate(Environment.GetEnvironmentVariable("CLIPSYNC_MYSQL_SERVICE"));
        if (candidates.Count == 0)
        {
            return new LocalMySqlHealResult("", "", StartedOrRunning: false);
        }

        foreach (var svc in candidates)
        {
            var (queryOutput, queryExit) = await RunScProcessAsync($"query {svc}", TimeSpan.FromSeconds(2), cancellationToken);
            if (queryExit == 1060 || string.IsNullOrWhiteSpace(queryOutput))
            {
                continue;
            }
            var match = ScStateRegex.Match(queryOutput);
            if (!match.Success)
            {
                continue;
            }

            var code = match.Groups[1].Value;
            if (code is "4" or "2")
            {
                return new LocalMySqlHealResult(svc, "running", StartedOrRunning: true);
            }
            if (code is not ("1" or "3"))
            {
                return new LocalMySqlHealResult(svc, "paused", StartedOrRunning: false);
            }

            var (_, startExit) = await RunScProcessAsync($"start {svc}", TimeSpan.FromSeconds(4), cancellationToken);
            if (startExit is 0 or 1056)
            {
                return new LocalMySqlHealResult(svc, "starting", StartedOrRunning: true);
            }

            if (!allowElevationPrompt)
            {
                return new LocalMySqlHealResult(svc, startExit == 5 ? "access_denied" : "stopped", StartedOrRunning: false);
            }

            try
            {
                using var elevated = Process.Start(new ProcessStartInfo
                {
                    FileName = "sc.exe",
                    Arguments = $"start {svc}",
                    Verb = "runas",
                    UseShellExecute = true,
                    WindowStyle = ProcessWindowStyle.Hidden,
                });
                if (elevated is not null)
                {
                    using var waitCts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
                    waitCts.CancelAfter(TimeSpan.FromSeconds(15));
                    await elevated.WaitForExitAsync(waitCts.Token);
                    if (elevated.ExitCode is 0 or 1056)
                    {
                        return new LocalMySqlHealResult(svc, "starting", StartedOrRunning: true);
                    }
                }
                return new LocalMySqlHealResult(svc, "stopped", StartedOrRunning: false);
            }
            catch (Win32Exception ex) when (ex.NativeErrorCode == 1223)
            {
                return new LocalMySqlHealResult(svc, "uac_cancelled", StartedOrRunning: false);
            }
            catch
            {
                return new LocalMySqlHealResult(svc, "access_denied", StartedOrRunning: false);
            }
        }

        return new LocalMySqlHealResult("", "", StartedOrRunning: false);
    }

    private static async Task<(string Output, int ExitCode)> RunScProcessAsync(string arguments, TimeSpan timeout, CancellationToken cancellationToken)
    {
        try
        {
            using var process = Process.Start(new ProcessStartInfo
            {
                FileName = "sc.exe",
                Arguments = arguments,
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
            });
            if (process is null)
            {
                return ("", -1);
            }

            using var cts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
            cts.CancelAfter(timeout);
            var stdoutTask = process.StandardOutput.ReadToEndAsync(cts.Token);
            await process.WaitForExitAsync(cts.Token);
            var stdout = await stdoutTask;
            return (stdout, process.ExitCode);
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            throw;
        }
        catch
        {
            return ("", -1);
        }
    }

    private static async Task<bool> IsAgentReadyAsync(CancellationToken cancellationToken)
    {
        try
        {
            using var response = await ProbeClient.GetAsync(GetStatusUri(), cancellationToken);
            return response.IsSuccessStatusCode;
        }
        catch
        {
            return false;
        }
    }

    private static string? FindAgentPath()
    {
        var names = new[]
        {
            Path.Combine(AppContext.BaseDirectory, "ClipSyncPro-Agent.exe"),
            Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "..", "..", "..", "..", "..", "..", "dist", "ClipSyncPro-Agent.exe")),
            Path.GetFullPath(Path.Combine(Environment.CurrentDirectory, "dist", "ClipSyncPro-Agent.exe")),
        };
        return names.FirstOrDefault(File.Exists);
    }

    private static bool IsProcessRunning(Process? process)
    {
        try
        {
            return process is not null && !process.HasExited;
        }
        catch
        {
            return false;
        }
    }
}
