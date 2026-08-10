import { useEffect, useState } from 'react';
import { Plus, Trash2, ScanLine, Upload, RefreshCw, KeyRound, Download, Eye, EyeOff, Globe, Zap, Loader2, MoreVertical } from 'lucide-react';
import { api } from '../services/api';
import { notify } from '../stores';
import { fmtTime, downloadText, safeName } from '../lib/utils';
import { Modal, Empty, CopyButton, ActionSheet, type ActionSheetItem } from '../components/ui';
import { NodeSelect } from '../components/NodePicker';

export function CredentialsPage() {
  const [creds, setCreds] = useState<any[]>([]);
  const [nodes, setNodes] = useState<any[]>([]);
  const [upload, setUpload] = useState(false);
  const [scan, setScan] = useState(false);
  const [form, setForm] = useState({ name: '', pub_key: '', priv_key: '', node_id: '' });
  const [scanNodes, setScanNodes] = useState<string[]>([]);
  const [scanResults, setScanResults] = useState<any[] | null>(null);
  const [scanning, setScanning] = useState(false);
  const [viewCred, setViewCred] = useState<any | null>(null);
  const [showPriv, setShowPriv] = useState(false);
  const [testing, setTesting] = useState<string | null>(null);
  const [testRes, setTestRes] = useState<Record<string, any>>({});
  const [selected, setSelected] = useState<string[]>([]);
  const [batchTesting, setBatchTesting] = useState(false);
  const [sheet, setSheet] = useState<any | null>(null);

  const onlineIds = () => nodes.filter((n) => n.online).map((n) => n.id);

  const load = () => {
    api.credentials.list().then((r: any) => setCreds(Array.isArray(r) ? r : []));
    api.nodes.list().then((r: any) => setNodes(Array.isArray(r) ? r : []));
  };
  useEffect(() => {
    load();
  }, []);

  const create = async () => {
    if (!form.priv_key.trim()) return notify('请粘贴私钥', 'error');
    try {
      await api.credentials.create(form);
      setUpload(false);
      setForm({ name: '', pub_key: '', priv_key: '', node_id: '' });
      load();
      notify('已上传', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  const runScan = async (ids: string[]) => {
    if (ids.length === 0) return notify('请选择节点', 'error');
    setScanning(true);
    setScanResults(null);
    try {
      const r: any = await api.credentials.scanMulti(ids);
      setScanResults(Array.isArray(r) ? r : []);
    } catch (e: any) {
      notify(e?.response?.data?.error || '扫描失败', 'error');
    } finally {
      setScanning(false);
    }
  };

  const doScan = () => runScan(scanNodes);
  // 全面扫描：直接扫所有在线节点（并发），无需手动多选
  const scanAll = () => {
    const ids = onlineIds();
    setScanNodes(ids);
    runScan(ids);
  };

  // 节点级导入：把该节点扫到的指定密钥入库
  const importFor = async (nodeId: string, keys: any[]) => {
    if (keys.length === 0) return;
    try {
      await api.credentials.importKeys(nodeId, keys);
      load();
      notify(`已导入 ${keys.length} 个密钥`, 'success');
    } catch {
      notify('导入失败', 'error');
    }
  };

  // 全量导入：遍历所有有结果的节点，只导入含私钥的密钥对（单公钥已过滤）
  const importAll = async () => {
    if (!scanResults) return;
    let total = 0;
    for (const res of scanResults) {
      if (res.ok && res.keypairs?.length) {
        try {
          await api.credentials.importKeys(res.node_id, res.keypairs);
          total += res.keypairs.length;
        } catch {
          /* 单节点失败继续 */
        }
      }
    }
    load();
    setScan(false);
    setScanResults(null);
    notify(total > 0 ? `已导入 ${total} 个密钥对` : '没有可导入的密钥对', total > 0 ? 'success' : 'error');
  };

  // 默认导入：每个节点只挑一把 —— “最新且【真实登录测试通过】的密钥对（含私钥）”。
  // 只收录含私钥的密钥对，跳过单纯公钥（单公钥无配套私钥，已过滤）。每节点最多 1 个。
  const defaultImport = async () => {
    if (!scanResults) return;
    let count = 0;
    let skipped = 0;
    let nonWorking = 0;
    for (const res of scanResults) {
      if (!res.ok) {
        continue;
      }
      const pairs = (res.keypairs || []).filter((p: any) => p.works); // 只取真实可登录的密钥对
      nonWorking += (res.keypairs || []).filter((p: any) => !p.works).length;
      const pick = pairs.length
        ? pairs.slice().sort((a: any, b: any) => (b.mtime || 0) - (a.mtime || 0))[0]
        : null;
      if (!pick) {
        skipped++;
        continue;
      }
      try {
        await api.credentials.importKeys(res.node_id, [pick]);
        count++;
      } catch {
        skipped++;
      }
    }
    load();
    setScan(false);
    setScanResults(null);
    notify(
      count > 0
        ? `默认导入完成：${count} 个节点各 1 个${nonWorking ? `（已过滤 ${nonWorking} 个登录失败的密钥对）` : ''}${skipped ? `，${skipped} 个跳过` : ''}`
        : '没有可导入的密钥',
      count > 0 ? 'success' : 'error',
    );
  };

  // 真实 SSH 测试：把凭证的私钥发到所绑定节点的 agent，由它对本机 sshd 做一次真实登录，
  // 验证私钥是否真能登入该节点。需要已保存私钥 + 已绑定节点。
  // 真实 SSH 测试：把凭证的私钥发到所绑定节点的 agent，由它对本机 sshd 做一次真实登录，
  // 验证私钥是否真能登入该节点。需要已保存私钥 + 已绑定节点。
  const runTest = (c: any) => api.credentials.test(c.id) as Promise<any>;

  const testKey = async (c: any) => {
    if (!c.priv_key) return notify('该凭证无私钥，无法测试', 'error');
    if (!c.node_id) return notify('请先在「绑定节点」选择目标节点', 'error');
    setTesting(c.id);
    try {
      const r: any = await runTest(c);
      setTestRes((m) => ({ ...m, [c.id]: r }));
      if (r?.works) {
        notify(`✓ 私钥有效：可登录 ${c.node_name || ''} ${r.user ? `（${r.user}）` : ''}:${r.port || ''}`.trim(), 'success');
      } else {
        notify(`✗ 私钥无法登录该节点：${r?.note || '认证失败'}`, 'error');
      }
    } catch (e: any) {
      notify(e?.response?.data?.error || '测试失败', 'error');
    } finally {
      setTesting(null);
    }
  };

  const toggleSel = (id: string) =>
    setSelected((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]));

  // 批量真实 SSH 测试：并发测试所有「含私钥且已绑定节点」的选中项，结果写入每行的 ✓有效/✗无效 徽标，
  // 结束后汇总通知。无私钥或未绑定的选中项会被跳过并计入 skipped。
  const testSelected = async () => {
    const picks = creds.filter((c) => selected.includes(c.id) && c.priv_key && c.node_id);
    const skipped = selected.length - picks.length;
    if (picks.length === 0) {
      return notify('选中项中没有可测试的凭证（需含私钥且已绑定节点）', 'error');
    }
    setBatchTesting(true);
    let ok = 0;
    let fail = 0;
    let err = 0;
    await Promise.all(picks.map(async (c) => {
      try {
        const r: any = await runTest(c);
        setTestRes((m) => ({ ...m, [c.id]: r }));
        if (r?.works) ok++;
        else fail++;
      } catch {
        err++;
      }
    }));
    setBatchTesting(false);
    const parts = [`✓ ${ok} 有效`, `✗ ${fail} 无效`];
    if (err) parts.push(`${err} 出错`);
    if (skipped) parts.push(`${skipped} 跳过(无私钥/未绑定)`);
    notify(`批量测试完成（共 ${picks.length} 个）：${parts.join('，')}`, ok > 0 ? 'success' : 'error');
  };

  return (
    <div>
      <div className="spread" style={{ marginBottom: 18 }}>
        <div>
          <h1 className="page-title">凭证</h1>
          <p className="page-subtitle" style={{ marginBottom: 0 }}>SSH 公钥/密钥管理与节点绑定</p>
        </div>
        <div className="page-actions">
          <button className="btn" onClick={() => { setScanNodes([]); setScanResults(null); setScan(true); }}>
            <ScanLine size={15} /> 扫描节点密钥
          </button>
          <button className="btn primary" onClick={() => setUpload(true)}>
            <Plus size={15} /> 上传密钥
          </button>
        </div>
      </div>

      <div className="card">
        {creds.length === 0 ? (
          <Empty text="暂无凭证" />
        ) : (
          <>
          {selected.length > 0 && (
            <div className="selection-bar" style={{ padding: '8px 12px', borderBottom: '1px solid var(--border-color)', background: 'var(--bg-tertiary)' }}>
              <span style={{ fontSize: 13, fontWeight: 600 }}>已选 {selected.length} 项</span>
              <button className="btn sm primary" onClick={testSelected} disabled={batchTesting} title="对所选凭证逐一真实 SSH 登录其绑定的节点">
                <Zap size={13} /> {batchTesting ? '测试中…' : '测试选中'}
              </button>
              <button className="btn sm ghost" onClick={() => setSelected([])}>取消选择</button>
              <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>仅测试含私钥且已绑定节点的项</span>
            </div>
          )}
          <div className="desktop-only">
          <div className="scroll-x">
          <table className="tbl">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    type="checkbox"
                    title="全选 / 取消全选"
                    checked={creds.length > 0 && selected.length === creds.length}
                    ref={(el) => { if (el) el.indeterminate = selected.length > 0 && selected.length < creds.length; }}
                    onChange={() => setSelected(selected.length === creds.length ? [] : creds.map((c) => c.id))}
                  />
                </th>
                <th>名称</th>
                <th>指纹</th>
                <th>来源</th>
                <th>绑定节点</th>
                <th>创建时间</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {creds.map((c) => (
                <tr key={c.id}>
                  <td style={{ width: 36 }}>
                    <input
                      type="checkbox"
                      checked={selected.includes(c.id)}
                      onChange={() => toggleSel(c.id)}
                      title="选择该凭证"
                    />
                  </td>
                  <td>{c.name}</td>
                  <td className="mono">
                    <span className="row">
                      <span>{c.fingerprint?.slice(0, 24)}…</span>
                      <CopyButton text={c.fingerprint} />
                    </span>
                  </td>
                  <td>
                    <span className="badge muted">{c.source}</span>
                  </td>
                  <td>
                    <select
                      className="select"
                      style={{ width: 'auto', padding: '4px 8px' }}
                      value={c.node_id || ''}
                      onChange={async (e) => {
                        await api.credentials.bind(c.id, e.target.value);
                        load();
                      }}
                    >
                      <option value="">未绑定</option>
                      {nodes.map((n) => (
                        <option key={n.id} value={n.id}>
                          {n.name}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td style={{ color: 'var(--text-tertiary)' }}>{fmtTime(c.created_at)}</td>
                  <td style={{ textAlign: 'right' }}>
                    <div className="row" style={{ justifyContent: 'flex-end', gap: 2, alignItems: 'center' }}>
                      {testRes[c.id] && (
                        <span
                          title={testRes[c.id].works
                            ? `✓ 私钥有效，可登录该节点${testRes[c.id].user ? `（${testRes[c.id].user}）` : ''}:${testRes[c.id].port || ''}`
                            : `✗ ${testRes[c.id].note || '私钥无法登录该节点'}`}
                          style={{
                            fontSize: 11,
                            fontWeight: 600,
                            color: testRes[c.id].works ? 'var(--success, #2e7d32)' : 'var(--danger, #c0392b)',
                            whiteSpace: 'nowrap',
                          }}
                        >
                          {testRes[c.id].works ? '✓有效' : '✗无效'}
                        </span>
                      )}
                      <button
                        className="icon-btn"
                        title={!c.priv_key
                          ? '该凭证无私钥，无法测试'
                          : !c.node_id
                            ? '未绑定节点，无法测试（请先在「绑定节点」选择目标）'
                            : '测试私钥（真实 SSH 登录所绑定的节点）'}
                        disabled={!c.priv_key || !c.node_id || testing === c.id}
                        onClick={() => testKey(c)}
                      >
                        {testing === c.id ? <Loader2 size={15} className="spin" /> : <Zap size={15} />}
                      </button>
                      <button
                        className="icon-btn"
                        title="查看 / 复制 / 下载密钥"
                        onClick={() => { setViewCred(c); setShowPriv(false); }}
                      >
                        <KeyRound size={15} />
                      </button>
                      <button
                        className="icon-btn"
                        title="下载公钥"
                        onClick={() => downloadText(`${safeName(c.name)}.pub`, c.pub_key || '')}
                      >
                        <Download size={15} />
                      </button>
                      <button
                        className="icon-btn"
                        onClick={async () => {
                          await api.credentials.remove(c.id);
                          load();
                        }}
                      >
                        <Trash2 size={15} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          </div>

          <div className="mobile-only m-list">
            {creds.map((c) => {
              const nodeLabel = nodes.find((n) => n.id === c.node_id)?.name || (c.node_id ? c.node_id.slice(0, 8) : '未绑定');
              return (
                <div key={c.id} className="m-item">
                  <div className="m-item-check">
                    <input
                      type="checkbox"
                      checked={selected.includes(c.id)}
                      onChange={() => toggleSel(c.id)}
                    />
                  </div>
                  <div className="m-item-main" onClick={() => setSheet(c)} role="button" tabIndex={0}
                    onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSheet(c); } }}>
                    <div className="m-item-title">{c.name}</div>
                    <div className="m-item-sub mono">{c.fingerprint?.slice(0, 20)}…</div>
                    <div className="m-item-meta">
                      <span className="badge muted">{c.source}</span>
                      <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>{nodeLabel}</span>
                      {testRes[c.id] && (
                        <span style={{
                          fontSize: 11,
                          fontWeight: 600,
                          color: testRes[c.id].works ? 'var(--success, #2e7d32)' : 'var(--danger, #c0392b)',
                        }}>
                          {testRes[c.id].works ? '✓有效' : '✗无效'}
                        </span>
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

          <ActionSheet
            open={!!sheet}
            onClose={() => setSheet(null)}
            title={sheet?.name}
            subtitle={sheet ? fmtTime(sheet.created_at) : undefined}
            actions={sheet ? ([
              {
                key: 'test',
                label: testing === sheet.id ? '测试中…' : '测试 SSH 登录',
                icon: testing === sheet.id ? <Loader2 size={18} className="spin" /> : <Zap size={18} />,
                disabled: !sheet.priv_key || !sheet.node_id || testing === sheet.id,
                onClick: () => testKey(sheet),
              },
              {
                key: 'view',
                label: '查看 / 复制密钥',
                icon: <KeyRound size={18} />,
                onClick: () => { setViewCred(sheet); setShowPriv(false); },
              },
              {
                key: 'dl',
                label: '下载公钥',
                icon: <Download size={18} />,
                onClick: () => downloadText(`${safeName(sheet.name)}.pub`, sheet.pub_key || ''),
              },
              {
                key: 'delete',
                label: '删除凭证',
                icon: <Trash2 size={18} />,
                danger: true,
                onClick: async () => {
                  await api.credentials.remove(sheet.id);
                  load();
                },
              },
            ] as ActionSheetItem[]) : []}
          >
            {sheet && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                <div className="kv-row">
                  <span>指纹</span>
                  <span className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
                    <span className="mono" style={{ fontSize: 11 }}>{sheet.fingerprint?.slice(0, 24)}…</span>
                    <CopyButton text={sheet.fingerprint} />
                  </span>
                </div>
                <div className="field" style={{ marginBottom: 0 }}>
                  <label>绑定节点</label>
                  <select
                    className="select"
                    value={sheet.node_id || ''}
                    onChange={async (e) => {
                      const v = e.target.value;
                      await api.credentials.bind(sheet.id, v);
                      setSheet({ ...sheet, node_id: v });
                      load();
                    }}
                  >
                    <option value="">未绑定</option>
                    {nodes.map((n) => (
                      <option key={n.id} value={n.id}>{n.name}</option>
                    ))}
                  </select>
                </div>
              </div>
            )}
          </ActionSheet>
          </>
        )}
      </div>

      {upload && (
        <Modal title="上传 SSH 密钥" onClose={() => setUpload(false)} footer={<>
          <button className="btn" onClick={() => setUpload(false)}>取消</button>
          <button className="btn primary" onClick={create}><Upload size={14} /> 上传</button>
        </>}>
          <div className="field">
            <label>名称</label>
            <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="例如：deploy-key" />
          </div>
          <div className="field">
            <label>私钥（必填）</label>
            <textarea className="textarea" value={form.priv_key} onChange={(e) => setForm({ ...form, priv_key: e.target.value })} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
          </div>
          <div className="field">
            <label>公钥（可选，留空将自动从私钥推导）</label>
            <textarea className="textarea" value={form.pub_key} onChange={(e) => setForm({ ...form, pub_key: e.target.value })} placeholder="ssh-ed25519 AAAA... comment" />
          </div>
          <div className="field">
            <label>绑定节点（可选）</label>
            <select className="select" value={form.node_id} onChange={(e) => setForm({ ...form, node_id: e.target.value })}>
              <option value="">不绑定</option>
              {nodes.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
            </select>
          </div>
        </Modal>
      )}

      {scan && (
        <Modal
          title="扫描节点密钥（全面扫描）"
          onClose={() => { setScan(false); setScanResults(null); }}
          wide
          footer={
            scanResults && scanResults.some((r) => r.ok && r.keypairs?.length) ? (
              <>
                <button className="btn" onClick={defaultImport} title="每个节点只导入 1 个最新的、公私匹配的有效密钥">
                  默认导入（每节点 1 个）
                </button>
                <button className="btn primary" onClick={importAll}>
                  导入全部 ({scanResults.reduce((s, r) => s + (r.ok ? (r.keypairs?.length || 0) : 0), 0)})
                </button>
              </>
            ) : undefined
          }
        >
          <div className="field">
            <label>选择节点（可多选 / 全选，仅在线）</label>
            <NodeSelect nodes={nodes} value={scanNodes} onChange={setScanNodes} onlineOnly placeholder="选择要扫描的节点…" />
          </div>
          <div className="row" style={{ gap: 10, marginTop: 4 }}>
            <button className="btn primary" onClick={doScan} disabled={scanning || scanNodes.length === 0}>
              <RefreshCw size={14} /> {scanning ? '扫描中…' : '扫描选中'}
            </button>
            <button className="btn" onClick={scanAll} disabled={scanning}>
              <Globe size={14} /> 全面扫描所有在线节点
            </button>
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 8 }}>
            只收录<strong>含私钥</strong>的密钥（<code>*.pub</code> 配套私钥、或单独的私钥文件），<strong>已过滤单纯公钥</strong>（authorized_keys 里没有配套私钥的，本机拿不到私钥，无法备份/复用）。
            每个密钥对<strong>公钥+私钥</strong>一起收录并已实测能否登录本机，可导入备份。SSH 端口自动探测真实监听端口（不必是 22）。
          </div>

          {scanning && <div style={{ marginTop: 16, color: 'var(--text-tertiary)' }}>正在扫描，请稍候…</div>}

          {scanResults && !scanning && (
            <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 14 }}>
              {scanResults.map((res) => {
                const keypairs = res.ok ? res.keypairs || [] : [];
                return (
                  <div key={res.node_id} style={{ border: '1px solid var(--border-color)', borderRadius: 8, overflow: 'hidden' }}>
                    <div className="row" style={{ justifyContent: 'space-between', padding: '8px 12px', background: 'var(--bg-tertiary)' }}>
                      <strong style={{ fontSize: 13 }}>
                        {res.name || res.node_id}
                        <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>
                          {' · '}{res.ok ? `${keypairs.length} 个密钥对（含私钥）` : '扫描失败'}
                        </span>
                        {res.ok && res.ssh_port ? (
                          <span style={{ marginLeft: 8, fontSize: 12, color: res.ssh_reachable ? 'var(--success, #2e7d32)' : 'var(--danger, #c0392b)' }}>
                            {res.ssh_reachable
                              ? res.ssh_detected_port && res.ssh_detected_port !== res.ssh_port
                                ? <>SSH 实际 :{res.ssh_detected_port} ✓ <span style={{ color: 'var(--text-tertiary)' }}>（你填 :{res.ssh_port}）</span></>
                                : <>SSH :{res.ssh_port} ✓ 可达</>
                              : <>SSH :{res.ssh_port} ✗ 未检测到</>}
                          </span>
                        ) : null}
                      </strong>
                      {res.ok && keypairs.length > 0 && (
                        <button className="btn sm" onClick={() => importFor(res.node_id, keypairs)}>导入本节点全部</button>
                      )}
                    </div>
                    {!res.ok ? (
                      <div style={{ padding: '10px 12px', color: 'var(--danger, #c0392b)', fontSize: 12 }}>{res.error || 'agent 不可达'}</div>
                    ) : keypairs.length === 0 ? (
                      <div style={{ padding: '10px 12px', color: 'var(--text-tertiary)', fontSize: 12 }}>未发现含私钥的密钥对（本机无可用私钥）</div>
                    ) : (
                      <div className="scroll-x"><table className="tbl">
                        <tbody>
                          {keypairs.map((k: any, i: number) => (
                            <tr key={i}>
                              <td className="mono" style={{ whiteSpace: 'nowrap' }}>
                                {k.user ? `${k.user}@` : ''}{k.name}
                                <span className="badge muted" style={{ marginLeft: 6 }}>私钥✓</span>
                                {k.works ? (
                                  <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--success, #2e7d32)' }}>可登录✓</span>
                                ) : (
                                  <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--danger, #c0392b)' }} title={k.works_note || '私钥连本机 sshd 认证失败：其公钥不在本机 authorized_keys，无法登录本机'}>不可登录✗</span>
                                )}
                              </td>
                              <td className="mono" style={{ color: 'var(--text-tertiary)' }}>{k.pub_key?.slice(0, 48)}…</td>
                              <td style={{ textAlign: 'right' }}>
                                <button className="btn sm" onClick={() => importFor(res.node_id, [k])}>导入(含私钥)</button>
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table></div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </Modal>
      )}

      {viewCred && (
        <Modal
          title={`密钥 · ${viewCred.name}`}
          wide
          onClose={() => setViewCred(null)}
          footer={<button className="btn" onClick={() => setViewCred(null)}>关闭</button>}
        >
          {/* Public key */}
          <div className="field">
            <div className="row" style={{ justifyContent: 'space-between', marginBottom: 6 }}>
              <label style={{ margin: 0 }}>公钥（{viewCred.kind || 'public'}）</label>
              <div className="row" style={{ gap: 6 }}>
                <CopyButton text={viewCred.pub_key || ''} title="复制公钥" />
                <button
                  className="btn sm"
                  onClick={() => downloadText(`${safeName(viewCred.name)}.pub`, viewCred.pub_key || '')}
                >
                  <Download size={13} /> 下载公钥
                </button>
              </div>
            </div>
            <pre className="mono" style={{ margin: 0, maxHeight: 120, overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', background: 'var(--bg-secondary)', padding: 10, borderRadius: 8, fontSize: 12 }}>
              {viewCred.pub_key || '—'}
            </pre>
          </div>

          {/* Private key (if present) */}
          <div className="field" style={{ marginBottom: 0 }}>
            <div className="row" style={{ justifyContent: 'space-between', marginBottom: 6 }}>
              <label style={{ margin: 0 }}>
                私钥{' '}
                {viewCred.priv_key
                  ? <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>（已保存）</span>
                  : <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>（未保存）</span>}
              </label>
              {viewCred.priv_key && (
                <div className="row" style={{ gap: 6 }}>
                  <button className="btn sm ghost" onClick={() => setShowPriv((v) => !v)}>
                    {showPriv ? <EyeOff size={13} /> : <Eye size={13} />} {showPriv ? '隐藏' : '显示'}
                  </button>
                  <CopyButton text={viewCred.priv_key} title="复制私钥" />
                  <button
                    className="btn sm"
                    onClick={() => downloadText(safeName(viewCred.name), viewCred.priv_key, 'application/octet-stream')}
                  >
                    <Download size={13} /> 下载私钥
                  </button>
                </div>
              )}
            </div>
            {viewCred.priv_key ? (
              <pre className="mono" style={{ margin: 0, maxHeight: showPriv ? 200 : 40, overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', background: 'var(--bg-secondary)', padding: 10, borderRadius: 8, fontSize: 12, filter: showPriv ? 'none' : 'blur(6px)' }}>
                {viewCred.priv_key}
              </pre>
            ) : (
              <div style={{ color: 'var(--text-tertiary)', fontSize: 13, padding: 10 }}>
                该凭证未保存私钥。新版 Agent 在自动收录新生成的密钥（如「安全」命令）时会一并带上私钥；
                此条可能是旧版收录或仅含公钥，可回到当时执行「安全」的命令输出中复制私钥。
              </div>
            )}
          </div>
        </Modal>
      )}
    </div>
  );
}
