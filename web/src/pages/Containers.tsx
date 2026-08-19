import { useEffect, useState } from 'react';
import { RefreshCw, RotateCw, Play, Square, Pencil, CheckCheck, Box, ArrowUpCircle, Zap, Activity, Trash2, X, Search, MoreVertical } from 'lucide-react';
import { api, normalizeContainerOperation, type NormalizedContainerOperation } from '../services/api';
import { useWs } from '../hooks/useWs';
import { notify } from '../stores';
import { Empty, ActionSheet, type ActionSheetItem } from '../components/ui';
import { relTime } from '../lib/utils';

type Container = {
  node_id: string;
  container_id: string;
  name: string;
  display_name: string;
  image: string;
  image_id: string;
  state: string;
  status: string;
  created: number;
  update_type: string; // latest | tag | pinned | build | local | unmanaged
  has_update: number;  // -1 unknown, 0 digest matches, 1 remote tag content differs
  note: string;
  scanned_at?: number;
};

type ScanCoverageEntry = {
  node_id?: string;
  node_name?: string;
  reason?: string;
};

type ScanReport = {
  items: any[];
  coverage?: {
    total_nodes?: number;
    attempted?: number;
    succeeded?: number;
    failed?: ScanCoverageEntry[];
    skipped?: ScanCoverageEntry[];
  };
};

type ActionOutcome = {
  nodeId: string;
  result?: NormalizedContainerOperation;
  transportError?: string;
};

const SCAN_FRESH_SECONDS = 24 * 60 * 60;

const key = (c: Container) => `${c.node_id}::${c.container_id}`;

