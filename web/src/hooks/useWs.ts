import { useEffect, useRef } from 'react';

type Handler = (data: any) => void;

let ws: WebSocket | null = null;
let connecting = false;
const listeners = new Map<string, Set<Handler>>();

function connect() {
  if (ws || connecting) return;
  connecting = true;
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  try {
    ws = new WebSocket(`${proto}://${window.location.host}/api/ws`);
  } catch {
    connecting = false;
    setTimeout(connect, 3000);
    return;
  }
  ws.onopen = () => {
    connecting = false;
  };
  ws.onmessage = (e) => {
    try {
      const m = JSON.parse(e.data);
      const set = listeners.get(m.type);
      if (set) set.forEach((h) => h(m.data));
    } catch {
      /* ignore */
    }
  };
  ws.onclose = () => {
    ws = null;
    connecting = false;
    setTimeout(connect, 3000);
  };
  ws.onerror = () => {
    ws?.close();
  };
}

function ensure() {
  if (!ws) connect();
}

/** Subscribe to a websocket event type. Reconnects automatically. */
export function useWs(type: string, handler: Handler) {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    ensure();
    let set = listeners.get(type);
    if (!set) {
      set = new Set();
      listeners.set(type, set);
    }
    const h = (d: any) => ref.current(d);
    set.add(h);
    return () => {
      set!.delete(h);
    };
  }, [type]);
}
