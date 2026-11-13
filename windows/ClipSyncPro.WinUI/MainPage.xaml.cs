using System.Collections.Concurrent;
using System.Collections.ObjectModel;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.RegularExpressions;
using Microsoft.UI.Input;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media.Imaging;
using Microsoft.UI.Xaml.Media;
using Windows.ApplicationModel.DataTransfer;
using Windows.System;
using Windows.UI.Core;
using DispatcherQueueTimer = Microsoft.UI.Dispatching.DispatcherQueueTimer;

namespace ClipSyncPro_WinUI;

public enum MainViewMode
{
    All,
    Favorites,
    Prompts,
    Sensitive
}

public sealed partial class MainPage : Page
{
    private readonly AgentClient _client = new();
    private readonly IncrementalRecordCollection _records;
    private readonly ObservableCollection<PromptView> _prompts = [];
    private readonly SemaphoreSlim _loadGate = new(1, 1);
    private readonly SemaphoreSlim _actionGate = new(1, 1);
    private CancellationTokenSource? _lifetime;
    private DispatcherQueueTimer? _eventRefreshTimer;
    private ClipboardRecordView? _selected;
    private PromptView? _selectedPrompt;

    private MainViewMode _currentView = MainViewMode.All;
    private string _date = "";
    private bool _sensitiveOnly;
    private bool _favoriteOnly;
    private string _promptCategory = "";
    private DispatcherQueueTimer? _searchDebounceTimer;
    private DispatcherQueueTimer? _recoveryRetryTimer;
    private AgentStatus? _lastKnownStatus;
    private string _lastExecutedKeyword = "";
    private bool _hasPendingRecordReload;
    private bool _hasPendingPromptReload;
    private volatile bool _isUnhealthy;
    private int _recoveryAttempt;
    private bool _uacPromptedForCurrentOutage;
    private bool _forceAllowUacOnNextFailure;
    private int _mysqlHealGate;

    private readonly ConcurrentDictionary<string, DateTime> _suppressedEventRecordIds = new();

    private void SuppressRecordRefresh(string recordId)
    {
        _suppressedEventRecordIds[recordId] = DateTime.UtcNow.AddSeconds(3);
    }

    public MainPage()
    {
        _records = new IncrementalRecordCollection(FetchRecordsPageAsync)
        {
            LoadFailed = HandleLoadException
        };
        InitializeComponent();
        RecordsList.ItemsSource = _records;
        PromptsList.ItemsSource = _prompts;
    }

    private async void Page_Loaded(object sender, RoutedEventArgs e)
    {
        _lifetime = new CancellationTokenSource();
        if (_eventRefreshTimer is null)
        {
            _eventRefreshTimer = DispatcherQueue.CreateTimer();
            _eventRefreshTimer.Interval = TimeSpan.FromMilliseconds(250);
            _eventRefreshTimer.IsRepeating = false;
            _eventRefreshTimer.Tick += EventRefreshTimer_Tick;
        }
        if (_searchDebounceTimer is null)
        {
            _searchDebounceTimer = DispatcherQueue.CreateTimer();
            _searchDebounceTimer.Interval = TimeSpan.FromMilliseconds(280);
            _searchDebounceTimer.IsRepeating = false;
            _searchDebounceTimer.Tick += SearchDebounceTimer_Tick;
        }
        if (_recoveryRetryTimer is null)
        {
            _recoveryRetryTimer = DispatcherQueue.CreateTimer();
            _recoveryRetryTimer.IsRepeating = false;
            _recoveryRetryTimer.Tick += RecoveryRetryTimer_Tick;
        }
        AgentLauncher.AgentRecovered -= OnAgentRecovered;
        AgentLauncher.AgentRecovered += OnAgentRecovered;
        if (Application.Current is App app && app.MainWindow is MainWindow mw)
        {
            mw.Activated -= MainWindow_Activated;
            mw.Activated += MainWindow_Activated;
        }
        await AgentLauncher.EnsureRunningAsync(_lifetime.Token);
        _ = Task.Run(() => AgentLauncher.WatchAsync(_lifetime.Token));
        _ = Task.Run(() => WatchAgentEventsAsync(_lifetime.Token));
        await LoadCurrentViewDataAsync();
    }

    private void Page_Unloaded(object sender, RoutedEventArgs e)
    {
        AgentLauncher.AgentRecovered -= OnAgentRecovered;
        if (Application.Current is App app && app.MainWindow is MainWindow mw)
        {
            mw.Activated -= MainWindow_Activated;
        }
        _recoveryRetryTimer?.Stop();
        _searchDebounceTimer?.Stop();
        _eventRefreshTimer?.Stop();
        _lifetime?.Cancel();
        _lifetime?.Dispose();
        _lifetime = null;
    }

    private void MainWindow_Activated(object sender, Microsoft.UI.Xaml.WindowActivatedEventArgs args)
    {
        if (args.WindowActivationState != Microsoft.UI.Xaml.WindowActivationState.Deactivated && _isUnhealthy)
        {
            QueueEventRefresh();
        }
    }

    private void OnAgentRecovered()
    {
        QueueEventRefresh();
    }

    private async void RecoveryRetryTimer_Tick(DispatcherQueueTimer sender, object args)
    {
        await LoadCurrentViewDataAsync();
    }

    private void MarkHealthy()
    {
        _isUnhealthy = false;
        _recoveryAttempt = 0;
        _uacPromptedForCurrentOutage = false;
        _forceAllowUacOnNextFailure = false;
        _recoveryRetryTimer?.Stop();
    }

    private void ScheduleRecoveryRetry(TimeSpan? customDelay = null)
    {
        _isUnhealthy = true;
        if (_recoveryRetryTimer is null || _lifetime?.IsCancellationRequested != false)
        {
            return;
        }
        TimeSpan delay;
        if (customDelay.HasValue)
        {
            delay = customDelay.Value;
        }
        else
        {
            var seconds = _recoveryAttempt switch
            {
                0 => 2,
                1 => 3,
                2 => 5,
                _ => 8
            };
            _recoveryAttempt++;
            delay = TimeSpan.FromSeconds(seconds);
        }
        _recoveryRetryTimer.Stop();
        _recoveryRetryTimer.Interval = delay;
        _recoveryRetryTimer.Start();
    }

    private void HandleLoadException(Exception ex)
    {
        if (_lifetime?.IsCancellationRequested == true)
        {
            return;
        }
        switch (ex)
        {
            case AgentRequestException agentEx when agentEx.StatusCode >= 500 || agentEx.ErrorCode == "storage_unavailable":
                HandleStorageUnavailable(agentEx);
                break;
            case HttpRequestException:
                HandleAgentDisconnected();
                break;
            case OperationCanceledException:
                StatusText.Text = "本地请求超时，系统会自动重试。";
                ScheduleRecoveryRetry();
                break;
            default:
                StatusText.Text = CleanError(ex.Message);
                break;
        }
    }

    private void HandleAgentDisconnected()
    {
        StatusText.Text = "本地 agent 未连接，正在自动拉起并重试...";
        ScheduleRecoveryRetry(TimeSpan.FromSeconds(2));
        if (_lifetime is { IsCancellationRequested: false } cts)
        {
            _ = Task.Run(() => AgentLauncher.EnsureRunningAsync(cts.Token));
        }
    }

