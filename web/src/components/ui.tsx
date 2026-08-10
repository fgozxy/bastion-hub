import React, { useEffect, useState } from 'react';
import { Check, Copy } from 'lucide-react';
import { copy } from '../lib/utils';
import { useNotify } from '../stores';

export type ActionSheetItem = {
  key?: string;
  label: string;
  icon?: React.ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
};

/** Mobile bottom sheet: optional detail body + action list + cancel. */
export function ActionSheet({
  open,
  title,
  subtitle,
  onClose,
  actions,
  children,
}: {
  open: boolean;
  title?: string;
  subtitle?: React.ReactNode;
  onClose: () => void;
  actions?: ActionSheetItem[];
  children?: React.ReactNode;
}) {
  useEffect(() => {
    if (!open) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = prev;
    };
  }, [open]);

  if (!open) return null;
  return (
    <div className="action-sheet-backdrop" onClick={onClose} role="presentation">
      <div
        className="action-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={title || '操作菜单'}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="action-sheet-handle" aria-hidden />
        {(title || subtitle) && (
          <div className="action-sheet-head">
            {title && <div className="action-sheet-title">{title}</div>}
            {subtitle && <div className="action-sheet-sub">{subtitle}</div>}
          </div>
        )}
        {children && <div className="action-sheet-body">{children}</div>}
        {actions && actions.length > 0 && (
          <div className="action-sheet-actions">
            {actions.map((a, i) => (
              <button
                key={a.key || `${a.label}-${i}`}
                type="button"
                className={`action-sheet-item${a.danger ? ' danger' : ''}`}
                disabled={a.disabled}
                onClick={() => {
                  if (a.disabled) return;
                  onClose();
                  // Defer so the sheet unmounts before nested confirm/modals open.
                  setTimeout(() => a.onClick(), 0);
                }}
              >
                {a.icon && <span className="action-sheet-icon">{a.icon}</span>}
                <span>{a.label}</span>
              </button>
            ))}
          </div>
        )}
        <button type="button" className="action-sheet-cancel" onClick={onClose}>
          取消
        </button>
      </div>
    </div>
  );
}

export function CopyButton({ text, title }: { text: string; title?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      className="icon-btn"
      title={title || '复制'}
      onClick={(e) => {
        e.stopPropagation();
        if (copy(text)) {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        }
      }}
    >
      {done ? <Check size={14} color="var(--success)" /> : <Copy size={14} />}
    </button>
  );
}

export function Modal({
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
  footer?: React.ReactNode;
  wide?: boolean;
}) {
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" style={wide ? { maxWidth: 760 } : undefined} onClick={(e) => e.stopPropagation()}>
        <h3 className="modal-title">{title}</h3>
        {children}
        {footer && (
          <div className="modal-actions" style={{ justifyContent: 'flex-end', marginTop: 18 }}>
            {footer}
          </div>
        )}
      </div>
    </div>
  );
}

export function StatusBadge({ online }: { online: boolean }) {
  return (
    <span className={`badge ${online ? 'success' : 'muted'}`}>
      <span className={`status-dot ${online ? 'online' : 'offline'}`} />
      {online ? '在线' : '离线'}
    </span>
  );
}

// ConfirmModal is a small centered destructive-action confirmation built on
// Modal — a project-styled replacement for window.confirm(). Use it for delete /
// uninstall / reset gates so the prompt matches the rest of the UI instead of
// the browser's native dialog. `message` keeps newlines (white-space: pre-wrap).
export function ConfirmModal({
  title,
  message,
  confirmText = '确认',
  cancelText = '取消',
  danger = false,
  busy = false,
  onConfirm,
  onClose,
}: {
  title: string;
  message: string;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>
            {cancelText}
          </button>
          <button className={`btn ${danger ? 'danger' : 'primary'}`} onClick={onConfirm} disabled={busy}>
            {busy ? '处理中…' : confirmText}
          </button>
        </>
      }
    >
      <div style={{ fontSize: 13, color: 'var(--text-secondary)', whiteSpace: 'pre-wrap', lineHeight: 1.6 }}>
        {message}
      </div>
    </Modal>
  );
}

export function Toasts() {
  const { toasts, dismiss } = useNotify();
  return (
    <div className="toast-wrap">
      {toasts.map((t) => (
        <div key={t.id} className={`toast ${t.type}`} onClick={() => dismiss(t.id)}>
          {t.msg}
        </div>
      ))}
    </div>
  );
}

export function Empty({ text }: { text: string }) {
  return (
    <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>{text}</div>
  );
}

export function Loading() {
  return <div style={{ padding: 30, color: 'var(--text-tertiary)' }}>加载中…</div>;
}
