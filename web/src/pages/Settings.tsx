import { useEffect, useState } from 'react';
import { Save, Send, RefreshCw, Github, UploadCloud, Cloud, Radar } from 'lucide-react';
import { api } from '../services/api';
import { notify } from '../stores';
import { MultiSelect } from '../components/MultiSelect';

export function SettingsPage() {
  const [tab, setTab] = useState('account');
  return (
    <div>
      <h1 className="page-title">设置</h1>
      <p className="page-subtitle">账户、通知、容器更新、Cloudflare 与 GitHub 集成</p>
      <div className="tab-bar" role="tablist">
        {[
          ['account', '账户'],
          ['notify', 'Telegram'],
          ['container', '容器更新'],
          ['monitor', '容器监控'],
          ['backup', 'GitHub'],
          ['cloudflare', 'Cloudflare'],
          ['komari', 'Komari'],
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
      {tab === 'account' && <AccountTab />}
      {tab === 'notify' && <NotifyTab />}
      {tab === 'container' && <ContainerTab />}
      {tab === 'monitor' && <MonitorTab />}
      {tab === 'backup' && <GithubTab />}
      {tab === 'cloudflare' && <CloudflareTab />}
      {tab === 'komari' && <KomariTab />}
    </div>
  );
}

function AccountTab() {
  const [username, setUsername] = useState('');
  const [pwd, setPwd] = useState('');
  useEffect(() => {
    api.settings.all().then((r: any) => setUsername(r.account?.username || ''));
  }, []);
  const save = async () => {
    try {
      await api.settings.putAccount(username, pwd);
      setPwd('');
      notify('账户已更新', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };
  return (
    <div className="card" style={{ padding: 18, maxWidth: 460 }}>
      <div className="field">
        <label>用户名</label>
        <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} />
      </div>
      <div className="field">
        <label>新密码（留空则不修改）</label>
        <input className="input" type="password" value={pwd} onChange={(e) => setPwd(e.target.value)} />
      </div>
      <button className="btn primary" onClick={save}>
        <Save size={14} /> 保存
      </button>
    </div>
  );
}

function NotifyTab() {
  const [bot, setBot] = useState('');
  const [chat, setChat] = useState('');
  useEffect(() => {
    api.settings.all().then((r: any) => {
      setBot(r.telegram?.bot_token || '');
      setChat(r.telegram?.chat_id || '');
    });
  }, []);
  const save = async () => {
    try {
      await api.settings.putTelegram(bot, chat);
      notify('已保存', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    }
  };
  const test = async () => {
    try {
      await api.settings.testTelegram(bot, chat);
      notify('测试消息已发送', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '发送失败', 'error');
    }
  };
  return (
    <div className="card" style={{ padding: 18, maxWidth: 520 }}>
      <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
        通过 @BotFather 创建机器人获取 token，chat_id 可用 @userinfobot 查询。节点上下线、备份结果、容器更新等会推送通知。
      </p>
      <div className="field">
        <label>Bot Token</label>
        <input className="input" value={bot} onChange={(e) => setBot(e.target.value)} placeholder="123456:ABC-DEF..." />
      </div>
      <div className="field">
        <label>Chat ID</label>
        <input className="input" value={chat} onChange={(e) => setChat(e.target.value)} placeholder="例如 100123456" />
      </div>
      <div className="row">
        <button className="btn primary" onClick={save}>
          <Save size={14} /> 保存
        </button>
        <button className="btn" onClick={test}>
          <Send size={14} /> 发送测试
        </button>
      </div>
    </div>
  );
}

function ContainerTab() {
  const [nodes, setNodes] = useState<any[]>([]);
  const [containers, setContainers] = useState<any[]>([]);
  const [sched, setSched] = useState<any | null>(null);
  const [containerIds, setContainerIds] = useState<string[]>([]);
  const [intervalHours, setIntervalHours] = useState(4);
  const [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false);

  const resolveContainerIds = (cfg: any, inventory: any[]) => {
    const conts: any[] = Array.isArray(cfg?.containers) ? cfg.containers : [];
    if (conts.length === 0) return [] as string[];
    return conts.map((x: any) => {
      const live = inventory.find(
        (c: any) =>
          c.node_id === x.node_id &&
          (c.name === x.name || c.container_id === x.container_id),
      );
      return live?.container_id || x.container_id;
    }).filter(Boolean);
  };

  useEffect(() => {
    Promise.all([api.nodes.list(), api.containers.list(), api.schedules.list()]).then(
      ([nodeList, contList, schedList]: any[]) => {
        const ns = Array.isArray(nodeList) ? nodeList : [];
        const cs = Array.isArray(contList) ? contList : [];
        setNodes(ns);
        setContainers(cs);
        const cu = (Array.isArray(schedList) ? schedList : []).find((s: any) => s.type === 'container_update');
        if (!cu) return;
        setSched(cu);
        let cfg: any = {};
        try { cfg = JSON.parse(cu.config || '{}'); } catch { /* ignore */ }
        setContainerIds(resolveContainerIds(cfg, cs));
        setEnabled(!!cu.enabled);
        const p = (cu.cron || '').split(/\s+/);
        const m = /^\*\/(\d+)$/.exec(p[1] || '');
        setIntervalHours(m ? Math.min(23, Math.max(1, +m[1])) : 4);
      },
    );
  }, []);

  // Same pool as「容器」板块：仅 latest/tag 可安全自动拉取重建。
  const selectable = containers.filter((c) => c.update_type === 'latest' || c.update_type === 'tag');

  const save = async () => {
    if (containerIds.length === 0) return notify('请选择要自动更新的容器', 'error');
    const selected = containers.filter((c) => containerIds.includes(c.container_id));
    if (selected.length === 0) return notify('所选容器不存在或已消失', 'error');
    setBusy(true);
    try {
      const cron = `0 */${Math.min(23, Math.max(1, intervalHours || 4))} * * *`;
      const body = {
        type: 'container_update',
        node_id: '',
        config: {
          containers: selected.map((c: any) => ({
            node_id: c.node_id,
            container_id: c.container_id,
            name: c.name,
          })),
        },
        cron,
        enabled,
      };
      if (sched) {
        await api.schedules.update(sched.id, body);
        setSched({
          ...sched,
          type: body.type,
          node_id: body.node_id,
          config: JSON.stringify(body.config),
          cron,
          enabled,
        });
        notify('计划已更新', 'success');
      } else {
        const created = await api.schedules.create(body);
        setSched({
          ...(created && typeof created === 'object' ? created : {}),
          type: body.type,
          node_id: body.node_id,
          config: typeof created?.config === 'string' ? created.config : JSON.stringify(body.config),
          cron: created?.cron || cron,
          enabled: created?.enabled ?? enabled,
        });
        notify('计划已创建', 'success');
      }
      try {
        const list: any = await api.schedules.list();
        const cu = (Array.isArray(list) ? list : []).find((s: any) => s.type === 'container_update');
        if (cu) {
          setSched(cu);
          let cfg: any = {};
          try { cfg = JSON.parse(cu.config || '{}'); } catch { /* ignore */ }
          setContainerIds(resolveContainerIds(cfg, containers));
        }
      } catch { /* ignore refresh errors; local state already updated */ }
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="card" style={{ padding: 18, maxWidth: 560 }}>
      <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
        定时（北京时间）扫描<strong>所选</strong>运行中 registry 容器，有新版本则<strong>自动拉取并重建更新</strong>，结果通过 Telegram 推送（需先在「Telegram 通知」标签配置机器人）。
        列表与「容器」板块一致，仅含可自动更新的 Compose Registry 镜像（latest/tag）；固定摘要、源码构建和本地镜像不会出现。节点 agent 需 ≥ 2.4.0。
      </p>
      {sched && (
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginBottom: 12 }}>
          当前计划：cron <span className="mono">{sched.cron}</span>
          {sched.last_run ? ` · 上次运行 ${new Date(sched.last_run * 1000).toLocaleString('zh-CN')}` : ' · 未运行过'}
          {sched.enabled ? '' : ' · 已停用'}
        </div>
      )}
      <div className="field">
        <label>启用</label>
        <button className={`btn sm ${enabled ? 'primary' : ''}`} onClick={() => setEnabled((v) => !v)}>
          {enabled ? '已启用' : '已停用'}
        </button>
      </div>
      <div className="field">
        <label>自动更新容器（可多选，运行时离线节点自动跳过）</label>
        <MultiSelect
          items={selectable.map((c) => ({
            id: c.container_id,
            label: c.display_name || c.name,
            sub: `${nodes.find((n) => n.id === c.node_id)?.name || c.node_id?.slice(0, 6)} · ${c.image || ''}`,
          }))}
          value={containerIds}
          onChange={setContainerIds}
          placeholder="从容器列表选择…"
          title="选择自动更新容器"
          emptyText="暂无可自动更新的容器（需节点上报 Compose Registry 镜像）"
        />
      </div>
      <div className="field">
        <label>每隔几小时检查更新</label>
        <input className="input" type="number" min={1} max={23} value={intervalHours} onChange={(e) => setIntervalHours(+e.target.value)} />
      </div>
      <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: -4 }}>
        按 <strong>北京时间</strong> 每隔指定小时数扫描一次（在 0/N/2N… 点触发）。例如填 4 = 每天 0/4/8/12/16/20 点各检查一次。
      </div>
      <button className="btn primary" onClick={save} disabled={busy} style={{ marginTop: 14 }}>
        <Save size={14} /> {busy ? '保存中…' : (sched ? '保存修改' : '创建定时更新计划')}
      </button>
    </div>
  );
}

function MonitorTab() {
  const [enabled, setEnabled] = useState(false);
  const [interval, setIntervalV] = useState(60);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    api.settings.all().then((r: any) => {
      const m = r?.container_monitor;
      setEnabled(!!m?.enabled);
      setIntervalV(m?.interval_seconds || 60);
    });
  }, []);
  const save = async () => {
    setSaving(true);
    try {
      await api.settings.putContainerMonitor(enabled, Number(interval) || 60);
      notify('已保存', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '失败', 'error');
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="card" style={{ padding: 18, maxWidth: 520 }}>
      <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
        定时扫描各节点容器，当容器进入 <code>exited</code> / <code>dead</code> / <code>restarting</code> 状态时，
        按下面的频率推送到「Telegram 通知」里配置的机器人。同一容器的异常只推一次，恢复后再次异常才会再推。
      </p>
      <label className="row" style={{ gap: 8, cursor: 'pointer', marginBottom: 12 }}>
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        <span style={{ fontSize: 14 }}>启用容器监控</span>
      </label>
      <div className="field">
        <label>扫描频率（秒，最小 30，默认 60）</label>
        <input
          className="input"
          type="number"
          min={30}
          value={interval}
          onChange={(e) => setIntervalV(Number(e.target.value))}
        />
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
          容器状态由节点 agent 每 30 秒上报，监控发现的异常最多有约 30 秒延迟，属正常。
        </div>
      </div>
      <button className="btn primary" onClick={save} disabled={saving}>
        <Save size={14} /> {saving ? '保存中…' : '保存'}
      </button>
    </div>
  );
}

