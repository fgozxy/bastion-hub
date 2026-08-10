// Small UI utility helpers.

export function copy(text: string): boolean {
  try {
    navigator.clipboard.writeText(text);
    return true;
  } catch {
    // fallback
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand('copy');
    } catch {
      /* ignore */
    }
    document.body.removeChild(ta);
    return true;
  }
}

// Trigger a browser download of a text blob (for saving keys across devices).
export function downloadText(filename: string, text: string, mime = 'text/plain') {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

// Turn a credential name into a safe filename stem.
export function safeName(name?: string): string {
  const base = (name || 'key').trim().replace(/[^A-Za-z0-9._-]+/g, '_');
  return base.replace(/^_+|_+$/g, '') || 'key';
}

// Abbreviate an IPv6 so its displayed length is similar to an IPv4 (~15 chars).
// e.g. 2001:0db8:85a3:0000:0000:8a2e:0370:7334 -> 2001:db8…:7334
export function truncateIPv6(ip: string): string {
  if (!ip || ip.includes(':') === false) return ip;
  // strip zone id
  const base = ip.split('%')[0];
  const groups = base.split(':');
  if (groups.length <= 4) return base;
  const head = groups.slice(0, 2).join(':');
  const tail = groups.slice(-2).join(':');
  return `${head}…${tail}`;
}

// Convert an ISO-3166 alpha-2 country code to a flag emoji.
export function flagEmoji(code?: string): string {
  if (!code || code.length !== 2) return '🏳️';
  const cc = code.toUpperCase();
  const A = 0x1f1e6;
  const base = 'A'.charCodeAt(0);
  return String.fromCodePoint(A + (cc.charCodeAt(0) - base), A + (cc.charCodeAt(1) - base));
}

export function bytes(n?: number): string {
  if (n == null) return '-';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

export function fmtTime(ts?: number): string {
  if (!ts) return '-';
  const d = new Date(ts * 1000);
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

export function relTime(ts?: number): string {
  if (!ts) return '-';
  const diff = Date.now() / 1000 - ts;
  if (diff < 60) return `${Math.floor(diff)}秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)}分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}小时前`;
  return `${Math.floor(diff / 86400)}天前`;
}
