/**
 * Singleton frontend logger that mirrors every call to the server via the
 * signaling WebSocket (type "log") so all timing data lands in one stream.
 * Call setWs(ws) once the socket opens and setWs(null) when it closes.
 */

type LogLevel = 'info' | 'warn' | 'error' | 'debug';
type LogData = Record<string, unknown> | unknown[] | string | number | boolean | null;

let _ws: WebSocket | null = null;

export function setLogWs(ws: WebSocket | null) {
  _ws = ws;
}

function sendLog(level: LogLevel, msg: string, data?: LogData) {
  const ws = _ws;
  const ts = performance.now();
  if (data !== undefined) {
    console[level === 'error' ? 'error' : level === 'warn' ? 'warn' : 'log'](
      `[frontend] [${level}] ${msg} clk=${ts.toFixed(3)}`, data,
    );
  } else {
    console[level === 'error' ? 'error' : level === 'warn' ? 'warn' : 'log'](
      `[frontend] [${level}] ${msg} clk=${ts.toFixed(3)}`,
    );
  }
  if (ws && ws.readyState === WebSocket.OPEN) {
    try {
      ws.send(JSON.stringify({
        type: 'log',
        payload: { level, msg, ts, data },
      }));
    } catch {
      // Don't let a failed log send break anything.
    }
  }
}

export const logger = {
  info(msg: string, data?: LogData)  { sendLog('info', msg, data); },
  warn(msg: string, data?: LogData)  { sendLog('warn', msg, data); },
  error(msg: string, data?: LogData) { sendLog('error', msg, data); },
  debug(msg: string, data?: LogData) { sendLog('debug', msg, data); },
};
