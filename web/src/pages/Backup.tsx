import { useEffect, useState } from 'react';
import { Plus, Trash2, Play, RotateCcw, RefreshCw, FlaskConical, Pencil, Folder, ChevronUp, HardDrive, Box, MoreVertical } from 'lucide-react';
import { api } from '../services/api';
import { notify } from '../stores';
import { bytes, fmtTime } from '../lib/utils';
import { Modal, Empty, CopyButton, ActionSheet, type ActionSheetItem } from '../components/ui';
import { NodeSelect } from '../components/NodePicker';
import { MultiSelect } from '../components/MultiSelect';

const WEEK = ['日', '一', '二', '三', '四', '五', '六'];

export function BackupPage() {
  const [tab, setTab] = useState('jobs');
  return (
    <div>
      <h1 className="page-title">备份</h1>
      <p className="page-subtitle">目录备份 / 恢复、存储目标、保留策略与计划任务</p>
      <div className="tab-bar" role="tablist">
        {[
          ['jobs', '备份记录'],
          ['run', '立即备份'],
          ['targets', '存储目标'],
          ['schedule', '保留计划'],
        ].map(([k, l]) => (
          <button
            key={k}
            type="button"
            role="tab"
            aria-selected={tab === k}
            className={tab === k ? 'tab-item active' : 'tab-item'}
            onClick={() => setTab(k)}
          >
            {l}
          </button>
        ))}
      </div>
      {tab === 'jobs' && <JobsTab />}
      {tab === 'run' && <RunTab />}
      {tab === 'targets' && <TargetsTab />}
      {tab === 'schedule' && <ScheduleTab />}
    </div>
  );
}