export function ContainersPage() {
  const [containers, setContainers] = useState<Container[]>([]);
  const [nodes, setNodes] = useState<any[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [nodeFilter, setNodeFilter] = useState('');
  const [search, setSearch] = useState('');
  const [busy, setBusy] = useState(false);
  const [scan, setScan] = useState<{ loading: boolean; report: ScanReport | null }>({ loading: false, report: null });
  const [confirm, setConfirm] = useState<{
    title: string;
    msg: string;
    confirmLabel: string;
    dangerous?: boolean;
    onYes: () => void;
  } | null>(null);

  const [sheet, setSheet] = useState<Container | null>(null);

  const load = () => {
    api.containers.list().then((r: any) => setContainers(Array.isArray(r) ? r : []));
    api.nodes.list().then((r: any) => setNodes(Array.isArray(r) ? r : []));
  };
  useEffect(() => {
    load();
  }, []);

  useWs('container.inventory', () => load());

  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.name || id.slice(0, 8);
  const nodeOnline = (id: string) => {
    const n = nodes.find((x) => x.id === id);
    return !!n && (n.online || n.status === 'online');
  };

  const isAutoUpdatable = (t: string) => t === 'latest' || t === 'tag';
  const isRegistryScannable = (t: string) => isAutoUpdatable(t) || t === 'unmanaged';
  const scanIsFresh = (c: Pick<Container, 'scanned_at'>) => {
    if (!c.scanned_at) return false;
    const age = Date.now() / 1000 - c.scanned_at;
    return age >= -300 && age <= SCAN_FRESH_SECONDS;
  };
  const isAvailableUpdate = (c: Container) =>
    isAutoUpdatable(c.update_type) && c.state === 'running' && nodeOnline(c.node_id) && c.has_update === 1 && scanIsFresh(c);
  const q = search.trim().toLowerCase();
  const supportedContainers = containers.filter((c) => isAutoUpdatable(c.update_type));
  const visible = supportedContainers.filter((c) => {
    if (nodeFilter && c.node_id !== nodeFilter) return false;
    if (q) {
      const hay = `${c.name}\n${c.display_name}\n${c.image}\n${nodeName(c.node_id)}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
  const visibleKeys = new Set(visible.map(key));
  const selectedItems = visible.filter((c) => selected.has(key(c)));
  const selectedUpdateCandidates = selectedItems.filter(isAvailableUpdate);
  const updateCandidates = supportedContainers.filter(isAvailableUpdate);

  // A selection belongs to the current view. Changing node, search, or
  // inventory cannot leave hidden containers queued for a later action.
  useEffect(() => {
    setSelected((prev) => {
      const next = new Set([...prev].filter((k) => visibleKeys.has(k)));
      if (next.size === prev.size && [...next].every((k) => prev.has(k))) return prev;
      return next;
    });
    // `visibleKeys` is derived from these values and intentionally omitted.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containers, nodeFilter, search]);

  const toggle = (c: Container) =>
    setSelected((prev) => {
      const n = new Set(prev);
      n.has(key(c)) ? n.delete(key(c)) : n.add(key(c));
      return n;
    });
  const selectAll = () => setSelected(new Set(visible.map(key)));
  const clearAll = () => setSelected(new Set());

  const errorText = (e: any) =>
    String(e?.response?.data?.error || e?.response?.data?.err || e?.message || '请求失败');
  const shortList = (items: string[]) => {
    const shown = items.slice(0, 6);
    return `${shown.join(', ')}${items.length > shown.length ? ` 等 ${items.length} 个` : ''}`;
  };
  const summarize = (outcomes: ActionOutcome[], action: string) => {
    const updated: string[] = [];
    const unchanged: string[] = [];
    const skipped: string[] = [];
    const failed: string[] = [];
    outcomes.forEach((outcome) => {
      if (outcome.transportError) {
        failed.push(`${nodeName(outcome.nodeId)}（${outcome.transportError}）`);
        return;
      }
      const r = outcome.result;
      if (!r) return;
      updated.push(...r.updated);
      unchanged.push(...r.unchanged);
      skipped.push(...r.skipped.map((v) => v.reason ? `${v.name}（${v.reason}）` : v.name));
      failed.push(...r.failed.map((v) => {
        const prefix = outcomes.length > 1 ? `${nodeName(outcome.nodeId)}/` : '';
        return `${prefix}${v.name}${v.reason ? `（${v.reason}）` : ''}`;
      }));
    });
    const parts: string[] = [];
    if (updated.length) parts.push(`${action === 'update' || action === 'upgrade' ? '已更新' : '成功'} ${updated.length}：${shortList(updated)}`);
    if (unchanged.length) parts.push(`未变化 ${unchanged.length}：${shortList(unchanged)}`);
    if (skipped.length) parts.push(`已跳过 ${skipped.length}：${shortList(skipped)}`);
    if (failed.length) parts.push(`失败 ${failed.length}：${shortList(failed)}`);
    return {
      msg: parts.length ? parts.join('；') : '无变化',
      failed: failed.length,
      completed: updated.length + unchanged.length + skipped.length,
    };
  };

  const executeAction = async (items: Container[], action: string): Promise<ActionOutcome[]> => {
    const byNode = new Map<string, string[]>();
    items.forEach((c) => {
      if (!byNode.has(c.node_id)) byNode.set(c.node_id, []);
      byNode.get(c.node_id)!.push(c.container_id);
    });
    const entries = [...byNode.entries()];
    const settled = await Promise.allSettled(
      entries.map(([nodeId, ids]) => api.containers.action(nodeId, ids, action)),
    );
    return settled.map((item, i) => item.status === 'fulfilled'
      ? { nodeId: entries[i][0], result: normalizeContainerOperation(item.value) }
      : { nodeId: entries[i][0], transportError: errorText(item.reason) });
  };

  // Batch actions are scoped to the current filtered view. Update additionally
  // requires a fresh positive scan and never includes build/local containers.
  const act = async (action: string, explicitTargets?: Container[]) => {
    const targets = explicitTargets || (action === 'update' ? selectedUpdateCandidates : selectedItems);
    if (targets.length === 0) {
      return notify(action === 'update' ? '选中项中没有经新鲜检测确认可更新的运行中容器' : '请先选择容器', 'error');
    }
    setBusy(true);
    try {
      const outcomes = await executeAction(targets, action);
      const { msg, failed, completed } = summarize(outcomes, action);
      notify(`${labelOf(action)}：${msg}`, failed ? (completed ? 'warning' : 'error') : 'success');
      setSelected(new Set());
      setTimeout(load, 800); // give the agent a moment to new state
    } finally {
      setBusy(false);
    }
  };

  const updateAvailable = async (items: Container[]) => {
    if (items.length === 0) return notify('没有经新鲜检测确认可更新的运行中容器', 'error');
    setBusy(true);
    try {
      const outcomes = await executeAction(items, 'update');
      const { msg, failed, completed } = summarize(outcomes, 'update');
      notify(`更新可用（${items.length} 个）：${msg}`, failed ? (completed ? 'warning' : 'error') : 'success');
      setTimeout(load, 1200);
    } finally {
      setBusy(false);
    }
  };

  // single-container action (upgrade)
  const rowAct = async (c: Container, action: string, newImage?: string) => {
    setBusy(true);
    try {
      const r: any = await api.containers.action(c.node_id, [c.container_id], action, '', newImage);
      const { msg, failed } = summarize([{ nodeId: c.node_id, result: normalizeContainerOperation(r) }], action);
      notify(`${labelOf(action)} ${c.name}：${msg}`, failed ? 'error' : 'success');
      setTimeout(load, 1000);
    } catch (e: any) {
      notify(errorText(e), 'error');
    } finally {
      setBusy(false);
    }
  };

  const removeContainer = (c: Container) => {
    setConfirm({
      title: '确认删除',
      msg: `确认删除容器「${c.name}」？将执行 docker rm -f（不可恢复）`,
      confirmLabel: '确认删除',
      dangerous: true,
      onYes: () => rowAct(c, 'delete'),
    });
  };
  const deleteSelected = () => {
    if (selectedItems.length === 0) return notify('请先选择容器', 'error');
    setConfirm({
      title: '确认删除',
      msg: `确认删除当前视图中选中的 ${selectedItems.length} 个容器？将执行 docker rm -f（不可恢复）`,
      confirmLabel: '确认删除',
      dangerous: true,
      onYes: () => act('delete'),
    });
  };

  const upgrade = (c: Container) => {
    const ni = window.prompt(
      `换镜像 / 升级「${c.name}」\n输入新的 image:tag(将改写节点 compose 并重建,原文件备份为 .bak):`,
      c.image
    );
    if (ni === null || !ni.trim()) return;
    rowAct(c, 'upgrade', ni.trim());
  };

  const rename = async (c: Container) => {
    const name = window.prompt(`设置「${c.name}」的显示名（留空清除）`, c.display_name || '');
    if (name === null) return;
    try {
      await api.containers.setName(c.node_id, c.name, name.trim());
      notify('已保存', 'success');
      load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  const doScan = async () => {
    setScan({ loading: true, report: null });
    try {
      const r: any = await api.containers.scanUpdates();
      const report: ScanReport = Array.isArray(r)
        ? { items: r }
        : { items: Array.isArray(r?.items) ? r.items : [], coverage: r?.coverage };
      setScan({ loading: false, report });
      load(); // refresh the list so the per-row has_update badge updates
    } catch (e: any) {
      notify(errorText(e) || '体检失败', 'error');
      setScan({ loading: false, report: null });
    }
  };

  const scanItems = (scan.report?.items || []).filter((item) => isAutoUpdatable(item.update_type));

  return (
    <div>
      <div className="spread" style={{ marginBottom: 14 }}>
        <div>
          <h1 className="page-title">容器</h1>
          <p className="page-subtitle" style={{ marginBottom: 0 }}>
            仅展示支持安全拉取并重建更新的 Compose Registry 容器
          </p>
        </div>
        <div className="page-actions" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
          <select className="select" style={{ width: 'auto' }} value={nodeFilter} onChange={(e) => setNodeFilter(e.target.value)}>
            <option value="">全部节点</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>{n.name}</option>
            ))}
          </select>
          <button className="btn ghost" onClick={load}><RefreshCw size={15} /> 刷新</button>
          <div className="row" style={{ gap: 6, marginLeft: 'auto', alignItems: 'center', flex: '0 1 240px', minWidth: 140 }}>
            <Search size={14} color="var(--text-tertiary)" />
            <input
              className="input"
              style={{ flex: 1, minWidth: 0 }}
              placeholder="搜索容器名 / 镜像 / 节点…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            {search && (
              <button className="icon-btn" title="清空" onClick={() => setSearch('')}>
                <X size={14} />
              </button>
            )}
          </div>
        </div>
      </div>

      {/* toolbar */}
      <div className="card" style={{ padding: '10px 14px', marginBottom: 12 }}>
        <div className="selection-bar" style={{ justifyContent: 'space-between' }}>
          <div className="page-actions" style={{ gap: 6 }}>
            <button className="btn sm ghost" onClick={selectAll}><CheckCheck size={13} /> 全选</button>
            <button className="btn sm ghost" onClick={clearAll}>清空</button>
            <span style={{ fontSize: 12, color: 'var(--text-tertiary)', alignSelf: 'center' }}>
              已选 {selectedItems.length}
            </span>
          </div>
          <div className="page-actions" style={{ gap: 6 }}>
            <button className="btn sm" disabled={scan.loading || busy} onClick={doScan} title="只读检测 registry 镜像内容是否变化，不拉取或重建容器">
              <Activity size={13} /> {scan.loading ? '体检中…' : '体检更新'}
            </button>
            <button
              className="btn sm primary"
              disabled={busy || scan.loading || updateCandidates.length === 0}
              onClick={() => {
                const items = [...updateCandidates];
                setConfirm({
                  title: '确认更新可用镜像',
                  msg: `将更新 ${items.length} 个运行中容器。仅包含在线节点上最近 24 小时检测为“镜像内容有变化”的 Compose registry 镜像；离线节点、固定摘要、非 Compose、本地构建、停止中、未检测或检测过期的容器不会执行。`,
                  confirmLabel: `更新 ${items.length} 个`,
                  onYes: () => updateAvailable(items),
                });
              }}
              title={updateCandidates.length ? '更新在线节点上最近 24 小时检测确认镜像内容有变化的运行中容器' : '请先体检；仅在线节点上有新鲜检测结果且确认镜像内容有变化的运行中容器可更新'}
            >
              <Zap size={13} /> 更新可用 ({updateCandidates.length})
            </button>
            <button
              className="btn sm"
              disabled={busy || scan.loading || selectedUpdateCandidates.length === 0}
              onClick={() => {
                const items = [...selectedUpdateCandidates];
                setConfirm({
                  title: '确认更新选中镜像',
                  msg: `将更新当前选中的 ${items.length} 个运行中容器。仅包含在线节点上最近 24 小时检测到镜像内容变化的 Compose registry 容器，非 Compose 容器不会执行。`,
                  confirmLabel: `更新 ${items.length} 个`,
                  onYes: () => act('update', items),
                });
              }}
              title="只更新选中且最近 24 小时检测确认镜像内容有变化的运行中 registry 容器"
            >
              <RefreshCw size={13} /> 更新选中可用 ({selectedUpdateCandidates.length})
            </button>
            <button className="btn sm" disabled={busy || selectedItems.length === 0} onClick={() => act('restart')}><RotateCw size={13} /> 重启</button>
            <button className="btn sm" disabled={busy || selectedItems.length === 0} onClick={() => act('start')}><Play size={13} /> 启动</button>
            <button className="btn sm" disabled={busy || selectedItems.length === 0} onClick={() => act('stop')}><Square size={13} /> 停止</button>
            <button className="btn sm" disabled={busy || selectedItems.length === 0} onClick={deleteSelected} title="删除当前视图选中的容器（docker rm -f，不可恢复）"><Trash2 size={13} /> 删除选中</button>
          </div>
        </div>
      </div>

      <div className="card">
        {supportedContainers.length === 0 ? (
          <Empty text="暂无支持更新的容器" />
        ) : visible.length === 0 ? (
          <Empty text="无匹配容器" />
        ) : (
          <>
          <div className="desktop-only">
          <div className="scroll-x"><table className="tbl">
            <thead>
              <tr>
                <th style={{ width: 30 }}></th>
                <th>显示名 / 容器名</th>
                <th>节点</th>
                <th>镜像（版本）</th>
                <th>状态</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {visible.map((c) => {
                const sel = selected.has(key(c));
                return (
                  <tr key={key(c)} style={{ opacity: c.state === 'running' ? 1 : 0.6 }}>
                    <td>
                      <input type="checkbox" checked={sel} onChange={() => toggle(c)} />
                    </td>
                    <td>
                      <div className="row" style={{ gap: 6 }}>
                        <Box size={14} color="var(--text-tertiary)" />
                        <span>
                          {c.display_name ? (
                            <>
                              <strong>{c.display_name}</strong>{' '}
                              <span className="mono" style={{ color: 'var(--text-tertiary)' }}>{c.name}</span>
                            </>
                          ) : (
                            <strong className="mono">{c.name}</strong>
                          )}
                        </span>
                      </div>
                    </td>
                    <td>
                      {nodeName(c.node_id)}
                      {!nodeOnline(c.node_id) && <span style={{ color: 'var(--text-tertiary)' }}> · 离线</span>}
                    </td>
                    <td>
                      <div className="mono" style={{ fontSize: 12 }}>{c.image || '—'}</div>
                      <div className="row" style={{ gap: 6, marginTop: 2, alignItems: 'center', flexWrap: 'wrap' }}>
                        {c.image_id && (
                          <span className="mono" style={{ fontSize: 11, color: 'var(--text-tertiary)' }} title={c.image_id}>
                            {c.image_id.replace(/^sha256:/, '').slice(0, 12)}
                          </span>
                        )}
                        <UpdateBadge t={c.update_type} />
                        {isRegistryScannable(c.update_type) && (
                          <ContainerScanBadge container={c} fresh={scanIsFresh(c)} auto={isAutoUpdatable(c.update_type)} />
                        )}
                        {c.scanned_at ? (
                          <span
                            style={{ fontSize: 11, color: 'var(--text-tertiary)' }}
                            title={new Date(c.scanned_at * 1000).toLocaleString()}
                          >
                            检测于 {relTime(c.scanned_at)}
                          </span>
                        ) : isRegistryScannable(c.update_type) ? (
                          <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>尚未检测</span>
                        ) : null}
                      </div>
                    </td>
                    <td>
                      <StateBadge state={c.state} status={c.status} />
                    </td>
                    <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                      {c.update_type === 'tag' && (
                        <button className="icon-btn" title="换镜像 / 升级 tag（改写 compose）" disabled={busy} onClick={() => upgrade(c)}>
                          <ArrowUpCircle size={14} />
                        </button>
                      )}
                      <button className="icon-btn" title="设置显示名" onClick={() => rename(c)}>
                        <Pencil size={14} />
                      </button>
                      <button className="icon-btn" title="删除容器（docker rm -f，不可恢复）" onClick={() => removeContainer(c)}>
                        <Trash2 size={14} />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table></div>
          </div>

          <div className="mobile-only m-list">
            {visible.map((c) => {
              const sel = selected.has(key(c));
              return (
                <div key={key(c)} className="m-item" style={{ opacity: c.state === 'running' ? 1 : 0.6 }}>
                  <div className="m-item-check">
                    <input type="checkbox" checked={sel} onChange={() => toggle(c)} />
                  </div>
                  <div className="m-item-main" onClick={() => setSheet(c)} role="button" tabIndex={0}
                    onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSheet(c); } }}>
                    <div className="m-item-title">
                      {c.display_name || c.name}
                    </div>
                    <div className="m-item-sub">
                      {c.display_name ? <span className="mono">{c.name}</span> : null}
                      {c.display_name ? ' · ' : null}
                      {nodeName(c.node_id)}
                      {!nodeOnline(c.node_id) ? ' · 离线' : ''}
                    </div>
                    <div className="m-item-meta">
                      <StateBadge state={c.state} status={c.status} />
                      <UpdateBadge t={c.update_type} />
                      {isRegistryScannable(c.update_type) && (
                        <ContainerScanBadge container={c} fresh={scanIsFresh(c)} auto={isAutoUpdatable(c.update_type)} />
                      )}
                    </div>
                  </div>
                  <button className="icon-btn m-item-more" title="更多" onClick={() => setSheet(c)}>
                    <MoreVertical size={18} />
                  </button>
                </div>
              );
            })}
          </div>
          </>
        )}
      </div>

      <ActionSheet
        open={!!sheet}
        onClose={() => setSheet(null)}
        title={sheet ? (sheet.display_name || sheet.name) : undefined}
        subtitle={sheet ? (
          <>
            {sheet.display_name ? <span className="mono">{sheet.name}</span> : null}
            {sheet.display_name ? ' · ' : null}
            {nodeName(sheet.node_id)}
            {!nodeOnline(sheet.node_id) ? ' · 离线' : ''}
          </>
        ) : undefined}
        actions={sheet ? ([
          sheet.update_type === 'tag' && {
            key: 'upgrade',
            label: '换镜像 / 升级 tag',
            icon: <ArrowUpCircle size={18} />,
            disabled: busy,
            onClick: () => upgrade(sheet),
          },
          {
            key: 'rename',
            label: '设置显示名',
            icon: <Pencil size={18} />,
            onClick: () => rename(sheet),
          },
          {
            key: 'delete',
            label: '删除容器',
            icon: <Trash2 size={18} />,
            danger: true,
            onClick: () => removeContainer(sheet),
          },
        ].filter(Boolean) as ActionSheetItem[]) : []}
      >
        {sheet && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div className="kv-row">
              <span>状态</span>
              <StateBadge state={sheet.state} status={sheet.status} />
            </div>
            <div className="kv-row">
              <span>镜像</span>
              <span className="mono break-anywhere" style={{ textAlign: 'right' }}>{sheet.image || '—'}</span>
            </div>
            {sheet.image_id && (
              <div className="kv-row">
                <span>摘要</span>
                <span className="mono">{sheet.image_id.replace(/^sha256:/, '').slice(0, 12)}</span>
              </div>
            )}
            <div className="kv-row">
              <span>类型</span>
              <UpdateBadge t={sheet.update_type} />
            </div>
            {isRegistryScannable(sheet.update_type) && (
              <div className="kv-row">
                <span>检测</span>
                <span style={{ display: 'flex', flexWrap: 'wrap', gap: 6, justifyContent: 'flex-end' }}>
                  <ContainerScanBadge container={sheet} fresh={scanIsFresh(sheet)} auto={isAutoUpdatable(sheet.update_type)} />
                  {sheet.scanned_at ? (
                    <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>于 {relTime(sheet.scanned_at)}</span>
                  ) : (
                    <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>尚未检测</span>
                  )}
                </span>
              </div>
            )}
          </div>
        )}
      </ActionSheet>

      {confirm && (
        <ConfirmDialog
          title={confirm.title}
          message={confirm.msg}
          confirmLabel={confirm.confirmLabel}
          dangerous={confirm.dangerous}
          onConfirm={() => { const f = confirm.onYes; setConfirm(null); f(); }}
          onCancel={() => setConfirm(null)}
        />
      )}
      {scan.report && (
        <div
          onClick={() => setScan({ loading: false, report: null })}
          style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 }}
        >
          <div onClick={(e) => e.stopPropagation()} className="card" style={{ width: '92%', maxWidth: 980, maxHeight: '86vh', overflow: 'auto', padding: 20 }}>
            <div className="spread" style={{ marginBottom: 12 }}>
              <h2 className="page-title" style={{ fontSize: 18, marginBottom: 0 }}>容器更新体检（{scanItems.length} 个容器）</h2>
              <button className="btn ghost sm" onClick={() => setScan({ loading: false, report: null })}>关闭</button>
            </div>
            {scan.report.coverage ? (
              <div style={{ padding: '10px 12px', marginBottom: 12, border: '1px solid var(--border-color)', borderRadius: 6 }}>
                <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
                  <strong style={{ fontSize: 13 }}>节点覆盖</strong>
                  <span className="badge success">成功 {scan.report.coverage.succeeded ?? 0} / 尝试 {scan.report.coverage.attempted ?? 0}</span>
                  <span className="badge muted">总节点 {scan.report.coverage.total_nodes ?? nodes.length}</span>
                  {!!scan.report.coverage.failed?.length && <span className="badge error">失败 {scan.report.coverage.failed.length}</span>}
                  {!!scan.report.coverage.skipped?.length && <span className="badge warning">跳过 {scan.report.coverage.skipped.length}</span>}
                </div>
                {!!scan.report.coverage.failed?.length && (
                  <div style={{ marginTop: 8, fontSize: 12, color: 'var(--danger, #c62828)', lineHeight: 1.6 }}>
                    {scan.report.coverage.failed.map((entry, i) => (
                      <div key={`${entry.node_id || entry.node_name || 'failed'}-${i}`}>
                        失败：{entry.node_name || (entry.node_id ? nodeName(entry.node_id) : '未知节点')} — {entry.reason || '未返回原因'}
                      </div>
                    ))}
                  </div>
                )}
                {!!scan.report.coverage.skipped?.length && (
                  <div style={{ marginTop: 6, fontSize: 12, color: 'var(--text-secondary)', lineHeight: 1.6 }}>
                    {scan.report.coverage.skipped.map((entry, i) => (
                      <div key={`${entry.node_id || entry.node_name || 'skipped'}-${i}`}>
                        跳过：{entry.node_name || (entry.node_id ? nodeName(entry.node_id) : '未知节点')} — {entry.reason || '未返回原因'}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ) : (
              <div className="badge warning" style={{ marginBottom: 12 }}>
                旧版接口未提供节点覆盖统计，结果可能不包含离线或不支持检测的节点
              </div>
            )}
            {scanItems.length === 0 ? (
              <Empty text="本次没有返回容器检测结果" />
            ) : (
              <div className="scroll-x"><table className="tbl">
                <thead>
                  <tr>
                    <th>节点</th><th>容器</th><th>运行状态</th><th>类型</th><th>更新状态</th><th>镜像</th><th>说明</th>
                  </tr>
                </thead>
                <tbody>
                  {scanItems.map((it: any, i: number) => (
                    <tr key={`${it.node_id || ''}-${it.name || i}`}>
                      <td style={{ fontSize: 12 }}>{nodeName(it.node_id)}</td>
                      <td className="mono" style={{ fontSize: 12 }}>{it.name}</td>
                      <td>{it.state === 'running' ? <span className="badge success">运行中</span> : <span className="badge muted">{it.state || '未知'}</span>}</td>
                      <td><UpdateBadge t={it.update_type} /></td>
                      <td><ScanStatus it={it} /></td>
                      <td className="mono" style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>{it.image}</td>
                      <td style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>{it.note || (it.has_update < 0 ? '未返回原因' : '—')}</td>
                    </tr>
                  ))}
                </tbody>
              </table></div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function ConfirmDialog({
  title,
  message,
  confirmLabel,
  dangerous,
  onConfirm,
  onCancel,
}: {
  title: string;
  message: string;
  confirmLabel: string;
  dangerous?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <div onClick={onCancel} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1100 }}>
      <div onClick={(e) => e.stopPropagation()} className="card" style={{ width: '90%', maxWidth: 420, padding: 22 }}>
        <h3 style={{ margin: '0 0 8px' }}>{title}</h3>
        <p style={{ color: 'var(--text-secondary)', margin: '0 0 18px', lineHeight: 1.6 }}>{message}</p>
        <div className="row" style={{ justifyContent: 'flex-end', gap: 8 }}>
          <button className="btn" onClick={onCancel}>取消</button>
          <button
            className={dangerous ? 'btn' : 'btn primary'}
            style={dangerous ? { background: 'var(--danger, #c62828)', color: '#fff' } : undefined}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

function ScanStatus({ it }: { it: any }) {
  if (it.update_type === 'pinned') return <span className="badge muted">固定摘要，不自动更新</span>;
  if (it.update_type === 'build') return <span className="badge muted">源码构建，不拉取</span>;
  if (it.update_type === 'local') return <span className="badge muted">本地镜像，不拉取</span>;
  if (it.has_update === 1) return <span className="badge warning">{it.update_type === 'unmanaged' ? '镜像内容有变化，需手动更新' : '镜像内容有变化'}</span>;
  if (it.has_update === 0) return <span className="badge success">镜像内容一致</span>;
  if (it.convertible) return <span className="badge warning">可转 registry 候选</span>;
  return <span className="badge muted" title={it.note || '未返回原因'}>未知：{it.note || '未返回原因'}</span>;
}

function ContainerScanBadge({ container, fresh, auto }: { container: Container; fresh: boolean; auto: boolean }) {
  if (!container.scanned_at) return null;
  if (!fresh) {
    const previous = container.has_update === 1 ? '上次检测到内容变化' : container.has_update === 0 ? '上次内容一致' : '上次未知';
    return <span className="badge warning" title={`${previous}；超过 24 小时，更新前需重新检测`}>检测已过期</span>;
  }
  if (container.has_update === 1) return <span className="badge warning">{auto ? '可更新' : '镜像变化，需手动'}</span>;
  if (container.has_update === 0) return <span className="badge success">镜像一致</span>;
  return <span className="badge muted" title={container.note || '未返回原因'}>未知</span>;
}

function StateBadge({ state, status }: { state: string; status: string }) {
  const color =
    state === 'running' ? 'var(--success)' :
    state === 'exited' ? 'var(--text-tertiary)' :
    state === 'restarting' || state === 'paused' ? 'var(--warning)' : 'var(--danger)';
  return (
    <span className="row" style={{ gap: 6 }}>
      <span style={{ width: 8, height: 8, borderRadius: '50%', background: color, display: 'inline-block' }} />
      <span style={{ fontSize: 12 }}>{status || state}</span>
    </span>
  );
}

function UpdateBadge({ t }: { t: string }) {
  const map: Record<string, { label: string; color: string }> = {
    latest: { label: 'Registry latest', color: 'var(--success, #2e7d32)' },
    tag: { label: 'Registry 固定 tag', color: 'var(--success, #2e7d32)' },
    pinned: { label: '固定摘要', color: 'var(--text-secondary)' },
    build: { label: '源码构建', color: 'var(--warning, #ed6c02)' },
    local: { label: '本地镜像', color: 'var(--danger, #c62828)' },
    unmanaged: { label: '非 Compose', color: 'var(--text-secondary)' },
  };
  const m = map[t] || { label: t || '—', color: 'var(--text-tertiary)' };
  return (
    <span style={{ fontSize: 11, padding: '1px 6px', borderRadius: 8, color: m.color, border: `1px solid ${m.color}`, whiteSpace: 'nowrap' }}>
      {m.label}
    </span>
  );
}

function labelOf(action: string) {
  switch (action) {
    case 'update': return '更新镜像';
    case 'rebuild': return '源码更新';
    case 'upgrade': return '换镜像';
    case 'delete': return '删除';
    case 'restart': return '重启';
    case 'start': return '启动';
    case 'stop': return '停止';
    default: return action;
  }
}
