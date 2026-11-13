using System.Collections.ObjectModel;
using Microsoft.UI.Xaml.Data;
using Windows.Foundation;

namespace ClipSyncPro_WinUI;

/// <summary>
/// 剪贴板记录的增量加载集合：由 ListView 在接近底部时自动调用 <see cref="LoadMoreItemsAsync"/>，
/// 滚轮、触摸板、拖动滚动条、键盘翻页均走同一条路径，不再依赖手工挂接 ScrollViewer 事件。
/// </summary>
public sealed class IncrementalRecordCollection : ObservableCollection<ClipboardRecordView>, ISupportIncrementalLoading
{
    /// <summary>与本地 agent 列表接口的单页条数保持一致（internal/agent/ui.go 中 Limit: 10）。</summary>
    public const int PageSize = 10;

    private readonly Func<int, CancellationToken, Task<IReadOnlyList<ClipboardRecordView>>> _fetchPage;
    private int _offset;
    private int _generation;
    private bool _hasMore;

    public IncrementalRecordCollection(Func<int, CancellationToken, Task<IReadOnlyList<ClipboardRecordView>>> fetchPage)
    {
        _fetchPage = fetchPage;
    }

    /// <summary>客户端侧的二次过滤（例如“全部”视图不展示收藏项）。偏移量仍按服务端返回条数推进。</summary>
    public Func<ClipboardRecordView, bool>? Accept { get; set; }

    /// <summary>后续分页失败时的回调（在 UI 线程触发）。</summary>
    public Action<Exception>? LoadFailed { get; set; }

    public bool HasMoreItems => _hasMore;

    /// <summary>
    /// 作废所有进行中的分页请求，并暂停继续加载，直到 <see cref="ResetWith"/> 完成。
    /// 用于筛选条件变化或整体刷新开始时，避免旧条件下返回的分页数据混入新列表。
    /// </summary>
    public void CancelPending()
    {
        _generation++;
        _hasMore = false;
    }

    /// <summary>用新的第一页数据替换整个集合，并重新开启后续分页。</summary>
    public void ResetWith(IReadOnlyList<ClipboardRecordView> firstPage)
    {
        _generation++;
        Clear();
        foreach (var record in firstPage)
        {
            if (Accept?.Invoke(record) ?? true)
            {
                Add(record);
            }
        }
        _offset = firstPage.Count;
        _hasMore = firstPage.Count >= PageSize;
    }

    /// <summary>本地移除一条记录并返回原索引（未找到返回 -1），同时回退服务端偏移量，避免后续分页漏掉紧随其后的一条。</summary>
    public int RemoveTracked(ClipboardRecordView record)
    {
        var index = IndexOf(record);
        if (index < 0)
        {
            return -1;
        }
        RemoveAt(index);
        _offset = Math.Max(0, _offset - 1);
        return index;
    }

    /// <summary>本地回插一条记录（例如请求失败回滚），同时前移服务端偏移量。</summary>
    public void InsertTracked(int index, ClipboardRecordView record)
    {
        var safeIndex = Math.Clamp(index, 0, Count);
        Insert(safeIndex, record);
        _offset++;
    }

    public IAsyncOperation<LoadMoreItemsResult> LoadMoreItemsAsync(uint count)
    {
        return LoadMoreAsync().AsAsyncOperation();
    }

    private async Task<LoadMoreItemsResult> LoadMoreAsync()
    {
        if (!_hasMore)
        {
            return new LoadMoreItemsResult { Count = 0 };
        }

        var generation = _generation;
        try
        {
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(12));
            var page = await _fetchPage(_offset, cts.Token);
            if (generation != _generation)
            {
                return new LoadMoreItemsResult { Count = 0 };
            }

            uint added = 0;
            foreach (var record in page)
            {
                if (Accept?.Invoke(record) ?? true)
                {
                    Add(record);
                    added++;
                }
            }
            _offset += page.Count;
            _hasMore = page.Count >= PageSize;
            return new LoadMoreItemsResult { Count = added };
        }
        catch (Exception ex)
        {
            if (generation == _generation)
            {
                // 失败后停止自动重试，避免 ListView 在服务不可用时持续高频请求；下一次刷新会重新开启。
                _hasMore = false;
                LoadFailed?.Invoke(ex);
            }
            return new LoadMoreItemsResult { Count = 0 };
        }
    }
}