function JobsTab() {
  const [backups, setBackups] = useState<any[]>([]);
  const [nodes, setNodes] = useState<any[]>([]);
  const [restore, setRestore] = useState<any | null>(null);
  const [rNode, setRNode] = useState('');
  const [rDest, setRDest] = useState('');
  const [sheet, setSheet] = useState<any | null>(null);

  const load = () => {
    api.backups.list().then((r: any) => setBackups(Array.isArray(r) ? r : []));
    api.nodes.list().then((r: any) => setNodes(Array.isArray(r) ? r : []));
  };
  useEffect(() => load(), []);

  const doRestore = async () => {
    if (!restore || !rNode || !rDest) return notify('请选择节点并填写路径', 'error');
    try {
      await api.backups.restore(restore.id, rNode, rDest);
      notify('恢复任务已下发', 'success');
      setRestore(null);
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  return (
    <div className="card">
      <div className="spread" style={{ padding: '12px 14px' }}>
        <strong>备份记录</strong>
        <button className="btn sm ghost" onClick={load}><RefreshCw size={13} /> 刷新</button>
      </div>
      {backups.length === 0 ? (
        <Empty text="暂无备份" />
      ) : (
        <>
        <div className="desktop-only">
        <div className="scroll-x"><table className="tbl">
          <thead>
            <tr>
              <th>名称</th>
              <th>节点</th>
              <th>大小</th>
              <th>状态</th>
              <th>时间</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {backups.map((b) => (
              <tr key={b.id}>
                <td>
                  <span className="badge muted" style={{ marginRight: 6 }}>{b.container ? '容器' : '目录'}</span>
                  {b.name}
                </td>
                <td>{nodes.find((n) => n.id === b.node_id)?.name || b.node_id?.slice(0, 8)}</td>
                <td className="mono">{bytes(b.size)}</td>
                <td>
                  <span className={`badge ${b.status === 'ok' ? 'success' : b.status === 'failed' ? 'error' : 'warning'}`}>
                    {b.status}
                  </span>
                </td>
                <td style={{ color: 'var(--text-tertiary)' }}>{fmtTime(b.created_at)}</td>
                <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                  <button className="btn sm" onClick={() => setRestore(b)}>
                    <RotateCcw size={13} /> 恢复
                  </button>
                  <button
                    className="icon-btn"
                    onClick={async () => {
                      await api.backups.remove(b.id);
                      load();
                    }}
                  >
                    <Trash2 size={14} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table></div>
        </div>

        <div className="mobile-only m-list">
          {backups.map((b) => (
            <div key={b.id} className="m-item">
              <div className="m-item-main" onClick={() => setSheet(b)} role="button" tabIndex={0}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSheet(b); } }}>
                <div className="m-item-title">{b.name}</div>
                <div className="m-item-sub">
                  {nodes.find((n) => n.id === b.node_id)?.name || b.node_id?.slice(0, 8)}
                  {' · '}
                  {bytes(b.size)}
                  {' · '}
                  {fmtTime(b.created_at)}
                </div>
                <div className="m-item-meta">
                  <span className="badge muted">{b.container ? '容器' : '目录'}</span>
                  <span className={`badge ${b.status === 'ok' ? 'success' : b.status === 'failed' ? 'error' : 'warning'}`}>
                    {b.status}
                  </span>
                </div>
              </div>
              <button className="icon-btn m-item-more" title="更多" onClick={() => setSheet(b)}>
                <MoreVertical size={18} />
              </button>
            </div>
          ))}
        </div>

        <ActionSheet
          open={!!sheet}
          onClose={() => setSheet(null)}
          title={sheet?.name}
          subtitle={sheet ? `${sheet.container ? '容器' : '目录'} · ${fmtTime(sheet.created_at)}` : undefined}
          actions={sheet ? ([
            !sheet.container && {
              key: 'restore',
              label: '恢复到目录',
              icon: <RotateCcw size={18} />,
              onClick: () => setRestore(sheet),
            },
            {
              key: 'delete',
              label: '删除记录',
              icon: <Trash2 size={18} />,
              danger: true,
              onClick: async () => {
                await api.backups.remove(sheet.id);
                load();
              },
            },
          ].filter(Boolean) as ActionSheetItem[]) : []}
        >
          {sheet && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              <div className="kv-row">
                <span>节点</span>
                <span>{nodes.find((n) => n.id === sheet.node_id)?.name || sheet.node_id?.slice(0, 8)}</span>
              </div>
              <div className="kv-row">
                <span>大小</span>
                <span className="mono">{bytes(sheet.size)}</span>
              </div>
              <div className="kv-row">
                <span>状态</span>
                <span className={`badge ${sheet.status === 'ok' ? 'success' : sheet.status === 'failed' ? 'error' : 'warning'}`}>
                  {sheet.status}
                </span>
              </div>
            </div>
          )}
        </ActionSheet>
        </>
      )}

      {restore && (
        <Modal title={`恢复：${restore.name}`} onClose={() => setRestore(null)} footer={<>
          <button className="btn" onClick={() => setRestore(null)}>取消</button>
          <button className="btn primary" onClick={doRestore}><RotateCcw size={14} /> 恢复</button>
        </>}>
          <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
            选择目标节点与恢复路径（定向恢复）。
            {restore.container
              ? ' 该备份是「容器」类型：数据会恢复到容器原始的数据卷/挂载路径；下方「恢复到目录」仅用于存放 container.json 配置快照。'
              : ''}
          </p>
          <div className="field">
            <label>目标节点</label>
            <select className="select" value={rNode} onChange={(e) => setRNode(e.target.value)}>
              <option value="">选择…</option>
              {nodes.filter((n) => n.online).map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
            </select>
          </div>
          <div className="field">
            <label>恢复到目录</label>
            <input className="input" value={rDest} onChange={(e) => setRDest(e.target.value)} placeholder="/tmp/restore" />
          </div>
        </Modal>
      )}
    </div>
  );
}

function RunTab() {
  const [nodes, setNodes] = useState<any[]>([]);
  const [targets, setTargets] = useState<any[]>([]);
  const [containers, setContainers] = useState<any[]>([]);
  const [busy, setBusy] = useState(false);
  const [source, setSource] = useState<'container' | 'dir'>('container');
  const [form, setForm] = useState<any>({ node_ids: [], paths: '/etc/nginx', target_id: '', name: '', container_id: '' });

  useEffect(() => {
    api.nodes.list().then((r: any) => setNodes(Array.isArray(r) ? r : []));
    api.targets.list().then((r: any) => setTargets(Array.isArray(r) ? r : []));
    api.containers.list().then((r: any) => setContainers(Array.isArray(r) ? r : []));
  }, []);

  const run = async () => {
    setBusy(true);
    try {
      if (source === 'container') {
        if (!form.container_id) { notify('请选择容器', 'error'); setBusy(false); return; }
        const c = containers.find((x) => x.container_id === form.container_id);
        if (!c) { notify('容器不存在', 'error'); setBusy(false); return; }
        await api.backups.now({ node_id: c.node_id, container: c.container_id, target_id: form.target_id, name: form.name });
        notify('容器备份任务已下发', 'success');
      } else {
        if (form.node_ids.length === 0 || !form.paths.trim()) { notify('选择节点并填写路径', 'error'); setBusy(false); return; }
        const paths = form.paths.split('\n').map((s: string) => s.trim()).filter(Boolean);
        const results = await Promise.all(
          form.node_ids.map((id: string) => api.backups.now({ node_id: id, paths, target_id: form.target_id, name: form.name }).catch(() => null))
        );
        const ok = results.filter((r) => r !== null).length;
        const fail = results.length - ok;
        notify(`已下发 ${ok} 个节点的备份任务${fail ? '，' + fail + ' 个失败' : ''}`, fail ? 'error' : 'success');
      }
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    } finally {
      setBusy(false);
    }
  };

  const cLabel = (c: any) => `${nodes.find((n) => n.id === c.node_id)?.name || c.node_id?.slice(0, 6)} · ${c.display_name || c.name}`;

  return (
    <div className="card" style={{ padding: 18, maxWidth: 560 }}>
      <div className="field">
        <label>备份类型</label>
        <div className="row" style={{ gap: 8 }}>
          <button className={`btn sm ${source === 'container' ? 'primary' : ''}`} onClick={() => setSource('container')}><Box size={13} /> 容器（带数据）</button>
          <button className={`btn sm ${source === 'dir' ? 'primary' : ''}`} onClick={() => setSource('dir')}><Folder size={13} /> 目录</button>
        </div>
      </div>

      {source === 'container' ? (
        <div className="field">
          <label>容器（备份其数据卷 + 配置）</label>
          <select className="select" value={form.container_id} onChange={(e) => setForm({ ...form, container_id: e.target.value })}>
            <option value="">选择容器…</option>
            {containers.map((c) => (
              <option key={c.node_id + ':' + c.container_id} value={c.container_id}>{cLabel(c)}</option>
            ))}
          </select>
          <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
            自动打包该容器的 bind 挂载与命名数据卷内容，并附带容器配置（便于恢复）。
          </div>
        </div>
      ) : (
        <>
          <div className="field">
            <label>节点（可多选，仅在线）</label>
            <NodeSelect nodes={nodes} value={form.node_ids} onChange={(ids) => setForm({ ...form, node_ids: ids })} onlineOnly placeholder="选择节点…" />
          </div>
          <div className="field">
            <label>备份目录（每行一个，将在每个选中节点上备份）</label>
            <textarea className="textarea" value={form.paths} onChange={(e) => setForm({ ...form, paths: e.target.value })} />
          </div>
        </>
      )}

      <div className="field">
        <label>存储目标（可留空，仅本地暂存）</label>
        <select className="select" value={form.target_id} onChange={(e) => setForm({ ...form, target_id: e.target.value })}>
          <option value="">仅本地暂存</option>
          {targets.map((t) => <option key={t.id} value={t.id}>{t.name} ({t.type})</option>)}
        </select>
      </div>
      <div className="field">
        <label>名称</label>
        <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="可留空自动生成" />
      </div>
      <button className="btn primary" onClick={run} disabled={busy}>
        <Play size={14} /> {busy ? '下发中…' : '立即备份'}
      </button>
    </div>
  );
}

function TargetsTab() {
  const [targets, setTargets] = useState<any[]>([]);
  const [add, setAdd] = useState(false);
  const [sheet, setSheet] = useState<any | null>(null);
  const [type, setType] = useState('github');
  const [cfg, setCfg] = useState<any>({});
  const [name, setName] = useState('');
  // onedrive device flow
  const [odDevice, setOdDevice] = useState<any | null>(null);
  const [odPolling, setOdPolling] = useState(false);
  // github repo picker
  const [ghRepos, setGhRepos] = useState<any[]>([]);
  const [ghReposBusy, setGhReposBusy] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  // vps directory browser
  const [vpsBrowse, setVpsBrowse] = useState(false);
  const [vpsPath, setVpsPath] = useState('/');
  const [vpsEntries, setVpsEntries] = useState<any[]>([]);
  const [vpsMounts, setVpsMounts] = useState<any[]>([]);
  const [vpsCurrent, setVpsCurrent] = useState<any>(null);
  const [vpsBusy, setVpsBusy] = useState(false);

  const load = () => api.targets.list().then((r: any) => setTargets(Array.isArray(r) ? r : []));
  useEffect(() => {
    load();
  }, []);

  const openAdd = () => {
    setEditing(null);
    setType('github');
    setCfg({});
    setName('');
    setOdDevice(null);
    setGhRepos([]);
    setAdd(true);
  };

  const openEdit = (t: any) => {
    setEditing(t.id);
    setType(t.type);
    try { setCfg(typeof t.config === 'string' ? JSON.parse(t.config || '{}') : (t.config || {})); } catch { setCfg({}); }
    setName(t.name || '');
    setOdDevice(null);
    setGhRepos([]);
    setAdd(true);
  };

  const close = () => { setAdd(false); setEditing(null); setCfg({}); setName(''); setOdDevice(null); setGhRepos([]); };

  const fetchRepos = async () => {
    if (!cfg.token) return notify('请先粘贴令牌', 'error');
    setGhReposBusy(true);
    try {
      const r: any = await api.targets.githubRepos(cfg.token);
      setGhRepos(Array.isArray(r) ? r : []);
      notify(`拉取到 ${(r as any[]).length} 个可写仓库`, 'success');
    } catch (e: any) {
      setGhRepos([]);
      notify(e?.response?.data?.error || '拉取失败', 'error');
    } finally {
      setGhReposBusy(false);
    }
  };

  const fetchVps = async (p: string) => {
    if (!cfg.host) return notify('请先填写 Host', 'error');
    if (!cfg.password && !cfg.key_pem) return notify('请填写密码或私钥', 'error');
    setVpsBusy(true);
    try {
      const r: any = await api.targets.vpsList(
        { host: cfg.host, port: cfg.port || 22, user: cfg.user || 'root', password: cfg.password, key_pem: cfg.key_pem },
        p
      );
      setVpsEntries(Array.isArray(r?.entries) ? r.entries : []);
      setVpsPath(r?.path || p || '/');
      setVpsMounts(Array.isArray(r?.mounts) ? r.mounts : []);
      setVpsCurrent(r?.current || null);
    } catch (e: any) {
      setVpsEntries([]);
      notify(e?.response?.data?.error || '列目录失败', 'error');
    } finally {
      setVpsBusy(false);
    }
  };
  const openVpsBrowse = () => {
    // start at the SFTP home dir (empty path → backend resolves via Getwd)
    setVpsBrowse(true);
    fetchVps(cfg.base_dir && cfg.base_dir.trim() !== '' ? cfg.base_dir : '');
  };
  const vpsUp = () => {
    const parts = vpsPath.split('/').filter(Boolean);
    parts.pop();
    fetchVps('/' + parts.join('/'));
  };

  const pickRepo = (full: string) => {
    if (full === '__new__') {
      // auto-create a new private repo on save
      setCfg({ ...cfg, owner: '', repo: 'nodepanel-backups', branch: '' });
      return;
    }
    const found = ghRepos.find((x) => x.full_name === full);
    if (found) {
      const owner = full.includes('/') ? full.split('/')[0] : '';
      setCfg({ ...cfg, owner, repo: found.name, branch: found.default_branch || 'main' });
    }
  };

  const startOd = async () => {
    if (!cfg.client_id) return notify('需要 client_id', 'error');
    try {
      const r: any = await api.targets.onedriveDevice(cfg.client_id);
      setOdDevice(r);
      setOdPolling(true);
      pollOd(r.device_code);
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  const pollOd = async (code: string) => {
    for (let i = 0; i < 60; i++) {
      await new Promise((r) => setTimeout(r, 4000));
      try {
        const r: any = await api.targets.onedrivePoll(cfg.client_id, code);
        if (r.status === 'ok') {
          setCfg(r.config);
          setOdPolling(false);
          notify('OneDrive 授权成功，点击保存', 'success');
          return;
        }
      } catch {
        break;
      }
    }
    setOdPolling(false);
  };

  const save = async () => {
    try {
      let config: any = cfg;
      if (type === 'vps') {
        config = { ...cfg, user: cfg.user || 'root', port: cfg.port || 22 };
      }
      if (type === 's3') {
        // MinIO / self-hosted stores need path-style by default; the field is
        // only touched in the advanced section, so default it here on save.
        config = { ...cfg, path_style: cfg.path_style ?? true };
      }
      const payload = { type, name: name || (type === 'onedrive' ? 'OneDrive' : type), config, enabled: true };
      if (editing) {
        await api.targets.update(editing, payload);
      } else {
        await api.targets.create(payload);
      }
      close();
      load();
      notify(editing ? '已更新' : '已添加', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  return (
    <div className="card">
      <div className="spread" style={{ padding: '12px 14px' }}>
        <strong>存储目标</strong>
        <button className="btn sm primary" onClick={openAdd}><Plus size={13} /> 添加</button>
      </div>
      {targets.length === 0 ? (
        <Empty text="暂无存储目标（备份将仅本地暂存）" />
      ) : (
        <>
        <div className="desktop-only">
        <div className="scroll-x"><table className="tbl">
          <tbody>
            {targets.map((t) => (
              <tr key={t.id}>
                <td>{t.name}</td>
                <td><span className="badge muted">{t.type}</span></td>
                <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                  <button className="btn sm" onClick={async () => { try { await api.targets.test(t.id); notify('连接正常', 'success'); } catch (e: any) { notify(e?.response?.data?.error || '失败', 'error'); } }}><FlaskConical size={13} /> 测试</button>
                  <button className="icon-btn" title="编辑" onClick={() => openEdit(t)}><Pencil size={14} /></button>
                  <button className="icon-btn" title="删除" onClick={async () => { await api.targets.remove(t.id); load(); }}><Trash2 size={14} /></button>
                </td>
              </tr>
            ))}
          </tbody>
        </table></div>
        </div>
        <div className="mobile-only m-list">
          {targets.map((t) => (
            <div key={t.id} className="m-item">
              <div className="m-item-main" onClick={() => setSheet(t)} role="button" tabIndex={0}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSheet(t); } }}>
                <div className="m-item-title">{t.name}</div>
                <div className="m-item-meta"><span className="badge muted">{t.type}</span></div>
              </div>
              <button className="icon-btn m-item-more" title="更多" onClick={() => setSheet(t)}>
                <MoreVertical size={18} />
              </button>
            </div>
          ))}
        </div>
        <ActionSheet
          open={!!sheet}
          onClose={() => setSheet(null)}
          title={sheet?.name}
          subtitle={sheet?.type}
          actions={sheet ? ([
            {
              key: 'test',
              label: '测试连接',
              icon: <FlaskConical size={18} />,
              onClick: async () => {
                try { await api.targets.test(sheet.id); notify('连接正常', 'success'); }
                catch (e: any) { notify(e?.response?.data?.error || '失败', 'error'); }
              },
            },
            {
              key: 'edit',
              label: '编辑',
              icon: <Pencil size={18} />,
              onClick: () => openEdit(sheet),
            },
            {
              key: 'delete',
              label: '删除',
              icon: <Trash2 size={18} />,
              danger: true,
              onClick: async () => { await api.targets.remove(sheet.id); load(); },
            },
          ] as ActionSheetItem[]) : []}
        />
        </>
      )}

      {add && (
        <Modal title={editing ? '编辑存储目标' : '添加存储目标'} onClose={close} wide footer={<>
          <button className="btn" onClick={close}>取消</button>
          <button className="btn primary" onClick={save}>{editing ? '保存' : '添加'}</button>
        </>}>
          <div className="field">
            <label>类型</label>
            <select className="select" value={type} disabled={!!editing} onChange={(e) => { setType(e.target.value); setCfg({}); setOdDevice(null); setGhRepos([]); }}>
              <option value="github">GitHub 私有仓库</option>
              <option value="onedrive">微软云盘 OneDrive</option>
              <option value="vps">VPS (SFTP)</option>
              <option value="s3">自建对象存储 (MinIO/S3)</option>
            </select>
          </div>
          <div className="field">
            <label>名称</label>
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
          </div>

          {type === 'github' && (
            <>
              <div className="field">
                <label>GitHub 令牌 (PAT)</label>
                <input className="input" type="password" value={cfg.token || ''} onChange={(e) => { setCfg({ ...cfg, token: e.target.value }); setGhRepos([]); }} placeholder="ghp_... 或 github_pat_..." />
                <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
                  粘贴令牌后点「拉取仓库」从你账号里选仓库。令牌需含 <code>repo</code> 权限（Classic）或仓库的 Contents 写权限（Fine-grained）。
                </div>
              </div>
              <div className="field">
                <label>仓库</label>
                <div className="row" style={{ gap: 8 }}>
                  <select
                    className="select"
                    style={{ flex: 1 }}
                    value={cfg.owner && cfg.repo ? `${cfg.owner}/${cfg.repo}` : cfg.repo ? '__new__' : ''}
                    onChange={(e) => pickRepo(e.target.value)}
                  >
                    <option value="">{ghRepos.length ? '选择仓库…' : '（先点右侧拉取仓库列表）'}</option>
                    {ghRepos.map((r) => (
                      <option key={r.full_name} value={r.full_name}>{r.full_name}{r.private ? '' : '（公开）'}</option>
                    ))}
                    <option value="__new__">＋ 新建私有仓库 nodepanel-backups</option>
                  </select>
                  <button className="btn sm" onClick={fetchRepos} disabled={ghReposBusy || !cfg.token}>
                    {ghReposBusy ? '拉取中…' : '拉取仓库'}
                  </button>
                </div>
                {(cfg.owner || cfg.repo) && (
                  <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 6 }}>
                    {cfg.owner ? `当前：${cfg.owner}/${cfg.repo}（${cfg.branch || '默认分支'}）` : `将自动新建私有仓库：${cfg.repo || 'nodepanel-backups'}`}
                  </div>
                )}
              </div>
              <details style={{ marginTop: 4 }}>
                <summary style={{ cursor: 'pointer', fontSize: 12, color: 'var(--text-tertiary)' }}>高级：自定义 Owner / 分支 / 路径前缀</summary>
                <div className="row" style={{ marginTop: 8 }}>
                  <div className="field" style={{ flex: 1 }}><label>Owner</label><input className="input" value={cfg.owner || ''} onChange={(e) => setCfg({ ...cfg, owner: e.target.value })} placeholder="留空=自动识别" /></div>
                  <div className="field" style={{ flex: 1 }}><label>分支</label><input className="input" value={cfg.branch || ''} onChange={(e) => setCfg({ ...cfg, branch: e.target.value })} placeholder="留空=默认分支" /></div>
                </div>
                <div className="field"><label>仓库内路径前缀</label><input className="input" value={cfg.prefix || ''} onChange={(e) => setCfg({ ...cfg, prefix: e.target.value })} placeholder="nodepanel-backups" /></div>
              </details>
            </>
          )}

          {type === 'vps' && (
            <>
              <div className="row">
                <div className="field" style={{ flex: 2 }}><label>Host</label><input className="input" value={cfg.host || ''} onChange={(e) => setCfg({ ...cfg, host: e.target.value })} placeholder="173.1.2.3" /></div>
                <div className="field" style={{ flex: 1 }}><label>Port</label><input className="input" type="number" value={cfg.port ?? ''} onChange={(e) => setCfg({ ...cfg, port: +e.target.value || 0 })} placeholder="22" /></div>
              </div>
              <div className="field"><label>用户</label><input className="input" value={cfg.user || ''} onChange={(e) => setCfg({ ...cfg, user: e.target.value })} placeholder="root" /></div>
              <div className="field"><label>密码（与密钥二选一）</label><input className="input" type="password" value={cfg.password || ''} onChange={(e) => setCfg({ ...cfg, password: e.target.value })} /></div>
              <div className="field"><label>私钥（PEM，可选）</label><textarea className="textarea" value={cfg.key_pem || ''} onChange={(e) => setCfg({ ...cfg, key_pem: e.target.value })} /></div>
              <div className="field">
                <label>远端目录</label>
                <div className="row" style={{ gap: 8 }}>
                  <input className="input" style={{ flex: 1 }} value={cfg.base_dir || ''} onChange={(e) => setCfg({ ...cfg, base_dir: e.target.value })} placeholder="/srv/nodepanel-backups" />
                  <button className="btn sm" onClick={openVpsBrowse}><Folder size={13} /> 浏览</button>
                </div>
              </div>
            </>
          )}

          {type === 's3' && (
            <>
              <div className="field"><label>Endpoint（含 https:// 或 http://）</label><input className="input" value={cfg.endpoint || ''} onChange={(e) => setCfg({ ...cfg, endpoint: e.target.value })} placeholder="https://minio.example.com 或 1.2.3.4:9000" /></div>
              <div className="row">
                <div className="field" style={{ flex: 1 }}><label>Access Key</label><input className="input" value={cfg.access_key || ''} onChange={(e) => setCfg({ ...cfg, access_key: e.target.value })} /></div>
                <div className="field" style={{ flex: 1 }}><label>Secret Key</label><input className="input" type="password" value={cfg.secret_key || ''} onChange={(e) => setCfg({ ...cfg, secret_key: e.target.value })} /></div>
              </div>
              <div className="row">
                <div className="field" style={{ flex: 1 }}><label>Bucket</label><input className="input" value={cfg.bucket || ''} onChange={(e) => setCfg({ ...cfg, bucket: e.target.value })} placeholder="nodepanel-backups" /></div>
                <div className="field" style={{ flex: 1 }}><label>路径前缀（可选）</label><input className="input" value={cfg.prefix || ''} onChange={(e) => setCfg({ ...cfg, prefix: e.target.value })} placeholder="留空=根目录" /></div>
              </div>
              <details style={{ marginTop: 4 }}>
                <summary style={{ cursor: 'pointer', fontSize: 12, color: 'var(--text-tertiary)' }}>高级：Region / Path-Style / TLS</summary>
                <div className="field"><label>Region（可选，MinIO 可留空）</label><input className="input" value={cfg.region || ''} onChange={(e) => setCfg({ ...cfg, region: e.target.value })} /></div>
                <div className="field" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <input id="s3-pathstyle" type="checkbox" checked={cfg.path_style ?? true} onChange={(e) => setCfg({ ...cfg, path_style: e.target.checked })} />
                  <label htmlFor="s3-pathstyle" style={{ margin: 0 }}>Path-Style 寻址（MinIO / 自建建议勾选）</label>
                </div>
                {(!cfg.endpoint || (!cfg.endpoint.startsWith('http://') && !cfg.endpoint.startsWith('https://'))) && (
                  <div className="field" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <input id="s3-secure" type="checkbox" checked={cfg.secure ?? true} onChange={(e) => setCfg({ ...cfg, secure: e.target.checked })} />
                    <label htmlFor="s3-secure" style={{ margin: 0 }}>启用 TLS（endpoint 无 scheme 时生效）</label>
                  </div>
                )}
                <div className="field" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <input id="s3-insecure" type="checkbox" checked={cfg.insecure_skip_verify ?? false} onChange={(e) => setCfg({ ...cfg, insecure_skip_verify: e.target.checked })} />
                  <label htmlFor="s3-insecure" style={{ margin: 0 }}>跳过 TLS 证书校验（自签证书 / 直连 IP 时勾选）</label>
                </div>
              </details>
            </>
          )}

          {type === 'onedrive' && (
            <>
              <div className="field"><label>应用 Client ID（Azure 公共客户端）</label><input className="input" value={cfg.client_id || ''} onChange={(e) => setCfg({ ...cfg, client_id: e.target.value, folder: 'NodePanel/backups' })} /></div>
              {!cfg.refresh_token ? (
                <button className="btn primary" onClick={startOd} disabled={odPolling}>{odPolling ? '等待授权…' : '开始设备授权'}</button>
              ) : (
                <span className="badge success">已授权 ✓</span>
              )}
              {odDevice && (
                <div className="card" style={{ padding: 12, marginTop: 10, background: 'var(--bg-tertiary)' }}>
                  <div className="row" style={{ justifyContent: 'space-between' }}>
                    <strong>访问码：{odDevice.user_code}</strong>
                    <CopyButton text={odDevice.user_code} />
                  </div>
                  <div style={{ marginTop: 6 }}>
                    打开 <a href={odDevice.verification_uri} target="_blank" rel="noreferrer" style={{ color: 'var(--primary)' }}>{odDevice.verification_uri}</a> 输入上方访问码完成授权。
                  </div>
                </div>
              )}
            </>
          )}
        </Modal>
      )}

      {vpsBrowse && (
        <Modal
          title="选择远端目录"
          onClose={() => setVpsBrowse(false)}
          wide
          footer={
            <>
              <button className="btn" onClick={() => setVpsBrowse(false)}>取消</button>
              <button
                className="btn primary"
                onClick={() => { setCfg({ ...cfg, base_dir: vpsPath }); setVpsBrowse(false); notify('已选择 ' + vpsPath, 'success'); }}
              >
                选择此目录
              </button>
            </>
          }
        >
          <div className="row" style={{ alignItems: 'center', gap: 8, marginBottom: 6 }}>
            <button className="btn sm ghost" onClick={vpsUp} title="上级" disabled={vpsPath === '/' || vpsBusy}>
              <ChevronUp size={14} />
            </button>
            <span className="mono" style={{ fontSize: 13, wordBreak: 'break-all' }}>{vpsPath}</span>
          </div>
          {vpsCurrent && vpsCurrent.total > 0 && (
            <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginBottom: 8 }}>
              当前磁盘：已用 {bytes(vpsCurrent.used)} / {bytes(vpsCurrent.total)}（空闲 {bytes(vpsCurrent.free)}）
            </div>
          )}

          {vpsMounts.length > 0 && (
            <div style={{ marginBottom: 10 }}>
              <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginBottom: 6 }}>磁盘（点击跳转，按容量降序）</div>
              <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
                {vpsMounts.map((m) => (
                  <button
                    key={m.path}
                    className={`btn sm ${vpsPath === m.path ? 'primary' : ''}`}
                    title={`${m.device || '?'} · ${m.fs_type || ''}`}
                    onClick={() => fetchVps(m.path)}
                    disabled={vpsBusy}
                  >
                    <HardDrive size={12} /> {m.path}{m.total ? ` · ${bytes(m.total)}` : ''}
                  </button>
                ))}
              </div>
            </div>
          )}

          <div style={{ maxHeight: 280, overflowY: 'auto', border: '1px solid var(--border-color)', borderRadius: 8 }}>
            {vpsBusy ? (
              <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-tertiary)' }}>读取中…</div>
            ) : vpsEntries.filter((e) => e.is_dir).length === 0 ? (
              <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-tertiary)' }}>无子目录</div>
            ) : (
              vpsEntries.filter((e) => e.is_dir).map((e) => (
                <div
                  key={e.path}
                  className="row"
                  style={{ padding: '8px 12px', cursor: 'pointer', borderBottom: '1px solid var(--border-color)' }}
                  onClick={() => fetchVps(e.path)}
                >
                  <Folder size={14} color="var(--primary)" />
                  <span style={{ fontSize: 13 }}>{e.name}</span>
                </div>
              ))
            )}
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 8 }}>
            点击目录进入下级，用左上角 ↑ 返回上级，确认后点「选择此目录」。
          </div>
        </Modal>
      )}
    </div>
  );
}