    private void HandleStorageUnavailable(AgentRequestException ex)
    {
        var svcName = !string.IsNullOrWhiteSpace(ex.MysqlServiceName)
            ? ex.MysqlServiceName
            : (_lastKnownStatus?.MysqlServiceName ?? "");
        var svcState = !string.IsNullOrWhiteSpace(ex.MysqlServiceState)
            ? ex.MysqlServiceState
            : (_lastKnownStatus?.MysqlService ?? "");

        StatusText.Text = "Agent 已连接，但本地存储暂不可用，系统会自动重试。";
        ScheduleRecoveryRetry();

        if (string.IsNullOrWhiteSpace(svcName))
        {
            return;
        }
        if (_lifetime is not { IsCancellationRequested: false } cts)
        {
            return;
        }
        if (Interlocked.CompareExchange(ref _mysqlHealGate, 1, 0) != 0)
        {
            return;
        }

        var allowUac = !_uacPromptedForCurrentOutage || _forceAllowUacOnNextFailure;
        if (allowUac)
        {
            _uacPromptedForCurrentOutage = true;
            _forceAllowUacOnNextFailure = false;
        }

        _ = Task.Run(async () =>
        {
            try
            {
                var heal = await AgentLauncher.TryStartLocalMySqlServiceAsync(svcName, allowUac, cts.Token);
                if (string.IsNullOrEmpty(heal.ServiceName))
                {
                    return;
                }
                DispatcherQueue.TryEnqueue(() =>
                {
                    if (!_isUnhealthy || _lifetime?.IsCancellationRequested != false)
                    {
                        return;
                    }
                    if (heal.StartedOrRunning)
                    {
                        StatusText.Text = $"本地 {heal.ServiceName} 服务已启动，正在自动恢复连接...";
                        ScheduleRecoveryRetry(TimeSpan.FromSeconds(1.5));
                    }
                    else if (heal.State is "uac_cancelled" or "access_denied")
                    {
                        StatusText.Text = $"本地 {heal.ServiceName} 服务已停止（需管理员授权启动），可点击「刷新」重试，后台持续检测中。";
                    }
                });
            }
            catch
            {
                // Ignore background service heal failures; periodic retry continues.
            }
            finally
            {
                Interlocked.Exchange(ref _mysqlHealGate, 0);
            }
        });
    }

    private async Task LoadCurrentViewDataAsync()
    {
        if (_currentView == MainViewMode.Prompts)
        {
            await LoadPromptsAsync();
        }
        else
        {
            await LoadRecordsAsync();
        }
    }

    private async Task LoadRecordsAsync()
    {
        if (!await _loadGate.WaitAsync(0))
        {
            _hasPendingRecordReload = true;
            return;
        }
        SetBusy(true);
        try
        {
            do
            {
                _hasPendingRecordReload = false;
                using var cts = RequestTimeout();
                // 作废旧条件下仍在进行的分页请求，并暂停继续加载，直到第一页就绪。
                _records.CancelPending();
                var status = await _client.StatusAsync(cts.Token);
                _lastKnownStatus = status;
                var excludeFavorite = _currentView == MainViewMode.All;
                var keyword = SearchBox.Text?.Trim() ?? "";
                _lastExecutedKeyword = keyword;
                var records = await _client.ListAsync(keyword, _date, _sensitiveOnly, _favoriteOnly, 0, cts.Token, excludeFavorite);

                _records.Accept = record => !(excludeFavorite && record.IsFavorite);
                _records.ResetWith(records);
                MarkHealthy();
                StatusText.Text = BuildStatus(status);
                if (_records.Count > 0)
                {
                    RecordsList.ScrollIntoView(_records[0]);
                }
            } while (_hasPendingRecordReload && _currentView != MainViewMode.Prompts);
        }
        catch (Exception ex)
        {
            HandleLoadException(ex);
        }
        finally
        {
            SetBusy(false);
            _loadGate.Release();
            if (_hasPendingPromptReload && _currentView == MainViewMode.Prompts)
            {
                _ = LoadPromptsAsync();
            }
        }
    }

    private async Task LoadPromptsAsync()
    {
        if (!await _loadGate.WaitAsync(0))
        {
            _hasPendingPromptReload = true;
            return;
        }
        SetBusy(true);
        try
        {
            do
            {
                _hasPendingPromptReload = false;
                using var cts = RequestTimeout();
                var status = await _client.StatusAsync(cts.Token);
                _lastKnownStatus = status;
                var keyword = SearchBox.Text?.Trim() ?? "";
                _lastExecutedKeyword = keyword;
                var prompts = await _client.ListPromptsAsync(_promptCategory, keyword, cts.Token);
                _prompts.Clear();
                foreach (var p in prompts)
                {
                    _prompts.Add(p);
                }
                MarkHealthy();
                StatusText.Text = $"常用提示词库 / 已加载 {_prompts.Count} 条模板";
            } while (_hasPendingPromptReload && _currentView == MainViewMode.Prompts);
        }
        catch (Exception ex)
        {
            HandleLoadException(ex);
        }
        finally
        {
            SetBusy(false);
            _loadGate.Release();
            if (_hasPendingRecordReload && _currentView != MainViewMode.Prompts)
            {
                _ = LoadRecordsAsync();
            }
        }
    }