function CloudflareTab() {
  const [token, setToken] = useState('');
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [result, setResult] = useState<any>(null);

  // Load the saved token (masked presence only) on mount.
  useEffect(() => {
    api.settings.all().then((r: any) => {
      const cf = r?.cloudflare;
      if (cf?.api_token) setToken(cf.api_token);
    }).catch(() => {});
  }, []);

  const test = async () => {
    setTesting(true);
    setResult(null);
    try {
      const r: any = await api.settings.testCloudflare(token || undefined);
      setResult(r);
      notify(`连接成功：账号 ${r?.account_id?.slice(0, 8)}…，${r?.count ?? 0} 个 tunnel`, 'success');
    } catch (e: any) {
      setResult(null);
      notify(e?.response?.data?.error || '测试失败', 'error');
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      await api.settings.putCloudflare(token);
      notify('已保存 Cloudflare 令牌', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="card" style={{ padding: 18, maxWidth: 620 }}>
      <div className="row" style={{ gap: 10, marginBottom: 8 }}>
        <Cloud size={18} color="var(--primary)" />
        <strong>Cloudflare（容器迁移用）</strong>
      </div>
      <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
        容器跨节点迁移时，面板用这个 API token 自动把容器的公网域名从源节点 tunnel 搬到目标节点 tunnel 并改 DNS，实现无缝切换。
        需要 <code>Tunnel:Edit</code> 与 <code>DNS:Edit</code>（账号级）权限。
      </p>

      <div className="field">
        <label>Cloudflare API Token</label>
        <div className="row" style={{ gap: 8 }}>
          <input
            className="input"
            style={{ flex: 1 }}
            type="password"
            value={token}
            onChange={(e) => { setToken(e.target.value); setResult(null); }}
            placeholder="Cloudflare 仪表盘 → My Profile → API Tokens → 创建 token"
          />
          <button className="btn sm" onClick={test} disabled={testing}>
            {testing ? '测试中…' : '测试连接'}
          </button>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
          令牌明文存于本机面板数据库（与 GitHub/Telegram 令牌一致）。点「测试连接」会列出账号下的 tunnel 以验证权限。
        </div>
      </div>

      {result && (
        <div style={{ fontSize: 12, marginTop: 6, border: '1px solid var(--border-color)', borderRadius: 8, padding: 10 }}>
          <div>账号 ID：<code className="mono">{result.account_id}</code></div>
          <div style={{ marginTop: 6 }}>Tunnel（{result.count}）：</div>
          <ul style={{ margin: '4px 0 0', paddingLeft: 20 }}>
            {(result.tunnels || []).map((t: any) => (
              <li key={t.id} className="mono" style={{ fontSize: 11 }}>
                {t.name || '(未命名)'} — <span style={{ color: 'var(--text-tertiary)' }}>{t.id}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="row" style={{ gap: 10, marginTop: 14 }}>
        <button className="btn primary" onClick={save} disabled={saving || !token}>
          <Save size={14} /> {saving ? '保存中…' : '保存令牌'}
        </button>
      </div>
    </div>
  );
}

function GithubTab() {
  const [token, setToken] = useState('');
  const [repos, setRepos] = useState<any[]>([]);
  const [full, setFull] = useState(''); // selected "owner/repo"
  const [branch, setBranch] = useState('main');
  const [force, setForce] = useState(false);
  const [busy, setBusy] = useState(false);
  const [fetching, setFetching] = useState(false);
  const [saving, setSaving] = useState(false);
  const [log, setLog] = useState('');

  // Load saved config on mount (token + repo + branch + force). If a token was
  // saved, silently fetch the repo list so the dropdown can pre-select the repo.
  useEffect(() => {
    api.settings.all().then((r: any) => {
      const g = r?.github_project;
      if (!g) return;
      if (g.token) {
        setToken(g.token);
        api.targets.githubRepos(g.token).then((rr: any) => setRepos(Array.isArray(rr) ? rr : [])).catch(() => {});
      }
      if (g.branch) setBranch(g.branch);
      setForce(!!g.force);
      if (g.owner && g.repo) setFull(`${g.owner}/${g.repo}`);
    }).catch(() => {});
  }, []);

  const save = async () => {
    const [owner, repo] = full.split('/');
    setSaving(true);
    try {
      await api.settings.putGithub(token, owner || '', repo || '', branch || 'main', force);
      notify('配置已保存，下次可直接复用', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const fetchRepos = async () => {
    if (!token) return notify('请先粘贴令牌', 'error');
    setFetching(true);
    try {
      const r: any = await api.targets.githubRepos(token);
      setRepos(Array.isArray(r) ? r : []);
      notify(`拉取到 ${(r as any[]).length} 个可写仓库`, 'success');
    } catch (e: any) {
      setRepos([]);
      notify(e?.response?.data?.error || '拉取失败', 'error');
    } finally {
      setFetching(false);
    }
  };

  const push = async () => {
    if (!token || !full) return notify('请先粘贴令牌并选择仓库', 'error');
    const [owner, repo] = full.split('/');
    if (!owner || !repo) return notify('仓库格式不正确', 'error');
    setBusy(true);
    setLog('');
    try {
      const r: any = await api.settings.githubPushProject(token, owner, repo, branch || 'main', force);
      setLog(r?.log || '');
      if (r?.ok) {
        notify(`已推送到 ${owner}/${repo}（${branch || 'main'}）`, 'success');
      } else {
        notify(r?.error || '推送失败', 'error');
      }
    } catch (e: any) {
      notify(e?.response?.data?.error || '推送失败', 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card" style={{ padding: 18, maxWidth: 620 }}>
      <div className="row" style={{ gap: 10, marginBottom: 8 }}>
        <Github size={18} color="var(--primary)" />
        <strong>GitHub 集成</strong>
      </div>
      <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
        把本面板项目源码推送到你选定的 GitHub 仓库，其他服务器可 <code className="mono">git clone</code> 后构建部署。
        自动排除 node_modules / dist / 构建产物 / 数据库等，只上传源码。
      </p>

      <div className="field">
        <label>GitHub 令牌 (PAT)</label>
        <div className="row" style={{ gap: 8 }}>
          <input
            className="input"
            style={{ flex: 1 }}
            type="password"
            value={token}
            onChange={(e) => { setToken(e.target.value); setRepos([]); setFull(''); }}
            placeholder="ghp_... 或 github_pat_..."
          />
          <button className="btn sm" onClick={fetchRepos} disabled={fetching || !token}>
            {fetching ? '拉取中…' : '拉取仓库'}
          </button>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
          令牌需含 <code>repo</code> 权限（或仓库 Contents 写权限）。点「保存配置」可记住令牌与仓库以便复用（明文存于本机面板数据库）。
        </div>
      </div>

      <div className="field">
        <label>目标仓库</label>
        <select className="select" value={full} onChange={(e) => setFull(e.target.value)} disabled={repos.length === 0}>
          <option value="">{repos.length ? '选择仓库…' : '（先点右侧拉取仓库）'}</option>
          {repos.map((r) => (
            <option key={r.full_name} value={r.full_name}>{r.full_name}{r.private ? '' : '（公开）'}</option>
          ))}
          {full && !repos.some((r) => r.full_name === full) && (
            <option value={full}>{full}</option>
          )}
        </select>
      </div>

      <div className="row" style={{ gap: 16, alignItems: 'flex-end' }}>
        <div className="field" style={{ flex: 1, marginBottom: 0 }}>
          <label>分支</label>
          <input className="input" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
        </div>
        <label className="row" style={{ gap: 6, cursor: 'pointer', paddingBottom: 10 }}>
          <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
          <span style={{ fontSize: 13 }}>强制覆盖远端该分支</span>
        </label>
      </div>

      <div className="row" style={{ gap: 10, marginTop: 12 }}>
        <button className="btn primary" onClick={push} disabled={busy || !token || !full}>
          <UploadCloud size={14} /> {busy ? '推送中…' : '上传项目到仓库'}
        </button>
        <button className="btn" onClick={save} disabled={saving || !token}>
          <Save size={14} /> {saving ? '保存中…' : '保存配置'}
        </button>
      </div>

      {log && (
        <pre
          className="mono"
          style={{
            marginTop: 14, maxHeight: 260, overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
            background: 'var(--bg-secondary)', padding: 12, borderRadius: 8, fontSize: 12,
            color: 'var(--text-secondary)',
          }}
        >
          {log}
        </pre>
      )}
    </div>
  );
}

function KomariTab() {
  const [base, setBase] = useState('');
  const [key, setKey] = useState('');
  const [install, setInstall] = useState('');
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api.settings.all().then((r: any) => {
      const k = r?.komari;
      if (k?.base_url) setBase(k.base_url);
      if (k?.api_key) setKey(k.api_key);
      if (k?.install_url) setInstall(k.install_url);
    }).catch(() => {});
  }, []);

  const test = async () => {
    setTesting(true);
    try {
      const r: any = await api.settings.testKomari(base || undefined, key || undefined);
      notify(`连接成功：Komari 现有 ${r?.count ?? 0} 个节点`, 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '测试失败', 'error');
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      await api.settings.putKomari(base, key, install);
      notify('已保存 Komari 配置', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="card" style={{ padding: 18, maxWidth: 620 }}>
      <div className="row" style={{ gap: 10, marginBottom: 8 }}>
        <Radar size={18} color="var(--primary)" />
        <strong>Komari 探针集成</strong>
      </div>
      <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
        在「节点」页用「加入探针」把节点批量接入 Komari 探针。Komari 节点名 = NodePanel 节点名；已在探针里的节点自动从候选中排除。
      </p>
      <div className="field">
        <label>Komari 面板地址</label>
        <input className="input" value={base} onChange={(e) => setBase(e.target.value)} placeholder="https://komari.example.com" />
      </div>
      <div className="field">
        <label>API Key</label>
        <input className="input" type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder="Komari 后台 → 设置 → API Key（≥12 字符）" />
      </div>
      <div className="field">
        <label>Agent 安装脚本（可选，默认官方）</label>
        <input className="input mono" value={install} onChange={(e) => setInstall(e.target.value)} placeholder="https://raw.githubusercontent.com/komari-monitor/komari-agent/master/install.sh" />
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
          节点访问 GitHub 受限时可改为镜像 / ghproxy。令牌明文存于本机面板数据库（与其它集成一致）。
        </div>
      </div>
      <div className="row" style={{ gap: 10, marginTop: 12 }}>
        <button className="btn primary" onClick={save} disabled={saving || !base || !key}>
          <Save size={14} /> {saving ? '保存中…' : '保存'}
        </button>
        <button className="btn" onClick={test} disabled={testing || !base || !key}>
          {testing ? '测试中…' : '测试连接'}
        </button>
      </div>
    </div>
  );
}