function ScheduleTab() {
  const [settings, setSettings] = useState<any>({});
  const [schedules, setSchedules] = useState<any[]>([]);
  const [nodes, setNodes] = useState<any[]>([]);
  const [targets, setTargets] = useState<any[]>([]);
  const [containers, setContainers] = useState<any[]>([]);
  const [ret, setRet] = useState({ keep_count: 7, keep_days: 30 });
  const [exc, setExc] = useState(''); // backup exclude paths, one per line
  const [add, setAdd] = useState(false);
  const [editing, setEditing] = useState<any | null>(null);
  const [sheet, setSheet] = useState<any | null>(null);
  const blank = () => ({ type: 'backup', bsource: 'container' as 'container' | 'dir', node_ids: [] as string[], target_ids: [] as string[], container_ids: [] as string[], paths: '/etc/nginx', days: [1, 2, 3, 4, 5], hour: 3, minute: 0 });
  const [form, setForm] = useState<any>(blank());

  const load = () => {
    api.settings.all().then((r: any) => {
      setSettings(r);
      if (r.backup_retention) setRet(r.backup_retention);
      if (Array.isArray(r.backup_excludes)) setExc(r.backup_excludes.join('\n'));
    });
    api.schedules.list().then((r: any) => setSchedules(Array.isArray(r) ? r : []));
    api.nodes.list().then((r: any) => setNodes(Array.isArray(r) ? r : []));
    api.targets.list().then((r: any) => setTargets(Array.isArray(r) ? r : []));
    api.containers.list().then((r: any) => setContainers(Array.isArray(r) ? r : []));
  };
  useEffect(() => {
    load();
  }, []);

  const saveRet = async () => {
    try {
      await api.settings.putRetention(ret.keep_count, ret.keep_days);
      notify('保留策略已保存', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  const saveExc = async () => {
    try {
      const list = exc.split('\n').map((s) => s.trim()).filter(Boolean);
      await api.settings.putExcludes(list);
      notify('排除路径已保存', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  const parseCron = (c: string) => {
    const p = (c || '').split(/\s+/);
    const minute = p[0] && !isNaN(+p[0]) ? +p[0] : 0;
    const hour = p[1] && !isNaN(+p[1]) ? +p[1] : 3;
    let days: number[] = [];
    if (p[4] && p[4] !== '*') days = p[4].split(',').map(Number).filter((n) => !isNaN(n));
    return { minute, hour, days };
  };

  const openAdd = () => { setEditing(null); setForm(blank()); setAdd(true); };

  const openEdit = (s: any) => {
    let cfg: any = {};
    try { cfg = JSON.parse(s.config || '{}'); } catch { /* ignore */ }
    const { minute, hour, days } = parseCron(s.cron);
    // New multi-target Config, with legacy single-value fallback.
    const backupConts: any[] = Array.isArray(cfg.containers) && s.type === 'backup'
      ? cfg.containers
      : (cfg.container ? [{ container_id: cfg.container }] : []);
    const updateConts: any[] = Array.isArray(cfg.containers) && s.type === 'container_update' ? cfg.containers : [];
    const resolveIds = (conts: any[]) => conts.map((x: any) => {
      const live = containers.find(
        (c: any) => c.node_id === x.node_id && (c.name === x.name || c.container_id === x.container_id),
      );
      return live?.container_id || x.container_id;
    }).filter(Boolean);
    const nodeIds: string[] = Array.isArray(cfg.node_ids) && cfg.node_ids.length ? cfg.node_ids : (s.node_id ? [s.node_id] : []);
    setEditing(s);
    setForm({
      type: s.type,
      bsource: backupConts.length || s.type === 'container_update' ? 'container' : 'dir',
      node_ids: nodeIds,
      target_ids: cfg.target_ids || (cfg.target_id ? [cfg.target_id] : []),
      container_ids: s.type === 'container_update' ? resolveIds(updateConts) : backupConts.map((x: any) => x.container_id),
      paths: Array.isArray(cfg.paths) ? cfg.paths.join('\n') : '/etc/nginx',
      days, hour, minute,
    });
    setAdd(true);
  };

  const close = () => { setAdd(false); setEditing(null); };

  const saveSchedule = async () => {
    const timing = { days: form.days, hour: form.hour, minute: form.minute, enabled: true };
    try {
      // One schedule covers every selected object. Containers/nodes are stored
      // as lists in Config instead of fanning out into many near-identical rows.
      let cfg: any;
      if (form.type === 'backup' && form.bsource === 'container') {
        if (!form.container_ids || form.container_ids.length === 0) { notify('请选择容器', 'error'); return; }
        const selected = containers.filter((c) => form.container_ids.includes(c.container_id));
        if (selected.length === 0) { notify('容器不存在', 'error'); return; }
        cfg = {
          containers: selected.map((c: any) => ({ node_id: c.node_id, container_id: c.container_id, name: c.name })),
          target_ids: form.target_ids,
        };
      } else if (form.type === 'backup') {
        if (!form.node_ids || form.node_ids.length === 0) { notify('请选择节点', 'error'); return; }
        cfg = { paths: form.paths.split('\n').filter(Boolean), node_ids: form.node_ids, target_ids: form.target_ids };
      } else {
        // container_update — select specific containers (same model as Settings)
        if (!form.container_ids || form.container_ids.length === 0) { notify('请选择容器', 'error'); return; }
        const selected = containers.filter((c) => form.container_ids.includes(c.container_id));
        if (selected.length === 0) { notify('容器不存在', 'error'); return; }
        cfg = {
          containers: selected.map((c: any) => ({ node_id: c.node_id, container_id: c.container_id, name: c.name })),
        };
      }
      if (editing) {
        await api.schedules.update(editing.id, { type: form.type, node_id: '', config: cfg, ...timing });
        notify('计划已更新', 'success');
      } else {
        await api.schedules.create({ type: form.type, node_id: '', config: cfg, ...timing });
        notify('计划已创建', 'success');
      }
      close();
      load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };

  const toggleDay = (d: number) => {
    setForm((f: any) => ({ ...f, days: f.days.includes(d) ? f.days.filter((x: number) => x !== d) : [...f.days, d] }));
  };

  return (
    <div style={{ display: 'grid', gap: 18 }}>
      <div className="card" style={{ padding: 16 }}>
        <h3 style={{ margin: '0 0 12px', fontSize: 15 }}>保留策略</h3>
        <div className="row" style={{ gap: 16 }}>
          <div className="field" style={{ marginBottom: 0 }}>
            <label>保留最新份数</label>
            <input className="input" type="number" min={0} style={{ width: 120 }} value={ret.keep_count} onChange={(e) => setRet({ ...ret, keep_count: +e.target.value })} />
          </div>
          <div className="field" style={{ marginBottom: 0 }}>
            <label>保留天数</label>
            <input className="input" type="number" min={0} style={{ width: 120 }} value={ret.keep_days} onChange={(e) => setRet({ ...ret, keep_days: +e.target.value })} />
          </div>
          <button className="btn primary" onClick={saveRet} style={{ alignSelf: 'flex-end' }}>保存</button>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 10 }}>
          填 <code>0</code> 表示该项不限：份数 0 = 不限份数，天数 0 = 不限天数；<strong>两项都填 0 = 永久保存（不清理）</strong>。
        </div>
      </div>

      <div className="card" style={{ padding: 16 }}>
        <h3 style={{ margin: '0 0 4px', fontSize: 15 }}>备份排除路径</h3>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginBottom: 10 }}>
          打包容器卷时跳过这些<strong>宿主机路径前缀</strong>（每行一个）。用于剔除循环/臃肿目录，例如 nodepanel 备份排除 <code>/var/lib/nodepanel/backups</code>（其它归档）和 <code>/var/lib/nodepanel/agents</code>，只留 DB。
        </div>
        <div className="row" style={{ gap: 12, alignItems: 'flex-end' }}>
          <div className="field" style={{ flex: 1, marginBottom: 0 }}>
            <textarea className="textarea" rows={3} value={exc} onChange={(e) => setExc(e.target.value)} placeholder={'/var/lib/nodepanel/backups\n/var/lib/nodepanel/agents'} />
          </div>
          <button className="btn primary" onClick={saveExc}>保存</button>
        </div>
      </div>

      <div className="card">
        <div className="spread" style={{ padding: '12px 14px' }}>
          <strong>计划任务（备份 / 容器更新）</strong>
          <button className="btn sm primary" onClick={openAdd}><Plus size={13} /> 添加</button>
        </div>
        {schedules.length === 0 ? (
          <Empty text="暂无计划任务" />
        ) : (
          <>
          <div className="desktop-only">
          <div className="scroll-x"><table className="tbl">
            <thead><tr><th>类型</th><th>节点</th><th>目标</th><th>Cron</th><th>启用</th><th></th></tr></thead>
            <tbody>
              {schedules.map((s) => {
                let cfg: any = {};
                try { cfg = JSON.parse(s.config || '{}'); } catch { /* ignore */ }
                const tids: string[] = cfg.target_ids || (cfg.target_id ? [cfg.target_id] : []);
                const tgtLabel = tids.length ? tids.map((id) => targets.find((t) => t.id === id)?.name || id.slice(0, 6)).join('、') : '仅本地';
                const conts: any[] = Array.isArray(cfg.containers) ? cfg.containers : (cfg.container ? [{ container_id: cfg.container }] : []);
                const nodeIds: string[] = Array.isArray(cfg.node_ids) && cfg.node_ids.length
                  ? cfg.node_ids
                  : (conts.length ? conts.map((x: any) => x.node_id).filter(Boolean) : (s.node_id ? [s.node_id] : []));
                const nodeLabel = nodeIds.length <= 1
                  ? (nodes.find((n) => n.id === nodeIds[0])?.name || nodeIds[0]?.slice(0, 8) || '全局')
                  : `${nodeIds.length} 节点`;
                let typeLabel = '目录备份';
                let targetStr = '';
                if (s.type === 'container_update') {
                  typeLabel = '容器更新';
                  if (conts.length) {
                    const names = conts.map((x: any) =>
                      containers.find((c) => c.node_id === x.node_id && (c.name === x.name || c.container_id === x.container_id))?.display_name
                      || containers.find((c) => c.node_id === x.node_id && (c.name === x.name || c.container_id === x.container_id))?.name
                      || x.name || (x.container_id || '').slice(0, 8)
                    ).filter(Boolean);
                    targetStr = `自动更新 ${names.length} 个${names.length ? '（' + names.slice(0, 2).join('、') + (names.length > 2 ? ' 等' : '') + '）' : ''}`;
                  } else {
                    targetStr = cfg.label ? 'label=' + cfg.label : '全部容器（旧）';
                  }
                } else if (conts.length) {
                  typeLabel = '容器备份';
                  const names = conts.map((x: any) => containers.find((c) => c.container_id === x.container_id)?.display_name || containers.find((c) => c.container_id === x.container_id)?.name || (x.container_id || '').slice(0, 8)).filter(Boolean);
                  targetStr = `容器 ${names.length} 个${names.length ? '（' + names.slice(0, 2).join('、') + (names.length > 2 ? ' 等' : '') + '）' : ''} → ${tgtLabel}`;
                } else {
                  typeLabel = '目录备份';
                  const pc = Array.isArray(cfg.paths) ? cfg.paths.length : 0;
                  targetStr = `${pc} 目录 → ${tgtLabel}`;
                }
                return (
                  <tr key={s.id}>
                    <td><span className="badge muted">{typeLabel}</span></td>
                    <td>{nodeLabel}</td>
                    <td style={{ fontSize: 12 }}>{targetStr}</td>
                    <td className="mono">{s.cron}</td>
                    <td>{s.enabled ? '✓' : '—'}</td>
                    <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                      <button className="icon-btn" title="编辑" onClick={() => openEdit(s)}><Pencil size={14} /></button>
                      <button className="icon-btn" title="删除" onClick={async () => { await api.schedules.remove(s.id); load(); }}><Trash2 size={14} /></button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table></div>
          </div>
          <div className="mobile-only m-list">
            {schedules.map((s) => {
              let cfg: any = {};
              try { cfg = JSON.parse(s.config || '{}'); } catch { /* ignore */ }
              const tids: string[] = cfg.target_ids || (cfg.target_id ? [cfg.target_id] : []);
              const tgtLabel = tids.length ? tids.map((id) => targets.find((t) => t.id === id)?.name || id.slice(0, 6)).join('、') : '仅本地';
              const conts: any[] = Array.isArray(cfg.containers) ? cfg.containers : (cfg.container ? [{ container_id: cfg.container }] : []);
              const nodeIds: string[] = Array.isArray(cfg.node_ids) && cfg.node_ids.length
                ? cfg.node_ids
                : (conts.length ? conts.map((x: any) => x.node_id).filter(Boolean) : (s.node_id ? [s.node_id] : []));
              const nodeLabel = nodeIds.length <= 1
                ? (nodes.find((n) => n.id === nodeIds[0])?.name || nodeIds[0]?.slice(0, 8) || '全局')
                : `${nodeIds.length} 节点`;
              let typeLabel = '目录备份';
              let targetStr = '';
              if (s.type === 'container_update') {
                typeLabel = '容器更新';
                if (conts.length) {
                  const names = conts.map((x: any) =>
                    containers.find((c) => c.node_id === x.node_id && (c.name === x.name || c.container_id === x.container_id))?.display_name
                    || containers.find((c) => c.node_id === x.node_id && (c.name === x.name || c.container_id === x.container_id))?.name
                    || x.name || (x.container_id || '').slice(0, 8)
                  ).filter(Boolean);
                  targetStr = `自动更新 ${names.length} 个${names.length ? '（' + names.slice(0, 2).join('、') + (names.length > 2 ? ' 等' : '') + '）' : ''}`;
                } else {
                  targetStr = cfg.label ? 'label=' + cfg.label : '全部容器（旧）';
                }
              } else if (conts.length) {
                typeLabel = '容器备份';
                const names = conts.map((x: any) => containers.find((c) => c.container_id === x.container_id)?.display_name || containers.find((c) => c.container_id === x.container_id)?.name || (x.container_id || '').slice(0, 8)).filter(Boolean);
                targetStr = `容器 ${names.length} 个${names.length ? '（' + names.slice(0, 2).join('、') + (names.length > 2 ? ' 等' : '') + '）' : ''} → ${tgtLabel}`;
              } else {
                typeLabel = '目录备份';
                const pc = Array.isArray(cfg.paths) ? cfg.paths.length : 0;
                targetStr = `${pc} 目录 → ${tgtLabel}`;
              }
              return (
                <div key={s.id} className="m-item">
                  <div className="m-item-main" onClick={() => setSheet({ ...s, _typeLabel: typeLabel, _nodeLabel: nodeLabel, _targetStr: targetStr })} role="button" tabIndex={0}
                    onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSheet({ ...s, _typeLabel: typeLabel, _nodeLabel: nodeLabel, _targetStr: targetStr }); } }}>
                    <div className="m-item-title">{typeLabel}</div>
                    <div className="m-item-sub">{nodeLabel} · {targetStr}</div>
                    <div className="m-item-meta">
                      <span className="mono" style={{ fontSize: 12 }}>{s.cron}</span>
                      <span className="badge muted">{s.enabled ? '启用' : '停用'}</span>
                    </div>
                  </div>
                  <button className="icon-btn m-item-more" title="更多" onClick={() => setSheet({ ...s, _typeLabel: typeLabel, _nodeLabel: nodeLabel, _targetStr: targetStr })}>
                    <MoreVertical size={18} />
                  </button>
                </div>
              );
            })}
          </div>
          <ActionSheet
            open={!!sheet}
            onClose={() => setSheet(null)}
            title={sheet?._typeLabel || '计划任务'}
            subtitle={sheet ? `${sheet._nodeLabel} · ${sheet.cron}` : undefined}
            actions={sheet ? ([
              {
                key: 'edit',
                label: '编辑',
                icon: <Pencil size={18} />,
                onClick: () => openEdit(sheet),
              },
              {
                key: 'delete',
                label: '删除',
                icon: <Trash2 size={18} />,
                danger: true,
                onClick: async () => { await api.schedules.remove(sheet.id); load(); },
              },
            ] as ActionSheetItem[]) : []}
          >
            {sheet && (
              <div className="kv-row">
                <span>目标</span>
                <span style={{ textAlign: 'right' }}>{sheet._targetStr}</span>
              </div>
            )}
          </ActionSheet>
          </>
        )}
      </div>

      {add && (
        <Modal title={editing ? '编辑计划任务' : '添加计划任务'} onClose={close} wide footer={<>
          <button className="btn" onClick={close}>取消</button>
          <button className="btn primary" onClick={saveSchedule}>{editing ? '保存' : '创建'}</button>
        </>}>
          <div className="field">
            <label>类型</label>
            <select className="select" value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value })}>
              <option value="backup">备份</option>
              <option value="container_update">容器自动更新</option>
            </select>
          </div>

          {form.type === 'backup' && (
            <div className="field">
              <label>备份对象</label>
              <div className="row" style={{ gap: 8 }}>
                <button className={`btn sm ${form.bsource === 'container' ? 'primary' : ''}`} onClick={() => setForm({ ...form, bsource: 'container' })}><Box size={13} /> 容器（带数据）</button>
                <button className={`btn sm ${form.bsource === 'dir' ? 'primary' : ''}`} onClick={() => setForm({ ...form, bsource: 'dir' })}><Folder size={13} /> 目录</button>
              </div>
            </div>
          )}

          {form.type === 'backup' && form.bsource === 'container' && (
            <div className="field">
              <label>容器（可多选，一条计划覆盖所有选中容器）</label>
              <MultiSelect
                items={containers.map((c) => ({
                  id: c.container_id,
                  label: c.display_name || c.name,
                  sub: nodes.find((n) => n.id === c.node_id)?.name || c.node_id?.slice(0, 6),
                }))}
                value={form.container_ids}
                onChange={(ids) => setForm({ ...form, container_ids: ids })}
                placeholder="选择容器…"
                title="选择容器"
                emptyText="暂无容器（节点未上报或未安装 Docker）"
              />
            </div>
          )}

          {form.type === 'container_update' && (
            <div className="field">
              <label>自动更新容器（可多选，与「设置 → 容器更新」相同）</label>
              <MultiSelect
                items={containers
                  .filter((c) => c.update_type === 'latest' || c.update_type === 'tag')
                  .map((c) => ({
                    id: c.container_id,
                    label: c.display_name || c.name,
                    sub: `${nodes.find((n) => n.id === c.node_id)?.name || c.node_id?.slice(0, 6)} · ${c.image || ''}`,
                  }))}
                value={form.container_ids}
                onChange={(ids) => setForm({ ...form, container_ids: ids })}
                placeholder="从容器列表选择…"
                title="选择自动更新容器"
                emptyText="暂无可自动更新的容器"
              />
            </div>
          )}

          {form.type === 'backup' && form.bsource !== 'container' && (
            <div className="field">
              <label>节点（可多选，一条计划覆盖所有选中节点）</label>
              <NodeSelect nodes={nodes} value={form.node_ids} onChange={(ids) => setForm({ ...form, node_ids: ids })} />
            </div>
          )}

          {form.type === 'backup' && form.bsource === 'dir' && (
            <div className="field"><label>备份目录（每行一个）</label><textarea className="textarea" value={form.paths} onChange={(e) => setForm({ ...form, paths: e.target.value })} /></div>
          )}

          {form.type === 'backup' && (
            <div className="field">
              <label>存储目标（可多选，同一备份会推送到所有选中目标）</label>
              <MultiSelect
                items={targets.map((t) => ({ id: t.id, label: t.name, sub: t.type }))}
                value={form.target_ids}
                onChange={(ids) => setForm({ ...form, target_ids: ids })}
                placeholder="仅本地（不推送远端）"
                title="选择存储目标"
                emptyText="暂无存储目标，先到「存储目标」添加"
              />
            </div>
          )}

          <div className="field">
            <label>每周（留空=每天）</label>
            <div className="day-picker">
              {WEEK.map((w, i) => (
                <button key={i} type="button" className={`btn sm ${form.days.includes(i) ? 'primary' : ''}`} onClick={() => toggleDay(i)}>{w}</button>
              ))}
            </div>
          </div>
          <div className="row">
            <div className="field" style={{ flex: 1 }}><label>小时</label><input className="input" type="number" min={0} max={23} value={form.hour} onChange={(e) => setForm({ ...form, hour: +e.target.value })} /></div>
            <div className="field" style={{ flex: 1 }}><label>分钟</label><input className="input" type="number" min={0} max={59} value={form.minute} onChange={(e) => setForm({ ...form, minute: +e.target.value })} /></div>
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: -4 }}>
            执行时间按 <strong>北京时间（UTC+8）</strong>。例如 小时 3 分钟 0 = 每到选定的星期几凌晨 3:00（北京）。
          </div>
        </Modal>
      )}
    </div>
  );
}