    private async Task WatchAgentEventsAsync(CancellationToken cancellationToken)
    {
        var hadDisconnect = false;
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                using var response = await _client.OpenEventsResponseAsync(cancellationToken);
                await using var stream = await response.Content.ReadAsStreamAsync(cancellationToken);
                using var reader = new StreamReader(stream);
                while (!cancellationToken.IsCancellationRequested)
                {
                    var line = await reader.ReadLineAsync(cancellationToken);
                    if (line is null)
                    {
                        break;
                    }
                    if (line.StartsWith(": connected", StringComparison.Ordinal))
                    {
                        if (hadDisconnect || _isUnhealthy)
                        {
                            hadDisconnect = false;
                            QueueEventRefresh();
                        }
                        continue;
                    }
                    if (line.StartsWith("data: ", StringComparison.Ordinal))
                    {
                        var shouldSkip = false;
                        var payload = line.Substring(6).Trim();
                        try
                        {
                            using var doc = JsonDocument.Parse(payload);
                            if (doc.RootElement.TryGetProperty("record_id", out var rIdProp))
                            {
                                var rId = rIdProp.GetString();
                                if (!string.IsNullOrEmpty(rId) && _suppressedEventRecordIds.TryGetValue(rId, out var expireAt))
                                {
                                    if (DateTime.UtcNow <= expireAt)
                                    {
                                        shouldSkip = true;
                                    }
                                    else
                                    {
                                        _suppressedEventRecordIds.TryRemove(rId, out _);
                                    }
                                }
                            }
                        }
                        catch
                        {
                            // 忽略心跳或非 JSON 数据，回退正常处理
                        }

                        if (!shouldSkip)
                        {
                            QueueEventRefresh();
                        }
                    }
                }
                hadDisconnect = true;
            }
            catch when (cancellationToken.IsCancellationRequested)
            {
                return;
            }
            catch
            {
                hadDisconnect = true;
                await Task.Delay(TimeSpan.FromSeconds(2), cancellationToken);
            }
        }
    }

    private void QueueEventRefresh()
    {
        DispatcherQueue.TryEnqueue(() =>
        {
            if (_eventRefreshTimer is null || _lifetime?.IsCancellationRequested != false)
            {
                return;
            }
            _eventRefreshTimer.Stop();
            _eventRefreshTimer.Start();
        });
    }

    private async void EventRefreshTimer_Tick(DispatcherQueueTimer sender, object args)
    {
        await LoadCurrentViewDataAsync();
    }

    #region Navigation & Filtering

    private void UpdateNavButtons(MainViewMode mode)
    {
        _currentView = mode;
        NavAllButton.Style = mode == MainViewMode.All ? (Style)Resources["AccentButtonStyle"] : (Style)Application.Current.Resources["DefaultButtonStyle"];
        NavFavoritesButton.Style = mode == MainViewMode.Favorites ? (Style)Resources["AccentButtonStyle"] : (Style)Application.Current.Resources["DefaultButtonStyle"];
        NavPromptsButton.Style = mode == MainViewMode.Prompts ? (Style)Resources["AccentButtonStyle"] : (Style)Application.Current.Resources["DefaultButtonStyle"];
        NavSensitiveButton.Style = mode == MainViewMode.Sensitive ? (Style)Resources["AccentButtonStyle"] : (Style)Application.Current.Resources["DefaultButtonStyle"];

        if (mode == MainViewMode.Prompts)
        {
            RecordsList.Visibility = Visibility.Collapsed;
            PromptsContainer.Visibility = Visibility.Visible;
            NewPromptButton.Visibility = Visibility.Visible;
            DateFilterButton.Visibility = Visibility.Collapsed;
            BatchDeleteButton.Visibility = Visibility.Collapsed;
            CaptureButton.Visibility = Visibility.Collapsed;
        }
        else
        {
            RecordsList.Visibility = Visibility.Visible;
            PromptsContainer.Visibility = Visibility.Collapsed;
            NewPromptButton.Visibility = Visibility.Collapsed;
            DateFilterButton.Visibility = Visibility.Visible;
            BatchDeleteButton.Visibility = Visibility.Visible;
            CaptureButton.Visibility = Visibility.Visible;
        }
    }

    private async void NavAll_Click(object sender, RoutedEventArgs e)
    {
        _date = "";
        _sensitiveOnly = false;
        _favoriteOnly = false;
        UpdateNavButtons(MainViewMode.All);
        await LoadRecordsAsync();
    }

    private async void NavFavorites_Click(object sender, RoutedEventArgs e)
    {
        _sensitiveOnly = false;
        _favoriteOnly = true;
        UpdateNavButtons(MainViewMode.Favorites);
        await LoadRecordsAsync();
    }

    private async void NavPrompts_Click(object sender, RoutedEventArgs e)
    {
        UpdateNavButtons(MainViewMode.Prompts);
        await LoadPromptsAsync();
    }

    private async void NavSensitive_Click(object sender, RoutedEventArgs e)
    {
        _sensitiveOnly = true;
        _favoriteOnly = false;
        UpdateNavButtons(MainViewMode.Sensitive);
        await LoadRecordsAsync();
    }

    private async void SearchBox_KeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (e.Key == VirtualKey.Enter)
        {
            _searchDebounceTimer?.Stop();
            _lastExecutedKeyword = SearchBox.Text?.Trim() ?? "";
            await LoadCurrentViewDataAsync();
        }
    }

    private async void SearchBox_TextChanged(object sender, TextChangedEventArgs e)
    {
        var box = sender as TextBox ?? SearchBox;
        if (box is null)
        {
            return;
        }
        var text = box.Text?.Trim() ?? "";
        if (string.IsNullOrEmpty(text))
        {
            _searchDebounceTimer?.Stop();
            if (!string.IsNullOrEmpty(_lastExecutedKeyword))
            {
                _lastExecutedKeyword = "";
                await LoadCurrentViewDataAsync();
            }
        }
        else
        {
            _searchDebounceTimer?.Stop();
            _searchDebounceTimer?.Start();
        }
    }

    private async void SearchDebounceTimer_Tick(DispatcherQueueTimer sender, object args)
    {
        _searchDebounceTimer?.Stop();
        var text = SearchBox.Text?.Trim() ?? "";
        if (text != _lastExecutedKeyword)
        {
            _lastExecutedKeyword = text;
            await LoadCurrentViewDataAsync();
        }
    }

    private async void RefreshButton_Click(object sender, RoutedEventArgs e)
    {
        _searchDebounceTimer?.Stop();
        _forceAllowUacOnNextFailure = true;
        _recoveryAttempt = 0;
        _lastExecutedKeyword = SearchBox.Text?.Trim() ?? "";
        await LoadCurrentViewDataAsync();
    }

    private async void DateButton_Click(object sender, RoutedEventArgs e)
    {
        var value = await PromptTextAsync("按时间筛选", "yyyyMMdd", _date);
        if (value is null)
        {
            return;
        }
        _date = value.Trim();
        await LoadRecordsAsync();
    }

    #endregion

    #region Record Star & Actions

    private async Task ToggleFavoriteRecordAsync(ClipboardRecordView record)
    {
        if (_currentView == MainViewMode.Sensitive || record.IsSensitive)
        {
            return;
        }

        var nextState = !record.IsFavorite;
        var removedIndex = -1;
        try
        {
            // 1. 本地模型即时原地更新，触发 INotifyPropertyChanged，星标平滑变化，无任何抖动
            record.IsFavorite = nextState;
            if (_favoriteOnly && !nextState)
            {
                removedIndex = _records.RemoveTracked(record);
            }
            else if (_currentView == MainViewMode.All && nextState)
            {
                // 当内容已经被收藏之后，收藏内容不再出现在“全部”视图，立即从当前列表中平滑移除
                removedIndex = _records.RemoveTracked(record);
            }

            // 2. 标记抑制此记录随后 3 秒内的 SSE 全量重载，避免页面清空重刷
            SuppressRecordRefresh(record.Id);

            // 3. 异步提交到本地 Agent 持久化
            using var cts = RequestTimeout();
            await _client.SetFavoriteAsync(record.Id, nextState, cts.Token);
            StatusText.Text = nextState ? "已添加至收藏夹" : "已从收藏夹移除";
        }
        catch (Exception ex)
        {
            // 若请求失败，回滚本地状态并插回原索引位置
            record.IsFavorite = !nextState;
            if (removedIndex >= 0)
            {
                _records.InsertTracked(removedIndex, record);
            }
            StatusText.Text = CleanError(ex.Message);
        }
    }

    private async void RecordStarButton_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not Button btn || btn.DataContext is not ClipboardRecordView record)
        {
            return;
        }
        await ToggleFavoriteRecordAsync(record);
    }

    private async void RecordConvertToPrompt_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not Button btn || btn.DataContext is not ClipboardRecordView record)
        {
            return;
        }
        var initialContent = record.ContentText;
        if (string.IsNullOrWhiteSpace(initialContent))
        {
            StatusText.Text = "当前记录无文本内容，无法转为提示词";
            return;
        }
        await ShowEditPromptDialogAsync(new PromptView
        {
            Title = record.Summary.Length > 20 ? record.Summary[..20] : record.Summary,
            Category = "通用",
            TemplateContent = initialContent,
            Shortcut = ""
        });
    }

    private async void BatchDeleteButton_Click(object sender, RoutedEventArgs e)
    {
        var result = await ConfirmAsync("批量删除", "将删除当前结果中的普通剪贴内容（账户密码及已收藏记录会自动保留）。");
        if (!result)
        {
            return;
        }
        await RunAgentActionAsync(async token => await _client.BatchDeleteAsync(_records.Select(r => r.Id), skipSensitive: true, skipFavorite: true, token));
        await LoadRecordsAsync();
    }

    private async void CaptureButton_Click(object sender, RoutedEventArgs e)
    {
        await RunAgentActionAsync(async token => await _client.CaptureCurrentAsync(token));
        await LoadRecordsAsync();
    }

    private async void DeleteButton_Click(object sender, RoutedEventArgs e)
    {
        var record = SelectedOrNull();
        if (record is null)
        {
            return;
        }
        if (record.IsSensitive)
        {
            var ok = await ConfirmAsync("删除", $"该内容已设置为{record.KindLabel}，确定删除吗？");
            if (!ok)
            {
                return;
            }
        }
        await RunAgentActionAsync(async token => await _client.DeleteAsync(record.Id, token));
        await LoadRecordsAsync();
    }

    private async void PasswordButton_Click(object sender, RoutedEventArgs e)
    {
        var record = SelectedOrNull();
        if (record is null)
        {
            return;
        }
        if (record.IsImage)
        {
            StatusText.Text = "图片不能标记为账户或密码";
            return;
        }
        var input = await PromptSensitiveAsync("密码", "账户", "", allowGenerate: false);
        if (input is null)
        {
            return;
        }
        await RunAgentActionAsync(async token => await _client.MarkPasswordAsync(record.Id, input.Value, input.Remark, token));
        await LoadRecordsAsync();
    }

    private async void AccountButton_Click(object sender, RoutedEventArgs e)
    {
        var record = SelectedOrNull();
        if (record is null)
        {
            return;
        }
        if (record.IsImage)
        {
            StatusText.Text = "图片不能标记为账户或密码";
            return;
        }
        var input = await PromptSensitiveAsync("账户", "密码", GeneratePassword(), allowGenerate: true);
        if (input is null)
        {
            return;
        }
        await RunAgentActionAsync(async token => await _client.MarkAccountAsync(record.Id, input.Value, input.Remark, token));
        await LoadRecordsAsync();
    }

    private async void ViewButton_Click(object sender, RoutedEventArgs e)
    {
        var record = SelectedOrNull();
        if (record is null)
        {
            return;
        }
        await ShowRecordAsync(record);
    }

    private async void CopyButton_Click(object sender, RoutedEventArgs e)
    {
        var record = SelectedOrNull();
        if (record is null)
        {
            return;
        }
        var field = await SelectFieldAsync(record, defaultField: record.SensitiveType == "account" ? "account" : "password");
        if (field is null)
        {
            return;
        }
        await RunAgentActionAsync(async token => await _client.CopyAsync(record.Id, field, token));
    }

    private async void PasteButton_Click(object sender, RoutedEventArgs e)
    {
        await PasteSelectedAsync();
    }

    private ulong GetPasteTargetHandle()
    {
        if (App.Current is App app && app.MainWindow is MainWindow mw)
        {
            mw.FocusPasteTarget();
            return mw.PasteTargetHandle;
        }
        return 0;
    }

    private async Task PasteSelectedAsync()
    {
        var record = SelectedOrNull();
        if (record is null)
        {
            return;
        }
        var field = await SelectFieldAsync(record, defaultField: record.SensitiveType == "account" ? "account" : "password");
        if (field is null)
        {
            return;
        }
        var targetHwnd = GetPasteTargetHandle();
        await RunAgentActionAsync(async token => await _client.PasteAsync(record.Id, field, targetHwnd, token));
    }

    private void RecordContextMenu_Copy_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        CopyButton_Click(sender, e);
    }

    private void RecordContextMenu_Paste_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        PasteButton_Click(sender, e);
    }

    private async void RecordContextMenu_Edit_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        var record = SelectedOrNull();
        if (record is null)
        {
            return;
        }
        await EditRecordAsync(record);
    }

    private async Task EditRecordAsync(ClipboardRecordView record)
    {
        if (record.IsSensitive)
        {
            await EditSensitiveCredentialsAsync(record);
            return;
        }

        string initialText = record.ContentText ?? string.Empty;

        var box = new TextBox
        {
            AcceptsReturn = true,
            TextWrapping = TextWrapping.Wrap,
            MinHeight = 160,
            MaxHeight = 360,
            MinWidth = 420,
            Text = initialText
        };

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = record.IsImage ? "编辑图片备注" : "编辑内容",
            Content = box,
            PrimaryButtonText = "保存并收藏",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };

        dialog.Opened += (s, args) =>
        {
            if (!string.IsNullOrEmpty(initialText) && NormalizeMultilineText(box.Text) != NormalizeMultilineText(initialText))
            {
                box.Text = initialText;
            }
            box.Focus(FocusState.Programmatic);
            box.SelectionStart = box.Text.Length;
            box.SelectionLength = 0;
        };

        var result = await dialog.ShowAsync();
        if (result != ContentDialogResult.Primary)
        {
            return;
        }

        var newContent = NormalizeMultilineText(box.Text);

        // 原地覆盖更新并保存为收藏，同时通过受保护通道写入系统剪贴板（避免产生重复未收藏记录）
        try
        {
            // 标记抑制此记录随后 3 秒内的 SSE 全量重载，避免页面清空重刷
            SuppressRecordRefresh(record.Id);

            using var cts = RequestTimeout();
            var updated = await _client.UpdateRecordAsync(record.Id, newContent, favorite: true, cts.Token);
            await _client.WriteClipboardAsync(newContent, paste: false, 0, cts.Token);

            // 3. 视图联动：若当前在「全部」视图（排除了已收藏），则从当前列表平滑移除归入收藏夹
            if (_currentView == MainViewMode.All)
            {
                _records.RemoveTracked(record);
            }
            else
            {
                // 其他视图原地更新属性并置为已收藏
                record.ContentText = newContent;
                if (record.IsImage)
                {
                    record.Summary = string.IsNullOrWhiteSpace(newContent) ? "图片" : ("图片 · " + FormatSingleLineSummary(newContent, 60));
                }
                else
                {
                    record.Summary = FormatSingleLineSummary(newContent, 120);
                }
                record.IsFavorite = true;
            }
            StatusText.Text = "已保存并收藏，最新内容已写入系统剪贴板";
        }
        catch (Exception ex)
        {
            StatusText.Text = CleanError(ex.Message);
        }
    }

    private async Task EditSensitiveCredentialsAsync(ClipboardRecordView record)
    {
        RevealedSensitive? revealed = null;
        try
        {
            using var cts = RequestTimeout();
            revealed = await _client.RevealAsync(record.Id, cts.Token);
        }
        catch
        {
            // 若获取失败容错降级
        }

        string initialPlatform = revealed?.Platform ?? "";
        string initialAccount = revealed?.Account ?? "";
        string initialPassword = revealed?.Password ?? "";
        string initialTwoFactor = revealed?.TwoFactor ?? "";
        string initialRemark = revealed?.Remark ?? "";

        // 若历史未拆分数据，降级回显
        if (string.IsNullOrEmpty(initialAccount) && string.IsNullOrEmpty(initialPassword) && revealed != null && !string.IsNullOrEmpty(revealed.Content))
        {
            if (record.SensitiveType == "account")
            {
                initialAccount = revealed.Content;
            }
            else
            {
                initialPassword = revealed.Content;
            }
        }

        var platformBox = new TextBox
        {
            Text = initialPlatform,
            PlaceholderText = "例如: GitHub, 谷歌, 阿里云等",
            MinWidth = 420
        };
        var accountBox = new TextBox
        {
            Text = initialAccount,
            PlaceholderText = "登录用户名 / 邮箱 / 手机号",
            MinWidth = 420
        };
        var passwordBox = new TextBox
        {
            Text = initialPassword,
            PlaceholderText = "登录密码",
            FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Cascadia Mono, Consolas"),
            MinWidth = 420
        };
        var twoFactorBox = new TextBox
        {
            Text = initialTwoFactor,
            PlaceholderText = "双重认证 Base32 密钥 (留空则不启用)",
            FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Cascadia Mono, Consolas"),
            MinWidth = 420
        };
        var remarkBox = new TextBox
        {
            PlaceholderText = "补充说明",
            AcceptsReturn = true,
            MinHeight = 60,
            MaxHeight = 120,
            TextWrapping = TextWrapping.Wrap,
            MinWidth = 420,
            Text = initialRemark
        };

        var panel = new StackPanel { Spacing = 10, MinWidth = 420 };
        panel.Children.Add(new TextBlock { Text = "平台", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 13 });
        panel.Children.Add(platformBox);
        panel.Children.Add(new TextBlock { Text = "账户", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 13 });
        panel.Children.Add(accountBox);
        panel.Children.Add(new TextBlock { Text = "密码", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 13 });
        panel.Children.Add(passwordBox);
        panel.Children.Add(new TextBlock { Text = "2FA 密钥 (Base32)", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 13 });
        panel.Children.Add(twoFactorBox);
        panel.Children.Add(new TextBlock { Text = "备注", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 13 });
        panel.Children.Add(remarkBox);

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = "编辑凭据",
            Content = new ScrollViewer { Content = panel, MaxHeight = 480 },
            PrimaryButtonText = "保存",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };

        dialog.Opened += (s, args) =>
        {
            var targetBox = string.IsNullOrEmpty(platformBox.Text) ? platformBox : (!string.IsNullOrEmpty(accountBox.Text) ? passwordBox : accountBox);
            targetBox.Focus(FocusState.Programmatic);
            targetBox.SelectionStart = targetBox.Text.Length;
            targetBox.SelectionLength = 0;
        };

        var result = await dialog.ShowAsync();
        if (result != ContentDialogResult.Primary)
        {
            return;
        }

        string newPlatform = platformBox.Text.Trim();
        string newAccount = accountBox.Text.Trim();
        string newPassword = passwordBox.Text;
        string newTwoFactor = twoFactorBox.Text.Trim();
        string newRemark = NormalizeMultilineText(remarkBox.Text);

        // 原地覆盖更新，不写入剪贴板，保持原本收藏状态不变
        try
        {
            SuppressRecordRefresh(record.Id);

            using var cts = RequestTimeout();
            var updated = await _client.UpdateCredentialsAsync(record.Id, newPlatform, newAccount, newPassword, newTwoFactor, newRemark, favorite: record.IsFavorite, cts.Token);

            string newSummary;
            if (!string.IsNullOrEmpty(newPlatform) && !string.IsNullOrEmpty(newAccount))
            {
                newSummary = $"{newPlatform} {newAccount}";
            }
            else if (!string.IsNullOrEmpty(newPlatform))
            {
                newSummary = newPlatform;
            }
            else if (!string.IsNullOrEmpty(newAccount))
            {
                newSummary = newAccount;
            }
            else
            {
                newSummary = "账户密码";
            }

            // 视图联动：在账户密码视图原地刷新属性，保留原有收藏状态
            record.Summary = newSummary;
            record.ContentText = newSummary;
            record.SourcePlatform = newPlatform;
            StatusText.Text = "凭据已保存";
        }
        catch (Exception ex)
        {
            StatusText.Text = CleanError(ex.Message);
        }
    }

    private void RecordContextMenu_View_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        ViewButton_Click(sender, e);
    }

    private async void RecordContextMenu_ToggleFavorite_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        var record = SelectedOrNull();
        if (record is null) return;
        await ToggleFavoriteRecordAsync(record);
    }

    private void RecordContextMenu_ConvertToPrompt_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        RecordConvertToPrompt_Click(sender, e);
    }

    private void RecordContextMenu_Password_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        PasswordButton_Click(sender, e);
    }

    private void RecordContextMenu_Account_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        AccountButton_Click(sender, e);
    }

    private void RecordContextMenu_Delete_Click(object sender, RoutedEventArgs e)
    {
        EnsureTargetSelected(sender);
        DeleteButton_Click(sender, e);
    }

    private void EnsureTargetSelected(object sender)
    {
        if (sender is FrameworkElement fe && fe.DataContext is ClipboardRecordView record)
        {
            _selected = record;
            RecordsList.SelectedItem = record;
        }
    }

    private void RecordsList_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        // 选中行展开内联操作栏：每次选择变化只更新旧/新两个对象，与列表长度无关。
        foreach (var removed in e.RemovedItems)
        {
            if (removed is ClipboardRecordView oldRecord)
            {
                oldRecord.IsActive = false;
            }
        }
        foreach (var added in e.AddedItems)
        {
            if (added is ClipboardRecordView newRecord)
            {
                newRecord.IsActive = true;
            }
        }

        _selected = RecordsList.SelectedItem as ClipboardRecordView;
        if (_selected is not null)
        {
            StatusText.Text = "已选择: " + ShortId(_selected.Id);
        }
    }

    private async void RecordsList_DoubleTapped(object sender, DoubleTappedRoutedEventArgs e)
    {
        await PasteSelectedAsync();
    }

    private async void RecordsList_KeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (_selected is null)
        {
            return;
        }
        if (e.Key == VirtualKey.Enter)
        {
            e.Handled = true;
            CopyButton_Click(sender, e);
        }
        else if (e.Key == VirtualKey.Delete)
        {
            e.Handled = true;
            DeleteButton_Click(sender, e);
        }
        else if (e.Key == VirtualKey.S)
        {
            // 账户密码视图下或敏感记录禁用 S 键标星快捷键
            if (_currentView == MainViewMode.Sensitive || _selected.IsSensitive)
            {
                return;
            }
            e.Handled = true;
            await ToggleFavoriteRecordAsync(_selected);
        }
    }

    private async Task CopyRecordAsync(ClipboardRecordView record)
    {
        if (!await _actionGate.WaitAsync(0))
        {
            return;
        }
        try
        {
            var field = await SelectFieldAsync(record, defaultField: record.SensitiveType == "account" ? "account" : "password");
            if (field is null)
            {
                return;
            }
            await RunAgentActionAsync(async token => await _client.CopyAsync(record.Id, field, token));
        }
        finally
        {
            _actionGate.Release();
        }
    }

    #endregion

    #region Prompts Management & Execution

    private async void PromptCatAll_Click(object sender, RoutedEventArgs e) => await SetPromptCategoryAsync("");
    private async void PromptCatDev_Click(object sender, RoutedEventArgs e) => await SetPromptCategoryAsync("代码开发");
    private async void PromptCatTrans_Click(object sender, RoutedEventArgs e) => await SetPromptCategoryAsync("翻译润色");
    private async void PromptCatOffice_Click(object sender, RoutedEventArgs e) => await SetPromptCategoryAsync("日常办公");

    private async Task SetPromptCategoryAsync(string category)
    {
        _promptCategory = category;
        await LoadPromptsAsync();
    }

    private void PromptsList_ItemClick(object sender, ItemClickEventArgs e)
    {
        _selectedPrompt = e.ClickedItem as PromptView;
    }

    private async void PromptsList_DoubleTapped(object sender, DoubleTappedRoutedEventArgs e)
    {
        var prompt = (e.OriginalSource as FrameworkElement)?.DataContext as PromptView
            ?? PromptsList.SelectedItem as PromptView
            ?? _selectedPrompt;
        if (prompt is not null)
        {
            _selectedPrompt = prompt;
            await ExecutePromptAsync(prompt);
        }
    }

    private async void ExecutePromptButton_Click(object sender, RoutedEventArgs e)
    {
        if (sender is Button btn && btn.DataContext is PromptView prompt)
        {
            await ExecutePromptAsync(prompt);
        }
    }

    private async Task ExecutePromptAsync(PromptView prompt)
    {
        try
        {
            var finalContent = prompt.TemplateContent;
            // 提取当前系统剪贴板文本
            var clipPackage = Clipboard.GetContent();
            var clipText = "";
            if (clipPackage != null && clipPackage.Contains(StandardDataFormats.Text))
            {
                clipText = await clipPackage.GetTextAsync();
            }

            // 识别模板变量
            var variables = prompt.Variables;
            var values = new Dictionary<string, string>();

            // 优先替换内置剪贴板变量
            if (variables.Contains("clip") || variables.Contains("input"))
            {
                values["clip"] = clipText;
                values["input"] = clipText;
            }

            // 过滤出需要人工输入的变量
            var requiredVars = variables.Where(v => v != "clip" && v != "input").ToList();
            if (requiredVars.Count > 0)
            {
                var inputValues = await PromptWithVariablesDialogAsync(prompt, requiredVars, clipText);
                if (inputValues == null)
                {
                    return; // 用户取消
                }
                foreach (var pair in inputValues)
                {
                    values[pair.Key] = pair.Value;
                }
            }

            // 执行模板字符串插值
            foreach (var kvp in values)
            {
                var regex = new Regex(@"\{" + Regex.Escape(kvp.Key) + @"(?::[^}]+)?\}");
                finalContent = regex.Replace(finalContent, kvp.Value);
            }

            // 受保护写入系统剪贴板，自动切回原目标窗口并通过 SendInput 模拟粘贴注入
            using (var cts = RequestTimeout())
            {
                await _client.WriteClipboardAsync(finalContent, paste: true, GetPasteTargetHandle(), cts.Token);
            }

            // 自增使用频次统计
            _ = Task.Run(async () =>
            {
                try
                {
                    using var cts = RequestTimeout();
                    await _client.UsePromptAsync(prompt.Id, cts.Token);
                }
                catch { }
            });

            StatusText.Text = $"提示词「{prompt.Title}」已组装并粘贴注入！";
        }
        catch (Exception ex)
        {
            StatusText.Text = CleanError(ex.Message);
        }
    }

    private async Task<Dictionary<string, string>?> PromptWithVariablesDialogAsync(PromptView prompt, List<string> variables, string defaultClip)
    {
        var panel = new StackPanel { Spacing = 10, MinWidth = 360 };
        var inputs = new Dictionary<string, TextBox>();

        foreach (var v in variables)
        {
            var label = new TextBlock { Text = $"参数 {{{v}}}:", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold };
            var tb = new TextBox { PlaceholderText = $"请输入 {v} 的值" };
            panel.Children.Add(label);
            panel.Children.Add(tb);
            inputs[v] = tb;
        }

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = $"填充提示词参数 - {prompt.Title}",
            Content = panel,
            PrimaryButtonText = "组装并复制",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };

        var result = await dialog.ShowAsync();
        if (result != ContentDialogResult.Primary)
        {
            return null;
        }
        var dict = new Dictionary<string, string>();
        foreach (var kvp in inputs)
        {
            dict[kvp.Key] = kvp.Value.Text;
        }
        return dict;
    }

    private async void NewPromptButton_Click(object sender, RoutedEventArgs e)
    {
        await ShowEditPromptDialogAsync(new PromptView
        {
            Title = "",
            Category = "通用",
            TemplateContent = "",
            Shortcut = ""
        });
    }

    private async void EditPromptButton_Click(object sender, RoutedEventArgs e)
    {
        if (sender is Button btn && btn.DataContext is PromptView prompt)
        {
            await ShowEditPromptDialogAsync(prompt);
        }
    }

    private async void DeletePromptButton_Click(object sender, RoutedEventArgs e)
    {
        if (sender is Button btn && btn.DataContext is PromptView prompt)
        {
            if (!await ConfirmAsync("删除提示词", $"确定要删除提示词「{prompt.Title}」吗？"))
            {
                return;
            }
            try
            {
                using var cts = RequestTimeout();
                await _client.DeletePromptAsync(prompt.Id, cts.Token);
                _prompts.Remove(prompt);
                StatusText.Text = "提示词已删除";
            }
            catch (Exception ex)
            {
                StatusText.Text = CleanError(ex.Message);
            }
        }
    }

    private async Task ShowEditPromptDialogAsync(PromptView prompt)
    {
        var titleBox = new TextBox { Text = prompt.Title, PlaceholderText = "提示词标题，例如：Code Review 专家" };
        var categoryBox = new TextBox { Text = prompt.Category, PlaceholderText = "分类，例如：代码开发 / 翻译润色" };
        var shortcutBox = new TextBox { Text = prompt.Shortcut, PlaceholderText = "快捷代号，例如：/cr" };
        var contentBox = new TextBox
        {
            PlaceholderText = "提示词正文模板，支持 {clip}、{target_lang} 等插槽变量",
            AcceptsReturn = true,
            MinHeight = 120,
            TextWrapping = TextWrapping.Wrap,
            Text = prompt.TemplateContent
        };

        var favoriteCheckBox = new CheckBox
        {
            Content = "设为星标收藏提示词",
            IsChecked = prompt.IsFavorite
        };

        var panel = new StackPanel { Spacing = 10, MinWidth = 420 };
        panel.Children.Add(new TextBlock { Text = "标题", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        panel.Children.Add(titleBox);
        panel.Children.Add(new TextBlock { Text = "分类", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        panel.Children.Add(categoryBox);
        panel.Children.Add(new TextBlock { Text = "快捷指令代号 (可选)", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        panel.Children.Add(shortcutBox);
        panel.Children.Add(new TextBlock { Text = "模板正文 ({clip} 代表剪贴板内容)", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        panel.Children.Add(contentBox);
        panel.Children.Add(favoriteCheckBox);

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = string.IsNullOrEmpty(prompt.Id) ? "新建常用提示词" : "编辑提示词",
            Content = panel,
            PrimaryButtonText = "保存",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };

        if (await dialog.ShowAsync() == ContentDialogResult.Primary)
        {
            if (string.IsNullOrWhiteSpace(titleBox.Text) || string.IsNullOrWhiteSpace(contentBox.Text))
            {
                StatusText.Text = "标题与模板正文不能为空";
                return;
            }
            prompt.Title = titleBox.Text.Trim();
            prompt.Category = string.IsNullOrWhiteSpace(categoryBox.Text) ? "通用" : categoryBox.Text.Trim();
            prompt.Shortcut = shortcutBox.Text.Trim();
            prompt.TemplateContent = NormalizeMultilineText(contentBox.Text);
            prompt.IsFavorite = favoriteCheckBox.IsChecked == true;

            try
            {
                using var cts = RequestTimeout();
                var saved = await _client.SavePromptAsync(prompt, cts.Token);
                await LoadPromptsAsync();
                StatusText.Text = "提示词保存成功";
            }
            catch (Exception ex)
            {
                StatusText.Text = CleanError(ex.Message);
            }
        }
    }

    #endregion

    #region Scroll & List Helpers

    private void NavScrollViewer_PointerWheelChanged(object sender, PointerRoutedEventArgs e)
    {
        if (sender is ScrollViewer sv)
        {
            var delta = e.GetCurrentPoint(sv).Properties.MouseWheelDelta;
            sv.ChangeView(sv.HorizontalOffset - delta, null, null, disableAnimation: false);
            e.Handled = true;
        }
    }

    private Task<IReadOnlyList<ClipboardRecordView>> FetchRecordsPageAsync(int offset, CancellationToken cancellationToken)
    {
        var keyword = SearchBox.Text?.Trim() ?? "";
        return _client.ListAsync(keyword, _date, _sensitiveOnly, _favoriteOnly, offset, cancellationToken, _currentView == MainViewMode.All);
    }

    private void RecordsList_ContainerContentChanging(ListViewBase sender, ContainerContentChangingEventArgs args)
    {
    }

    private ClipboardRecordView? SelectedOrNull()
    {
        if (_selected is not null)
        {
            return _selected;
        }
        StatusText.Text = "请先选择一条记录";
        return null;
    }

    private async Task<string?> SelectFieldAsync(ClipboardRecordView record, string defaultField)
    {
        if (!record.IsSensitive)
        {
            return "content";
        }
        var options = new ComboBox { SelectedValuePath = "Tag", MinWidth = 240 };
        options.Items.Add(new ComboBoxItem { Content = "原始内容", Tag = "content" });
        options.Items.Add(new ComboBoxItem { Content = "平台", Tag = "platform" });
        options.Items.Add(new ComboBoxItem { Content = "账户", Tag = "account" });
        options.Items.Add(new ComboBoxItem { Content = "密码", Tag = "password" });
        options.Items.Add(new ComboBoxItem { Content = "2FA 密钥", Tag = "two_factor" });
        options.Items.Add(new ComboBoxItem { Content = "备注", Tag = "remark" });
        foreach (ComboBoxItem item in options.Items)
        {
            if ((string)item.Tag == defaultField)
            {
                options.SelectedItem = item;
                break;
            }
        }
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = "选择字段",
            Content = options,
            PrimaryButtonText = "确定",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };
        var result = await dialog.ShowAsync();
        return result == ContentDialogResult.Primary && options.SelectedItem is ComboBoxItem selected
            ? selected.Tag?.ToString()
            : null;
    }

    private async Task RunAgentActionAsync(Func<CancellationToken, Task> action)
    {
        SetBusy(true);
        try
        {
            using var cts = RequestTimeout();
            await action(cts.Token);
            StatusText.Text = "操作完成";
        }
        catch (Exception ex)
        {
            StatusText.Text = CleanError(ex.Message);
        }
        finally
        {
            SetBusy(false);
        }
    }

    private void SetBusy(bool value)
    {
        BusyRing.IsActive = value;
    }

    private CancellationTokenSource RequestTimeout()
    {
        return new CancellationTokenSource(TimeSpan.FromSeconds(12));
    }

    private string BuildStatus(AgentStatus status)
    {
        var filter = _favoriteOnly ? "⭐ 收藏夹" : (_sensitiveOnly ? "🔒 账户密码" : "全部记录");
        if (!string.IsNullOrWhiteSpace(_date))
        {
            filter += " / " + _date;
        }
        return $"Agent {status.Status} / {status.DeviceId} / 本地安全存储 / {filter}";
    }

    private static string CleanError(string message)
    {
        return message.Replace("\r", " ").Replace("\n", " ").Trim();
    }

    private async Task<bool> ConfirmAsync(string title, string message)
    {
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = title,
            Content = message,
            PrimaryButtonText = "确定",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };
        return await dialog.ShowAsync() == ContentDialogResult.Primary;
    }

    private async Task<string?> PromptTextAsync(string title, string label, string defaultValue)
    {
        var box = new TextBox { Text = defaultValue, PlaceholderText = label };
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = title,
            Content = box,
            PrimaryButtonText = "确定",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };
        var result = await dialog.ShowAsync();
        return result == ContentDialogResult.Primary ? box.Text : null;
    }

    private async Task ShowRecordAsync(ClipboardRecordView record)
    {
        if (record.IsImage)
        {
            if (!Uri.TryCreate(record.ImageUrl, UriKind.Absolute, out var imageUri))
            {
                StatusText.Text = "图片地址无效";
                return;
            }
            var image = new Image
            {
                Source = new BitmapImage(imageUri),
                MaxWidth = 760,
                MaxHeight = 540,
                Stretch = Microsoft.UI.Xaml.Media.Stretch.Uniform
            };
            FrameworkElement dialogContent;
            if (!string.IsNullOrWhiteSpace(record.ContentText))
            {
                var imgPanel = new StackPanel { Spacing = 10 };
                imgPanel.Children.Add(new ScrollViewer { Content = image, MaxHeight = 420 });
                imgPanel.Children.Add(new TextBlock
                {
                    Text = "图片备注",
                    FontSize = 13,
                    FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                    Foreground = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["TextFillColorSecondaryBrush"]
                });
                var imgRemarkBox = ReadonlyTextBox(record.ContentText, multiline: true);
                imgRemarkBox.MaxHeight = 120;
                imgPanel.Children.Add(imgRemarkBox);
                dialogContent = imgPanel;
            }
            else
            {
                dialogContent = new ScrollViewer { Content = image };
            }

            var imageDialog = new ContentDialog
            {
                XamlRoot = XamlRoot,
                Title = "查看图片",
                Content = dialogContent,
                PrimaryButtonText = "确定",
                SecondaryButtonText = "编辑",
                CloseButtonText = "关闭",
                DefaultButton = ContentDialogButton.Primary
            };
            var imgResult = await imageDialog.ShowAsync();
            if (imgResult == ContentDialogResult.Secondary)
            {
                await EditRecordAsync(record);
            }
            return;
        }
        using var cts = RequestTimeout();
        var revealed = await _client.RevealAsync(record.Id, cts.Token);
        if (!record.IsSensitive)
        {
            var textDialog = new ContentDialog
            {
                XamlRoot = XamlRoot,
                Title = "查看",
                Content = new ScrollViewer
                {
                    Content = new TextBlock
                    {
                        Text = revealed.Content,
                        TextWrapping = TextWrapping.Wrap,
                        FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Cascadia Mono, Consolas")
                    },
                    MaxHeight = 480
                },
                PrimaryButtonText = "确定",
                SecondaryButtonText = "编辑",
                CloseButtonText = "关闭",
                DefaultButton = ContentDialogButton.Primary
            };
            var textResult = await textDialog.ShowAsync();
            if (textResult == ContentDialogResult.Secondary)
            {
                await EditRecordAsync(record);
            }
            return;
        }

        var panel = new StackPanel { Spacing = 10, MinWidth = 420 };
        if (!string.IsNullOrWhiteSpace(revealed.Platform))
        {
            var platformBox = ReadonlyTextBox(revealed.Platform);
            panel.Children.Add(FieldWithCopy("平台", platformBox, async () => await RunAgentActionAsync(async token => await _client.CopyAsync(record.Id, "platform", token))));
        }

        var accountBox = ReadonlyTextBox(revealed.Account);
        panel.Children.Add(FieldWithCopy("账户", accountBox, async () => await RunAgentActionAsync(async token => await _client.CopyAsync(record.Id, "account", token))));

        var passwordBox = ReadonlyTextBox(revealed.Password);
        panel.Children.Add(FieldWithCopy("密码", passwordBox, async () => await RunAgentActionAsync(async token => await _client.CopyAsync(record.Id, "password", token))));

        DispatcherTimer? totpTimer = null;
        if (!string.IsNullOrWhiteSpace(revealed.TwoFactor))
        {
            var totpCard = new Border
            {
                Background = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["CardBackgroundFillColorSecondaryBrush"],
                BorderBrush = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["CardStrokeColorDefaultBrush"],
                BorderThickness = new Thickness(1),
                CornerRadius = new CornerRadius(8),
                Padding = new Thickness(12)
            };
            var totpStack = new StackPanel { Spacing = 8 };

            var totpHeader = new Grid();
            totpHeader.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            totpHeader.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });

            var titleText = new TextBlock
            {
                Text = "2FA 一次性动态验证码",
                FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                FontSize = 13,
                VerticalAlignment = VerticalAlignment.Center
            };
            var timerText = new TextBlock
            {
                FontSize = 12,
                Foreground = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["TextFillColorSecondaryBrush"],
                VerticalAlignment = VerticalAlignment.Center
            };
            Grid.SetColumn(titleText, 0);
            Grid.SetColumn(timerText, 1);
            totpHeader.Children.Add(titleText);
            totpHeader.Children.Add(timerText);
            totpStack.Children.Add(totpHeader);

            var codeRow = new Grid { ColumnSpacing = 12, Margin = new Thickness(0, 4, 0, 4) };
            codeRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            codeRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });

            var codeText = new TextBlock
            {
                FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Cascadia Mono, Consolas"),
                FontSize = 32,
                FontWeight = Microsoft.UI.Text.FontWeights.Bold,
                Foreground = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["AccentTextFillColorPrimaryBrush"],
                VerticalAlignment = VerticalAlignment.Center
            };
            var copyTotpBtn = new Button
            {
                Content = "复制动态码",
                Style = (Style)Application.Current.Resources["AccentButtonStyle"],
                VerticalAlignment = VerticalAlignment.Center,
                Padding = new Thickness(14, 6, 14, 6)
            };

            void UpdateTotpDisplay()
            {
                if (TotpHelper.TryGenerateTotp(revealed.TwoFactor, out var currentOtp, out var remSec, out var errMsg))
                {
                    codeText.Text = currentOtp;
                    timerText.Text = $"有效期剩余 {remSec}s";
                    if (remSec <= 5)
                    {
                        timerText.Foreground = new Microsoft.UI.Xaml.Media.SolidColorBrush(Windows.UI.Color.FromArgb(255, 255, 82, 82));
                    }
                    else
                    {
                        timerText.Foreground = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["TextFillColorSecondaryBrush"];
                    }
                }
                else
                {
                    codeText.Text = "------";
                    timerText.Text = errMsg;
                    timerText.Foreground = new Microsoft.UI.Xaml.Media.SolidColorBrush(Windows.UI.Color.FromArgb(255, 255, 82, 82));
                }
            }

            UpdateTotpDisplay();

            copyTotpBtn.Click += async (_, _) =>
            {
                if (codeText.Text != "------" && !string.IsNullOrEmpty(codeText.Text))
                {
                    try
                    {
                        using var copyCts = RequestTimeout();
                        await _client.WriteClipboardAsync(codeText.Text, paste: false, 0, copyCts.Token);
                        copyTotpBtn.Content = "已复制!";
                        var restoreTimer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(2) };
                        restoreTimer.Tick += (_, _) =>
                        {
                            copyTotpBtn.Content = "复制动态码";
                            restoreTimer.Stop();
                        };
                        restoreTimer.Start();
                    }
                    catch (Exception ex)
                    {
                        StatusText.Text = CleanError(ex.Message);
                    }
                }
            };

            Grid.SetColumn(codeText, 0);
            Grid.SetColumn(copyTotpBtn, 1);
            codeRow.Children.Add(codeText);
            codeRow.Children.Add(copyTotpBtn);
            totpStack.Children.Add(codeRow);

            var twoFactorSecretBox = ReadonlyTextBox(revealed.TwoFactor);
            totpStack.Children.Add(FieldWithCopy("2FA 密钥 (Base32)", twoFactorSecretBox, async () => await RunAgentActionAsync(async token => await _client.CopyAsync(record.Id, "two_factor", token))));

            totpCard.Child = totpStack;
            panel.Children.Add(totpCard);

            totpTimer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(1) };
            totpTimer.Tick += (_, _) => UpdateTotpDisplay();
            totpTimer.Start();
        }

        var remarkBox = new TextBox
        {
            AcceptsReturn = true,
            MinHeight = 70,
            TextWrapping = TextWrapping.Wrap,
            Text = revealed.Remark
        };
        panel.Children.Add(new TextBlock { Text = "备注", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        panel.Children.Add(remarkBox);

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = "查看",
            Content = new ScrollViewer { Content = panel, MaxHeight = 520 },
            PrimaryButtonText = "确定",
            SecondaryButtonText = "编辑",
            CloseButtonText = "关闭",
            DefaultButton = ContentDialogButton.Primary
        };
        dialog.Closed += (_, _) =>
        {
            totpTimer?.Stop();
        };
        dialog.Closing += async (_, args) =>
        {
            totpTimer?.Stop();
            var normalizedRemark = NormalizeMultilineText(remarkBox.Text);
            if (normalizedRemark == NormalizeMultilineText(revealed.Remark))
            {
                return;
            }
            var deferral = args.GetDeferral();
            try
            {
                SuppressRecordRefresh(record.Id);
                using var saveCts = RequestTimeout();
                await _client.UpdateRemarkAsync(record.Id, normalizedRemark, saveCts.Token);
            }
            catch (Exception ex)
            {
                StatusText.Text = CleanError(ex.Message);
            }
            finally
            {
                deferral.Complete();
            }
        };
        var result = await dialog.ShowAsync();
        if (result == ContentDialogResult.Secondary)
        {
            await EditRecordAsync(record);
        }
        else
        {
            await LoadRecordsAsync();
        }
    }

    private static FrameworkElement FieldWithCopy(string label, TextBox valueBox, Func<Task> copy)
    {
        var grid = new Grid { ColumnSpacing = 8 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var stack = new StackPanel { Spacing = 4 };
        stack.Children.Add(new TextBlock { Text = label, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        stack.Children.Add(valueBox);
        var button = new Button { Content = "复制", MinWidth = 64, VerticalAlignment = VerticalAlignment.Bottom };
        button.Click += async (_, _) => await copy();
        Grid.SetColumn(button, 1);
        grid.Children.Add(stack);
        grid.Children.Add(button);
        return grid;
    }

    private static TextBox ReadonlyTextBox(string text, bool multiline = false)
    {
        return new TextBox
        {
            AcceptsReturn = multiline,
            TextWrapping = multiline ? TextWrapping.Wrap : TextWrapping.NoWrap,
            IsReadOnly = true,
            FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Cascadia Mono, Consolas"),
            Text = text
        };
    }

    private static string NormalizeMultilineText(string? text)
    {
        if (string.IsNullOrEmpty(text))
        {
            return string.Empty;
        }
        return text.Replace("\r\n", "\n").Replace("\r", "\n").Replace("\n", Environment.NewLine);
    }

    private static string FormatSingleLineSummary(string? text, int maxLength)
    {
        if (string.IsNullOrEmpty(text))
        {
            return string.Empty;
        }
        var singleLine = text.Replace("\r\n", " ").Replace("\r", " ").Replace("\n", " ");
        return singleLine.Length <= maxLength ? singleLine : singleLine[..maxLength] + "...";
    }

    private async Task<SensitiveInput?> PromptSensitiveAsync(string title, string valueLabel, string defaultValue, bool allowGenerate)
    {
        var valueBox = new TextBox
        {
            Text = defaultValue,
            FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Cascadia Mono, Consolas")
        };
        var remarkBox = new TextBox { PlaceholderText = "备注" };
        var panel = new StackPanel { Spacing = 10 };
        var header = new Grid { ColumnSpacing = 8 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.Children.Add(new TextBlock { Text = valueLabel, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, VerticalAlignment = VerticalAlignment.Center });
        if (allowGenerate)
        {
            var generate = new Button { Content = "生成密码" };
            generate.Click += (_, _) => valueBox.Text = GeneratePassword();
            Grid.SetColumn(generate, 1);
            header.Children.Add(generate);
        }
        panel.Children.Add(header);
        panel.Children.Add(valueBox);
        panel.Children.Add(new TextBlock { Text = "备注", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        panel.Children.Add(remarkBox);

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = title,
            Content = panel,
            PrimaryButtonText = "确定",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };
        var result = await dialog.ShowAsync();
        return result == ContentDialogResult.Primary
            ? new SensitiveInput(valueBox.Text, remarkBox.Text)
            : null;
    }

    private async Task ShowMessageAsync(string title, string message)
    {
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot,
            Title = title,
            Content = new TextBlock
            {
                Text = message,
                TextWrapping = TextWrapping.Wrap,
                FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Cascadia Mono, Consolas")
            },
            PrimaryButtonText = "确定",
            DefaultButton = ContentDialogButton.Primary
        };
        await dialog.ShowAsync();
    }

    private static string ShortId(string id)
    {
        return id.Length <= 22 ? id : id[..22] + "...";
    }

    private static string GeneratePassword()
    {
        const string upper = "ABCDEFGHJKLMNPQRSTUVWXYZ";
        const string lower = "abcdefghijkmnopqrstuvwxyz";
        const string digits = "23456789";
        const string symbols = "!@#$%^&*()-_=+[]{};:,.?";
        var classes = new[] { upper, lower, digits, symbols };
        var all = string.Concat(classes);
        var chars = new char[20];
        for (var i = 0; i < classes.Length; i++)
        {
            chars[i] = Pick(classes[i]);
        }
        for (var i = classes.Length; i < chars.Length; i++)
        {
            chars[i] = Pick(all);
        }
        for (var i = chars.Length - 1; i > 0; i--)
        {
            var j = RandomNumberGenerator.GetInt32(i + 1);
            (chars[i], chars[j]) = (chars[j], chars[i]);
        }
        return new string(chars);

        static char Pick(string alphabet)
        {
            return alphabet[RandomNumberGenerator.GetInt32(alphabet.Length)];
        }
    }

    #endregion
}

public sealed record SensitiveInput(string Value, string Remark);
