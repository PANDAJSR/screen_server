export type SignalType =
  | 'hello'
  | 'welcome'
  | 'peer-joined'
  | 'peer-left'
  | 'ping'
  | 'pong'
  | 'offer'
  | 'answer'
  | 'candidate'
  | 'error'
  | 'input-mode'
  | 'quality-preset'
  | 'input-mousemove'
  | 'input-mousemove-abs'
  | 'input-mousebtn'
  | 'input-scroll'
  | 'input-keydown'
  | 'input-keyup'
  | 'input-key-state'
  | 'input-release-all'
  | 'input-touch'
  | 'cursor-pos'
  | 'cursor-image'
  | 'screen-size'
  | 'latency-start'
  | 'latency-blue'
  | 'latency-red'
  | 'log';

export interface SignalMessage<TPayload = unknown> {
  type: SignalType;
  room?: string;
  from?: string;
  to?: string;
  payload?: TPayload;
}

export function createSignalingSocket(room: string): WebSocket {
  const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = new URL('/api/signaling/ws', `${wsProtocol}//${window.location.host}`);
  url.searchParams.set('room', room);
  return new WebSocket(url);
}
