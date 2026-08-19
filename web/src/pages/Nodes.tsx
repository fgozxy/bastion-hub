import { useEffect, useState } from 'react';
import { Plus, RefreshCw, Trash2, Pencil, DownloadCloud, Shield, ShieldOff, Activity } from 'lucide-react';
import { api } from '../services/api';
import { useWs } from '../hooks/useWs';
import { notify } from '../stores';
import { truncateIPv6 } from '../lib/utils';
import { CopyButton, Modal, StatusBadge, Empty } from '../components/ui';
import { NodeSelect } from '../components/NodePicker';

export function NodesPage() {
  const [nodes, setNodes] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [addOpen, setAddOpen] = useState(false);
  const [newName, setNewName] = useState('');
  const [installCmd, setInstallCmd] = useState('');
  const [editNode, setEditNode] = useState<any | null>(null);
  const [editName, setEditName] = useState('');
  const [editPort, setEditPort] = useState('22');
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchIds, setBatchIds] = useState<string[]>([]);
  const [batchRes, setBatchRes] = useState<any[] | null>(null);
  const [batchBusy, setBatchBusy] = useState(false);
  // Netdata install modal state
  const [netOpen, setNetOpen] = useState(false);
  const [netIds, setNetIds] = useState<string[]>([]);
  const [netBusy, setNetBusy] = useState(false);
  const [netRes, setNetRes] = useState<any[] | null>(null);
  const [netInstalled, setNetInstalled] = useState<string[]>([]);
  const [regenNode, setRegenNode] = useState<any | null>(null);
  const [updId, setUpdId] = useState<string | null>(null);
  // firewall modal state (single node)
  const [fwNode, setFwNode] = useState<any | null>(null);
  const [fwRes, setFwRes] = useState<any | null>(null);
  const [fwSel, setFwSel] = useState<Set<string>>(new Set());
  const [fwBusy, setFwBusy] = useState(false);
  const [fwAct, setFwAct] = useState(''); // 'enable' | 'disable' | 'allow' | 'deny'
  // Source allowlist for each selected node's configured SSH port + mesh 22022.
  const [meshOpen, setMeshOpen] = useState(false);
  const [meshEnabled, setMeshEnabled] = useState(false);
  const [meshIds, setMeshIds] = useState<string[]>([]);
  const [meshSources, setMeshSources] = useState('');
  const [meshBusy, setMeshBusy] = useState(false);
  const [meshLoading, setMeshLoading] = useState(false);
  const [meshRes, setMeshRes] = useState<any[] | null>(null);
  const [meshDefaults, setMeshDefaults] = useState<string[]>([]);

  const load = () => {
    setLoading(true);
    api.nodes
      .list()
      .then((r: any) => setNodes(Array.isArray(r) ? r : []))
      .finally(() => setLoading(false));
  };
  useEffect(() => {
    load();
  }, []);

  // live updates
  useWs('node.update', (d: any) => {
    setNodes((prev) => prev.map((n) => (n.id === d.id ? { ...n, ...d, status: d.status || n.status } : n)));
  });
  useWs('node.status', (d: any) => {
    setNodes((prev) => prev.map((n) => (n.id === d.id ? { ...n, status: d.status, online: d.status === 'online' } : n)));
  });

  const create = async () => {
    try {
      const r: any = await api.nodes.create(newName || 'New Node');
      setInstallCmd(r.install_cmd);
      setNewName('');
      setNodes((p) => [{ ...r, online: false }, ...p]);
      notify('节点已创建，复制命令到目标主机执行', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '创建失败', 'error');
    }
  };

  const regen = async (id: string) => {
    try {
      const r: any = await api.nodes.regenerate(id);
      setRegenNode({ id, cmd: r.install_cmd });
      notify('已重新生成安装命令', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  const saveName = async () => {
    if (!editNode) return;
    try {
      await api.nodes.rename(editNode.id, editName, editPort || '22');
      setNodes((p) => p.map((n) => (n.id === editNode.id ? { ...n, name: editName, ssh_port: editPort || '22' } : n)));
      setEditNode(null);
      notify('已保存', 'success');
    } catch {
      notify('保存失败', 'error');
    }
  };

  const remove = async (id: string) => {
    if (!confirm('确定删除该节点？')) return;
    await api.nodes.remove(id);
    setNodes((p) => p.filter((n) => n.id !== id));
    notify('已删除');
  };

  const updateAgent = async (n: any) => {
    setUpdId(n.id);
    try {
      const r: any = await api.nodes.updateAgent(n.id);
      notify(`已更新到最新版${r.version ? '（v' + r.version + '）' : ''}，Agent 正在重启`  , 'success');
      load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '更新失败', 'error');
    } finally {
      setUpdId(null);
    }
  };

  const runBatchUpdate = async (ids: string[]) => {
    if (ids.length === 0) return notify('请选择节点', 'error');
    setBatchBusy(true);
    setBatchRes(null);
    try {
      const r: any = await api.nodes.updateAgents(ids);
      setBatchRes(Array.isArray(r) ? r : []);
      const ok = (r as any[]).filter((x) => x.ok).length;
      notify(`完成：${ok}/${ids.length} 个节点已更新`, ok === ids.length ? 'success' : 'error');
      load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '批量更新失败', 'error');
    } finally {
      setBatchBusy(false);
    }
  };
  const doBatchUpdate = () => runBatchUpdate(batchIds);
  const batchUpdateAll = () => {
    const ids = nodes.filter((n) => n.online || n.status === 'online').map((n) => n.id);
    setBatchIds(ids);
    runBatchUpdate(ids);
  };

  // --- Netdata install (health backend) ---
  // openNet pulls current install status so the picker can badge already-
  // installed nodes and default the selection to the still-pending online ones.
  const openNet = async () => {
    setNetIds([]);
    setNetRes(null);
    setNetOpen(true);
    try {
      const st: any = await api.health.status();
      const arr = Array.isArray(st) ? st : [];
      const installed = arr.filter((n: any) => n.installed).map((n: any) => n.node_id);
      setNetInstalled(installed);
      // Default-select online nodes that are NOT yet installed.
      const installedSet = new Set(installed);
      setNetIds(nodes.filter((n) => (n.online || n.status === 'online') && !installedSet.has(n.id)).map((n) => n.id));
    } catch {
      setNetInstalled([]);
    }
  };
  const runNetInstall = async (ids: string[]) => {
    if (ids.length === 0) return notify('请选择节点', 'error');
    setNetBusy(true);
    setNetRes(null);
    try {
      const r: any = await api.health.install(ids);
      setNetRes(Array.isArray(r) ? r : []);
      const ok = (r as any[]).filter((x) => x.ok).length;
      notify(`完成：${ok}/${ids.length} 个节点已安装 Netdata`, ok === ids.length ? 'success' : 'error');
      // Refresh install status so badges stay accurate after a batch.
      try {
        const st: any = await api.health.status();
        setNetInstalled((Array.isArray(st) ? st : []).filter((n: any) => n.installed).map((n: any) => n.node_id));
      } catch { /* ignore */ }
      load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '安装失败', 'error');
    } finally {
      setNetBusy(false);
    }
  };
  const doNetInstall = () => runNetInstall(netIds);
  const netInstallAll = () => {
    // All ONLINE nodes that are NOT already installed (re-installing is
    // harmless but pointless, and a fresh boot per node is expensive).
    const installedSet = new Set(netInstalled);
    const ids = nodes.filter((n) => (n.online || n.status === 'online') && !installedSet.has(n.id)).map((n) => n.id);
    if (ids.length === 0) {
      notify('所有在线节点均已安装 Netdata', 'success');
      return;
    }
    setNetIds(ids);
    runNetInstall(ids);
  };

  // --- firewall (single node) ---
  const openFirewall = async (n: any) => {
    setFwNode(n); setFwRes(null); setFwSel(new Set()); setFwBusy(true);
    try {
      const r: any = await api.nodes.firewallStatus(n.id);
      setFwRes(r);
    } catch (e: any) {
      setFwRes({ error: e?.response?.data?.error || '查询失败' });
    } finally { setFwBusy(false); }
  };
  const closeFirewall = () => { setFwNode(null); setFwRes(null); setFwSel(new Set()); };
  const refreshFw = async () => {
    if (!fwNode) return;
    setFwBusy(true);
    try {
      const r: any = await api.nodes.firewallStatus(fwNode.id);
      setFwRes(r);
    } catch (e: any) { notify(e?.response?.data?.error || '查询失败', 'error'); }
    finally { setFwBusy(false); }
  };
  const toggleFw = async (action: 'enable' | 'disable') => {
    if (!fwNode) return;
    setFwAct(action);
    try {
      const r: any = await api.nodes.firewallToggle(fwNode.id, action);
      setFwRes(r);
      notify(`已${action === 'enable' ? '开启' : '关闭'}防火墙`, 'success');
    } catch (e: any) { notify(e?.response?.data?.error || '操作失败', 'error'); }
    finally { setFwAct(''); }
  };
  const portAction = async (action: 'allow' | 'deny') => {
    if (!fwNode || fwSel.size === 0) return notify('请先选择端口', 'error');
    const ports = Array.from(fwSel);
    setFwAct(action);
    try {
      const r: any = await api.nodes.firewallPorts(fwNode.id, ports, action);
      setFwRes(r);
      setFwSel(new Set());
      notify(`已${action === 'allow' ? '开放' : '关闭'} ${ports.length} 个端口`, 'success');
    } catch (e: any) { notify(e?.response?.data?.error || '操作失败', 'error'); }
    finally { setFwAct(''); }
  };
  const togglePortSel = (key: string) =>
    setFwSel((prev) => { const n = new Set(prev); n.has(key) ? n.delete(key) : n.add(key); return n; });

  // --- mesh SSH source restriction (multi-node, hot update) ---
  const openMeshAccess = async () => {
    setMeshOpen(true);
    setMeshLoading(true);
    setMeshRes(null);
    try {
      const r: any = await api.mesh.access();
      const cfg = r?.config || {};
      setMeshEnabled(!!cfg.enabled);
      const knownIds = new Set(nodes.map((n) => n.id));
      setMeshIds(Array.isArray(cfg.node_ids) ? cfg.node_ids.filter((id: string) => knownIds.has(id)) : []);
      setMeshSources(Array.isArray(cfg.source_cidrs) ? cfg.source_cidrs.join('\n') : '');
      setMeshDefaults(Array.isArray(r?.default_sources) ? r.default_sources : []);
    } catch (e: any) {
      notify(e?.response?.data?.error || '读取跳板连接限制失败', 'error');
      setMeshOpen(false);
    } finally {
      setMeshLoading(false);
    }
  };
  const applyMeshAccess = async () => {
    const sources = meshSources.split(/[\s,;]+/).map((x) => x.trim()).filter(Boolean);
    if (meshEnabled && meshIds.length === 0) return notify('请至少选择一个目标节点', 'error');
    if (meshEnabled && sources.length === 0) return notify('请至少填写一个允许来源 IP/CIDR', 'error');
    const selected = new Set(meshIds);
    const sshPorts = Array.from(new Set(nodes.filter((n) => selected.has(n.id)).map((n) => n.ssh_port || '22'))).sort();
    if (meshEnabled && !confirm(`即将限制 ${meshIds.length} 个节点的实际 SSH 端口（${sshPorts.join('、')}）及 22022，仅允许 ${sources.length} 项来源。确认热更新？`)) return;
    setMeshBusy(true);
    setMeshRes(null);
    try {
      const r: any = await api.mesh.putAccess({ enabled: meshEnabled, node_ids: meshIds, source_cidrs: sources });
      const cfg = r?.config || {};
      setMeshIds(Array.isArray(cfg.node_ids) ? cfg.node_ids : meshIds);
      setMeshSources(Array.isArray(cfg.source_cidrs) ? cfg.source_cidrs.join('\n') : meshSources);
      const results = Array.isArray(r?.results) ? r.results : [];
      setMeshRes(results);
      const failed = results.filter((x: any) => !x.ok && !x.pending).length;
      const pending = results.filter((x: any) => x.pending).length;
      notify(
        failed ? `配置已保存，${failed} 个在线节点应用失败` : `配置已保存并热更新${pending ? `，${pending} 个离线节点待应用` : ''}`,
        failed ? 'error' : 'success'
      );
    } catch (e: any) {
      notify(e?.response?.data?.error || '更新跳板连接限制失败', 'error');
    } finally {
      setMeshBusy(false);
    }
  };

  return (
    <div>
      <div className="spread" style={{ marginBottom: 18 }}>
        <div>
          <h1 className="page-title">节点</h1>
          <p className="page-subtitle" style={{ marginBottom: 0 }}>
            在目标主机执行安装命令即可加入面板（自动安装低占用 Agent）
          </p>
        </div>
        <div className="page-actions">
          <button className="btn ghost" onClick={load}>
            <RefreshCw size={15} /> 刷新
          </button>
          <button
            className="btn"
            onClick={() => { setBatchIds([]); setBatchRes(null); setBatchOpen(true); }}
            title="批量更新多个节点的 Agent 到最新版"
          >
            <DownloadCloud size={15} /> 批量更新 Agent
          </button>
          <button
            className="btn"
            onClick={openNet}
            title="批量安装 Netdata 监控（仅本地，不接入 Cloud）"
          >
            <Activity size={15} /> 加入Netdata
          </button>
          <button className="btn" onClick={openMeshAccess} title="限制各节点实际 SSH 端口及 22022 的允许来源 IP">
            <Shield size={15} /> 跳板连接限制
          </button>
          <button className="btn primary" onClick={() => setAddOpen(true)}>
            <Plus size={15} /> 添加节点
          </button>
        </div>
      </div>

      {loading ? (
        <Empty text="加载中…" />
      ) : nodes.length === 0 ? (
        <Empty text="还没有节点，点击「添加节点」生成安装命令" />
      ) : (
        <div className="auto-grid" style={{ '--grid-min': '400px' } as any}>
          {nodes.map((n) => (
            <div className="card" key={n.id} style={{ padding: 16, display: 'flex', flexDirection: 'column' }}>
              <div className="spread" style={{ marginBottom: 10 }}>
                <div className="row" style={{ minWidth: 0, flex: 1 }}>
                  <Flag code={n.country_code} name={n.country} />
                  <strong className="text-ellipsis" style={{ fontSize: 15 }}>{n.name}</strong>
                </div>
                <StatusBadge online={n.online || n.status === 'online'} />
              </div>

              <div style={{ display: 'grid', gap: 7, fontSize: 13, flex: 1, minWidth: 0 }}>
                <IPRow label="IPv4" value={n.ipv4} />
                <IPRow label="IPv6" value={n.ipv6 ? truncateIPv6(n.ipv6) : '—'} full={n.ipv6} />
                <div className="kv-row">
                  <span>主机名</span>
                  <span className="mono">{n.hostname || '—'}</span>
                </div>
                <div className="kv-row">
                  <span>系统 / 架构</span>
                  <span className="mono">
                    {n.os} / {n.arch}
                  </span>
                </div>
                <div className="kv-row">
                  <span>Agent</span>
                  <span className="mono">{n.agent_version || '—'}</span>
                </div>
              </div>

              <div className="card-actions">
                <button
                  className="btn sm"
                  onClick={() => {
                    setEditNode(n);
                    setEditName(n.name);
                    setEditPort(n.ssh_port || '22');
                  }}
                >
                  <Pencil size={13} /> 改名
                </button>
                <button
                  className="btn sm"
                  onClick={() => openFirewall(n)}
                  disabled={!(n.online || n.status === 'online')}
                  title="查看 / 开关本机防火墙端口"
                >
                  <Shield size={13} /> 防火墙
                </button>
                <button
                  className="btn sm"
                  onClick={() => updateAgent(n)}
                  disabled={updId === n.id || !(n.online || n.status === 'online')}
                  title={n.online || n.status === 'online' ? '更新本机 Agent 到最新版' : '节点离线'}
                >
                  <DownloadCloud size={13} /> {updId === n.id ? '更新中…' : '更新'}
                </button>
                <button className="btn sm" onClick={() => regen(n.id)}>
                  <RefreshCw size={13} /> 生成命令
                </button>
                <button className="btn sm" onClick={() => remove(n.id)}>
                  <Trash2 size={13} /> 删除
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Add node modal */}
      {addOpen && (
        <Modal
          title="添加节点"
          onClose={() => {
            setAddOpen(false);
            setInstallCmd('');
          }}
          footer={
            <>
              <button className="btn" onClick={() => setAddOpen(false)}>
                关闭
              </button>
            </>
          }
        >
          {!installCmd ? (
            <>
              <div className="field">
                <label>节点名称（可稍后修改）</label>
                <input
                  className="input"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="例如：东京-1"
                  autoFocus
                />
              </div>
              <button className="btn primary" onClick={create}>
                生成安装命令
              </button>
            </>
          ) : (
            <>
              <p style={{ color: 'var(--text-secondary)', marginTop: 0 }}>
                在目标主机以 root 执行以下命令，Agent 会自动安装并加入面板：
              </p>
              <div
                className="card mono"
                style={{ padding: 12, background: 'var(--bg-tertiary)', display: 'flex', alignItems: 'center', gap: 8 }}
              >
                <span style={{ flex: 1, wordBreak: 'break-all' }}>{installCmd}</span>
                <CopyButton text={installCmd} />
              </div>
              <p style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>
                支持主流 Linux 发行版；Agent 通过 WSS 反向连接，节点无需开放入站端口。
              </p>
            </>
          )}
        </Modal>
      )}

      {/* Rename modal */}
      {editNode && (
        <Modal
          title="编辑节点"
          onClose={() => setEditNode(null)}
          footer={
            <>
              <button className="btn" onClick={() => setEditNode(null)}>
                取消
              </button>
              <button className="btn primary" onClick={saveName}>
                保存
              </button>
            </>
          }
        >
          <div className="field">
            <label>节点名</label>
            <input className="input" value={editName} onChange={(e) => setEditName(e.target.value)} autoFocus />
          </div>
          <div className="field" style={{ marginBottom: 0 }}>
            <label>SSH 端口</label>
            <input
              className="input"
              style={{ maxWidth: 160 }}
              inputMode="numeric"
              value={editPort}
              onChange={(e) => setEditPort(e.target.value.replace(/[^0-9]/g, ''))}
              placeholder="22"
            />
            <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
              凭据扫描会用此端口校验 SSH 服务是否真实监听（读取本机 sshd banner）。
            </div>
          </div>
        </Modal>
      )}

      {/* Batch update agent modal */}
      {batchOpen && (
        <Modal
          title="批量更新 Agent"
          wide
          onClose={() => setBatchOpen(false)}
        >
          <div className="field">
            <label>选择节点（可多选 / 全选，仅在线）</label>
            <NodeSelect nodes={nodes} value={batchIds} onChange={setBatchIds} onlineOnly placeholder="选择要更新 Agent 的节点…" />
          </div>
          <div className="row" style={{ gap: 10, marginTop: 4 }}>
            <button className="btn primary" onClick={doBatchUpdate} disabled={batchBusy || batchIds.length === 0}>
              <DownloadCloud size={14} /> {batchBusy ? '更新中…' : '更新选中'}
            </button>
            <button className="btn" onClick={batchUpdateAll} disabled={batchBusy}>
              <DownloadCloud size={14} /> 全部更新（所有在线节点）
            </button>
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 8 }}>
            每个节点会拉取 <code>/dl/</code> 最新 Agent 并自更新重启。完成后逐台显示结果，可在节点列表点「刷新」查看。
          </div>

          {batchRes && !batchBusy && (
            <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
              {batchRes.map((x) => (
                <div key={x.node_id} className="row" style={{ justifyContent: 'space-between', padding: '8px 12px', border: '1px solid var(--border-color)', borderRadius: 8 }}>
                  <span style={{ fontSize: 13 }}>
                    {x.name || x.node_id}
                    {!x.online && <span style={{ color: 'var(--text-tertiary)' }}> · 离线</span>}
                  </span>
                  {x.ok ? (
                    <span style={{ fontSize: 12, color: 'var(--success, #2e7d32)' }}>✓ 已更新{x.version ? `（v${x.version}）` : ''}</span>
                  ) : (
                    <span style={{ fontSize: 12, color: 'var(--danger, #c0392b)' }}>✗ {x.err || '失败'}</span>
                  )}
                </div>
              ))}
            </div>
          )}
        </Modal>
      )}

      {/* Install Netdata (health backend) modal */}
      {netOpen && (
        <Modal title="加入 Netdata 监控" wide onClose={() => setNetOpen(false)}>
          <div className="field">
            <label>选择节点（可多选 / 全选，仅在线）</label>
            <NodeSelect nodes={nodes} value={netIds} onChange={setNetIds} onlineOnly installedIds={netInstalled} placeholder="选择要安装 Netdata 的节点…" />
          </div>
          <div className="row" style={{ gap: 10, marginTop: 4 }}>
            <button className="btn primary" onClick={doNetInstall} disabled={netBusy || netIds.length === 0}>
              <Activity size={14} /> {netBusy ? '安装中…' : '安装选中'}
            </button>
            <button className="btn" onClick={netInstallAll} disabled={netBusy}>
              <Activity size={14} /> 安装所有未装节点
            </button>
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 8 }}>
            列表中带 <span className="badge success" style={{ fontSize: 11 }}>✓ 已安装</span> 标记的节点已装过 Netdata（可跳过）。每个节点执行官方 kickstart 脚本，Netdata 仅监听 <code>127.0.0.1:19999</code>（不接入 Netdata Cloud、不开公网），约 60-90 秒/节点。完成后到「健康监控」查看图表并配置告警。
          </div>

          {netRes && !netBusy && (
            <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
              {netRes.map((x) => (
                <div key={x.node_id} className="row" style={{ justifyContent: 'space-between', padding: '8px 12px', border: '1px solid var(--border-color)', borderRadius: 8 }}>
                  <span style={{ fontSize: 13 }}>
                    {x.name || x.node_id}
                    {!x.online && <span style={{ color: 'var(--text-tertiary)' }}> · 离线</span>}
                  </span>
                  {x.ok ? (
                    <span style={{ fontSize: 12, color: 'var(--success, #2e7d32)' }}>✓ 已安装</span>
                  ) : (
                    <span style={{ fontSize: 12, color: 'var(--danger, #c0392b)' }}>✗ {x.err || '失败'}</span>
                  )}
                </div>
              ))}
            </div>
          )}
        </Modal>
      )}

      {/* Dedicated mesh SSH source restriction modal */}
      {meshOpen && (
        <Modal title="SSH 跳板连接限制" wide onClose={() => !meshBusy && setMeshOpen(false)}>
          {meshLoading ? (
            <div style={{ padding: 20, color: 'var(--text-tertiary)' }}>读取配置中…</div>
          ) : (
            <>
              <label className="row" style={{ gap: 9, cursor: 'pointer', marginBottom: 14 }}>
                <input type="checkbox" checked={meshEnabled} onChange={(e) => setMeshEnabled(e.target.checked)} />
                <strong>启用自定义来源白名单</strong>
              </label>
              <div className="field">
                <label>目标节点（可多选 / 全选，离线节点会在重连后应用）</label>
                <NodeSelect nodes={nodes} value={meshIds} onChange={setMeshIds} placeholder="选择要限制跳板 SSH 的节点…" />
                <div className="row" style={{ marginTop: 7, gap: 8 }}>
                  <button className="btn sm" type="button" onClick={() => setMeshIds(nodes.map((n) => n.id))}>全选所有节点</button>
                  <button className="btn sm ghost" type="button" onClick={() => setMeshIds([])}>清空选择</button>
                </div>
              </div>
              <div className="field">
                <label>允许来源 IP / CIDR（每行一个，也可用逗号分隔）</label>
                <textarea
                  className="input mono"
                  rows={6}
                  value={meshSources}
                  onChange={(e) => setMeshSources(e.target.value)}
                  disabled={!meshEnabled}
                  placeholder={'例如：\n203.0.113.10\n198.51.100.0/24\n2001:db8::/48'}
                />
              </div>
              <div className="card" style={{ padding: 11, background: 'var(--bg-tertiary)', fontSize: 12, color: 'var(--text-secondary)' }}>
                对选中节点同时限制其节点资料中配置的实际 SSH 端口和 NodePanel 专用端口 <code>22022</code>；不会修改 sshd 的监听端口。保存后在线节点立即热更新，白名单外的现有 SSH 会话可能断开，但 Agent 的出站管理连接仍可用于恢复。
                {!meshEnabled && (
                  <div style={{ marginTop: 6 }}>当前将使用自动白名单：所有 NodePanel 节点的已知 IP{meshDefaults.length ? `（${meshDefaults.length} 项）` : ''}。</div>
                )}
              </div>
              <div className="row" style={{ gap: 10, marginTop: 14 }}>
                <button className="btn primary" onClick={applyMeshAccess} disabled={meshBusy}>
                  <Shield size={14} /> {meshBusy ? '热更新中…' : '保存并热更新'}
                </button>
                {meshEnabled && <span className="badge warning">白名单外来源将无法连接选中节点的 SSH 端口</span>}
              </div>
              {meshRes && (
                <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
                  {meshRes.length === 0 && <div className="badge muted">配置已保存，无需变更在线节点</div>}
                  {meshRes.map((x) => (
                    <div key={x.node_id} className="row" style={{ justifyContent: 'space-between', padding: '8px 12px', border: '1px solid var(--border-color)', borderRadius: 8 }}>
                      <span style={{ fontSize: 13 }}>
                        {x.name || x.node_id}
                        {Array.isArray(x.ports) && x.ports.length > 0 && <span className="mono" style={{ color: 'var(--text-tertiary)' }}> · TCP/{x.ports.join(',')}</span>}
                      </span>
                      {x.ok ? (
                        <span style={{ fontSize: 12, color: 'var(--success, #2e7d32)' }}>✓ 已热更新</span>
                      ) : x.pending ? (
                        <span style={{ fontSize: 12, color: 'var(--warning, #a66a00)' }}>○ 离线，重连后应用</span>
                      ) : (
                        <span style={{ fontSize: 12, color: 'var(--danger, #c0392b)' }}>✗ {x.error || '应用失败'}</span>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
        </Modal>
      )}

      {/* Firewall modal (single node, port management) */}
      {fwNode && (
        <Modal
          title={`防火墙 — ${fwNode.name}`}
          wide
          onClose={closeFirewall}
          footer={
            <>
              <button className="btn ghost sm" onClick={refreshFw} disabled={fwBusy}>刷新</button>
              <button className="btn sm" onClick={() => portAction('allow')} disabled={!!fwAct || fwSel.size === 0 || (fwRes && !fwRes.active)}>
                <Shield size={13} /> {fwAct === 'allow' ? '处理中…' : `开放选中${fwSel.size ? `（${fwSel.size}）` : ''}`}
              </button>
              <button className="btn sm" onClick={() => portAction('deny')} disabled={!!fwAct || fwSel.size === 0 || (fwRes && !fwRes.active)}>
                <ShieldOff size={13} /> {fwAct === 'deny' ? '处理中…' : `关闭选中${fwSel.size ? `（${fwSel.size}）` : ''}`}
              </button>
            </>
          }
        >
          {fwRes?.error ? (
            <div className="badge error" style={{ display: 'block', padding: 10 }}>{fwRes.error}</div>
          ) : !fwRes ? (
            <div style={{ padding: 20, color: 'var(--text-tertiary)' }}>查询中…</div>
          ) : (
            <>
              <div className="row" style={{ gap: 10, alignItems: 'center', marginBottom: 10, flexWrap: 'wrap' }}>
                <span className="badge muted">{fwRes.type || '未知'}</span>
                {fwRes.active ? <span className="badge success">防火墙已开启</span> : <span className="badge warning">防火墙已关闭</span>}
                {fwRes.type && fwRes.type !== 'none' && (
                  fwRes.active ? (
                    <button className="btn sm" onClick={() => toggleFw('disable')} disabled={!!fwAct}>{fwAct === 'disable' ? '处理中…' : '关闭整个防火墙'}</button>
                  ) : (
                    <button className="btn sm primary" onClick={() => toggleFw('enable')} disabled={!!fwAct}>{fwAct === 'enable' ? '处理中…' : '开启整个防火墙'}</button>
                  )
                )}
              </div>
              {!fwRes.active && fwRes.type && fwRes.type !== 'none' && (
                <div style={{ fontSize: 12, color: 'var(--danger, #c0392b)', marginBottom: 8 }}>⚠ 防火墙未开启：所有端口当前对公网开放。</div>
              )}
              {(!fwRes.ports || fwRes.ports.length === 0) ? (
                <Empty text="未检测到公网监听端口" />
              ) : (
                <div style={{ border: '1px solid var(--border-color)', borderRadius: 8 }}>
                  <div className="row" style={{ padding: '7px 10px', borderBottom: '1px solid var(--border-color)', fontSize: 12, color: 'var(--text-tertiary)', alignItems: 'center' }}>
                    <input
                      type="checkbox"
                      style={{ marginRight: 8 }}
                      checked={fwRes.ports.every((p: any) => fwSel.has(p.port + '/' + p.proto))}
                      onChange={(e) => setFwSel(e.target.checked ? new Set(fwRes.ports.map((p: any) => p.port + '/' + p.proto)) : new Set())}
                    />
                    <div style={{ flex: '0 0 100px' }}>端口</div>
                    <div style={{ flex: 1 }}>状态</div>
                  </div>
                  {fwRes.ports.map((p: any) => {
                    const key = p.port + '/' + p.proto;
                    return (
                      <label key={key} className="row" style={{ padding: '7px 10px', borderBottom: '1px solid var(--border-color)', alignItems: 'center', cursor: 'pointer' }}>
                        <input type="checkbox" style={{ marginRight: 8 }} checked={fwSel.has(key)} onChange={() => togglePortSel(key)} />
                        <div className="mono" style={{ flex: '0 0 100px', fontSize: 13 }}>{key}</div>
                        <div style={{ flex: 1 }}>{p.open ? <span className="badge success">已放行</span> : <span className="badge muted">未放行</span>}</div>
                      </label>
                    );
                  })}
                </div>
              )}
              {fwRes.detail && (
                <details style={{ marginTop: 10 }}>
                  <summary style={{ cursor: 'pointer', fontSize: 12, color: 'var(--text-tertiary)' }}>原始规则</summary>
                  <pre className="mono" style={{ fontSize: 11, whiteSpace: 'pre-wrap', maxHeight: 200, overflow: 'auto' }}>{fwRes.detail}</pre>
                </details>
              )}
            </>
          )}
          <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 10 }}>
            列出该节点公网监听的 TCP 端口，勾选后可批量开放（ufw allow）或关闭（删除放行）。通过应用配置（如 Nginx Full）开放的端口也会标为「已放行」。
          </div>
        </Modal>
      )}

      {/* Regenerate modal */}
      {regenNode && (
        <Modal title="重新生成的安装命令" onClose={() => setRegenNode(null)}>
          <p style={{ color: 'var(--text-secondary)', marginTop: 0 }}>旧命令已失效，请在目标主机执行新命令：</p>
          <div
            className="card mono"
            style={{ padding: 12, background: 'var(--bg-tertiary)', display: 'flex', alignItems: 'center', gap: 8 }}
          >
            <span style={{ flex: 1, wordBreak: 'break-all' }}>{regenNode.cmd}</span>
            <CopyButton text={regenNode.cmd} />
          </div>
        </Modal>
      )}
    </div>
  );
}

function IPRow({ label, value, full }: { label: string; value: string; full?: string }) {
  return (
    <div className="kv-row">
      <span>{label}</span>
      <span className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
        <span className="mono break-anywhere">{value || '—'}</span>
        {value && value !== '—' && <CopyButton text={full || value} title={`复制完整${label}`} />}
      </span>
    </div>
  );
}

// Render a real flag image. Flag *emoji* (regional-indicator code points) do not
// display on Windows / most Linux desktops (no flag font), so they render as the
// literal letters "US"/"SG". A CDN flag image renders the actual flag everywhere.
function Flag({ code, name }: { code?: string; name?: string }) {
  const [failed, setFailed] = useState(false);
  const cc = code && code.length === 2 ? code.toLowerCase() : '';
  if (!cc || failed) {
    return (
      <span
        className="mono"
        title={name || code}
        style={{ fontSize: 12, fontWeight: 600, opacity: 0.6, display: 'inline-block', width: 30, textAlign: 'center' }}
      >
        {(code || '??').toUpperCase()}
      </span>
    );
  }
  return (
    <img
      src={`https://flagcdn.com/h40/${cc}.png`}
      srcSet={`https://flagcdn.com/h80/${cc}.png 2x`}
      width={30}
      height={20}
      alt={name || code}
      title={name || code}
      loading="lazy"
      onError={() => setFailed(true)}
      style={{ borderRadius: 3, objectFit: 'cover', display: 'block', flexShrink: 0 }}
    />
  );
}
