import { useEffect, useState } from 'react';
import { Plus, RefreshCw, Trash2, Pencil, DownloadCloud, Shield, ShieldOff, Radar, Activity } from 'lucide-react';
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
  // Komari probe join modal state
  const [probeOpen, setProbeOpen] = useState(false);
  const [probeCfg, setProbeCfg] = useState<any | null>(null);
  const [probeCands, setProbeCands] = useState<any[]>([]);
  const [probeExisting, setProbeExisting] = useState<any[]>([]);
  const [probeIds, setProbeIds] = useState<string[]>([]);
  const [probeBusy, setProbeBusy] = useState(false);
  const [probeRes, setProbeRes] = useState<any[] | null>(null);
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

  // --- Komari probe join ---
  const openProbe = async () => {
    setProbeOpen(true);
    setProbeIds([]); setProbeRes(null); setProbeCands([]); setProbeExisting([]); setProbeCfg(null);
    try {
      const r: any = await api.nodes.probeCandidates();
      setProbeCfg(r);
      if (r?.configured) { setProbeCands(r.candidates || []); setProbeExisting(r.existing || []); }
    } catch (e: any) {
      notify(e?.response?.data?.error || '读取探针候选失败', 'error');
    }
  };
  const runProbeJoin = async (ids: string[]) => {
    if (ids.length === 0) return notify('请选择节点', 'error');
    setProbeBusy(true); setProbeRes(null);
    try {
      const r: any = await api.nodes.probeJoin(ids);
      setProbeRes(Array.isArray(r) ? r : []);
      const ok = (r as any[]).filter((x) => x.ok).length;
      notify(`完成：${ok}/${ids.length} 个节点已加入探针`, ok === ids.length ? 'success' : 'error');
      const c: any = await api.nodes.probeCandidates().catch(() => null); // refresh: joined ones drop out
      if (c?.configured) { setProbeCfg(c); setProbeCands(c.candidates || []); setProbeExisting(c.existing || []); }
      setProbeIds([]);
    } catch (e: any) {
      notify(e?.response?.data?.error || '加入探针失败', 'error');
    } finally {
      setProbeBusy(false);
    }
  };
  const doProbeJoin = () => runProbeJoin(probeIds);
  const probeJoinAll = () => {
    const ids = probeCands.map((n) => n.id);
    setProbeIds(ids);
    runProbeJoin(ids);
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
          <button className="btn" onClick={openProbe} title="把节点批量接入 Komari 探针">
            <Radar size={15} /> 加入探针
          </button>
          <button
            className="btn"
            onClick={openNet}
            title="批量安装 Netdata 监控（仅本地，不接入 Cloud）"
          >
            <Activity size={15} /> 加入Netdata
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

      {/* Join Komari probe modal */}
      {probeOpen && (
        <Modal title="加入探针（Komari）" wide onClose={() => setProbeOpen(false)}>
          {probeCfg && !probeCfg.configured ? (
            <div className="card" style={{ padding: 16 }}>
              <p style={{ marginTop: 0 }}>未配置 Komari 探针。请先到「设置 → 探针 Komari」填写面板地址与 API Key。</p>
              <a className="btn primary sm" href="/settings" style={{ textDecoration: 'none' }}>前往设置</a>
            </div>
          ) : (
            <>
              <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 10 }}>
                目标探针：<code className="mono">{probeCfg?.komari_url}</code>。Komari 节点名 = NodePanel 节点名；已在探针里的节点不会出现。
              </div>
              <div className="field">
                <label>候选节点（在线且未已在探针，可多选 / 全选）</label>
                <NodeSelect
                  nodes={probeCands}
                  value={probeIds}
                  onChange={setProbeIds}
                  onlineOnly
                  placeholder={probeCands.length ? '选择要加入探针的节点…' : '没有可加入的节点（在线节点都已在探针）'}
                />
              </div>
              {probeExisting.length > 0 && (
                <div className="field">
                  <label>已在探针中（无需加入，{probeExisting.length} 个）</label>
                  <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
                    {probeExisting.map((n: any) => (
                      <span key={n.id} className="badge muted">✓ {n.name}</span>
                    ))}
                  </div>
                </div>
              )}
              <div className="row" style={{ gap: 10, marginTop: 4 }}>
                <button className="btn primary" onClick={doProbeJoin} disabled={probeBusy || probeIds.length === 0}>
                  <Radar size={14} /> {probeBusy ? '加入中…' : '加入选中'}
                </button>
                <button className="btn" onClick={probeJoinAll} disabled={probeBusy || probeCands.length === 0}>
                  <Radar size={14} /> 全部加入
                </button>
              </div>
              <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 8 }}>
                每个节点会在 Komari 创建同名 client 并安装 komari-agent 接入探针，约 30-90 秒/节点。
              </div>
              {probeRes && !probeBusy && (
                <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
                  {probeRes.map((x) => (
                    <div key={x.node_id} className="row" style={{ justifyContent: 'space-between', padding: '8px 12px', border: '1px solid var(--border-color)', borderRadius: 8 }}>
                      <span style={{ fontSize: 13 }}>{x.name || x.node_id}</span>
                      {x.ok ? (
                        <span style={{ fontSize: 12, color: 'var(--success, #2e7d32)' }}>✓ 已加入</span>
                      ) : (
                        <span style={{ fontSize: 12, color: 'var(--danger, #c0392b)' }}>✗ {x.err || '失败'}</span>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </>
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
