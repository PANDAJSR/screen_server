import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createSignalingSocket, type SignalMessage } from './signaling';

// ---- Key code → readable name mapping ----

const KEY_CODE_NAMES: Record<number, string> = {
  3: 'Pause', 8: 'Backspace', 9: 'Tab', 12: 'Clear', 13: 'Enter',
  16: 'Shift', 17: 'Ctrl', 18: 'Alt', 19: 'Pause', 20: 'CapsLock',
  27: 'Esc', 32: 'Space', 33: 'PageUp', 34: 'PageDown', 35: 'End', 36: 'Home',
  37: '←', 38: '↑', 39: '→', 40: '↓',
  44: 'PrtSc', 45: 'Insert', 46: 'Delete',
  48: '0', 49: '1', 50: '2', 51: '3', 52: '4', 53: '5', 54: '6', 55: '7', 56: '8', 57: '9',
  65: 'A', 66: 'B', 67: 'C', 68: 'D', 69: 'E', 70: 'F', 71: 'G', 72: 'H',
  73: 'I', 74: 'J', 75: 'K', 76: 'L', 77: 'M', 78: 'N', 79: 'O',
  80: 'P', 81: 'Q', 82: 'R', 83: 'S', 84: 'T', 85: 'U', 86: 'V',
  87: 'W', 88: 'X', 89: 'Y', 90: 'Z',
  91: 'Win', 92: 'Win',
  96: 'Num0', 97: 'Num1', 98: 'Num2', 99: 'Num3', 100: 'Num4',
  101: 'Num5', 102: 'Num6', 103: 'Num7', 104: 'Num8', 105: 'Num9',
  106: 'Num*', 107: 'Num+', 108: 'NumEnter', 109: 'Num-', 110: 'Num.', 111: 'Num/',
  112: 'F1', 113: 'F2', 114: 'F3', 115: 'F4', 116: 'F5', 117: 'F6',
  118: 'F7', 119: 'F8', 120: 'F9', 121: 'F10', 122: 'F11', 123: 'F12',
  144: 'NumLock', 145: 'ScrollLock',
  160: 'LShift', 161: 'RShift', 162: 'LCtrl', 163: 'RCtrl', 164: 'LAlt', 165: 'RAlt',
  173: 'Mute', 174: 'Vol-', 175: 'Vol+', 176: 'Next', 177: 'Prev', 178: 'Stop', 179: 'Play',
  186: ';', 187: '=', 188: ',', 189: '-', 190: '.', 191: '/', 192: '`',
  219: '[', 220: '\\', 221: ']', 222: '\'',
};

function keyCodeToName(code: number): string {
  return KEY_CODE_NAMES[code] ?? `VK${code}`;
}

type ConnectionState = 'connecting' | 'open' | 'closed' | 'error';
type CursorMode = 'disabled' | 'remote' | 'client' | 'remote-render';

interface CursorImagePayload {
  data: string;
  width: number;
  height: number;
  hotspotX: number;
  hotspotY: number;
}

interface TouchContact {
  id: number;
  x: number;
  y: number;
  phase: 'start' | 'move' | 'end';
}

// ---- CursorOverlay — renders the OS cursor image at a screen position ----

function CursorOverlay({
  position,
  cursorImage,
}: {
  position: { x: number; y: number };
  cursorImage: CursorImagePayload | null;
}) {
  const hotspotX = cursorImage ? cursorImage.hotspotX : 0;
  const hotspotY = cursorImage ? cursorImage.hotspotY : 0;

  return (
    <div
      className="remoteCursor"
      style={{
        position: 'fixed',
        left: position.x - hotspotX,
        top: position.y - hotspotY,
        pointerEvents: 'none',
        zIndex: 9999,
      }}
    >
      {cursorImage ? (
        <img
          src={`data:image/png;base64,${cursorImage.data}`}
          width={cursorImage.width}
          height={cursorImage.height}
          alt=""
          style={{ display: 'block' }}
        />
      ) : (
        <div className="cursorFallback" />
      )}
    </div>
  );
}

// ---- RemoteCursor — maps server cursor-pos to screen coordinates ----

function RemoteCursor({
  videoRef,
  cursorPos,
  screenSize,
  cursorImage,
}: {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  cursorPos: { x: number; y: number };
  screenSize: { width: number; height: number };
  cursorImage: CursorImagePayload | null;
}) {
  const [position, setPosition] = useState({ x: 0, y: 0 });

  useEffect(() => {
    const updatePosition = () => {
      const video = videoRef.current;
      if (!video) return;

      const containerRect = video.getBoundingClientRect();
      const videoWidth = video.videoWidth || screenSize.width;
      const videoHeight = video.videoHeight || screenSize.height;

      const containerAspect = containerRect.width / containerRect.height;
      const videoAspect = videoWidth / videoHeight;

      let contentLeft = containerRect.left;
      let contentTop = containerRect.top;
      let contentWidth = containerRect.width;
      let contentHeight = containerRect.height;

      if (containerAspect > videoAspect) {
        contentWidth = containerRect.height * videoAspect;
        contentLeft = containerRect.left + (containerRect.width - contentWidth) / 2;
      } else {
        contentHeight = containerRect.width / videoAspect;
        contentTop = containerRect.top + (containerRect.height - contentHeight) / 2;
      }

      const percentX = cursorPos.x / screenSize.width;
      const percentY = cursorPos.y / screenSize.height;

      setPosition({
        x: contentLeft + percentX * contentWidth,
        y: contentTop + percentY * contentHeight,
      });
    };

    updatePosition();

    const interval = setInterval(updatePosition, 1000 / 30);
    const resizeObserver = new ResizeObserver(updatePosition);
    if (videoRef.current) {
      resizeObserver.observe(videoRef.current);
    }
    return () => {
      clearInterval(interval);
      resizeObserver.disconnect();
    };
  }, [videoRef, cursorPos, screenSize]);

  return <CursorOverlay position={position} cursorImage={cursorImage} />;
}

// ---- Icon components ----

function StatusIcon({ state }: { state: ConnectionState }) {
  return (
    <svg className={`statusIcon status-${state}`} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <circle cx="12" cy="12" r="10" />
      <circle className="statusDot" cx="12" cy="12" r="4" fill="currentColor" stroke="none" />
    </svg>
  );
}

function RefreshIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <path d="M23 4v6h-6M1 20v-6h6" />
      <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15" />
    </svg>
  );
}

function InputIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <rect x="2" y="6" width="20" height="12" rx="2" />
      <path d="M6 10h.01M6 14h.01M10 10h.01M10 14h.01M14 10h.01M14 14h.01M18 10h.01M18 14h.01" strokeLinecap="round" />
      <rect x="9" y="3" width="6" height="4" rx="1" />
      <path d="M7 21h10" strokeLinecap="round" />
    </svg>
  );
}

function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="toggleRow">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggleTrack">
        <span className="toggleThumb" />
      </span>
      <span className="toggleLabel">{label}</span>
    </label>
  );
}

// ---- Main App ----

export function App() {
  const room = useMemo(() => new URLSearchParams(window.location.search).get('room') ?? 'default', []);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const startedRef = useRef(false);
  const inputEnabledRef = useRef(true);
  const inputLockedRef = useRef(false);
  const cursorModeRef = useRef<CursorMode>('client');
  const touchEnabledRef = useRef(true);
  const [connectionKey, setConnectionKey] = useState(0);
  const [state, setState] = useState<ConnectionState>('connecting');
  const [clientId, setClientId] = useState<string>('');
  const [pcState, setPcState] = useState<RTCPeerConnectionState>('new');
  const [iceState, setIceState] = useState<RTCIceConnectionState>('new');
  const [videoStats, setVideoStats] = useState('等待中');
  const [clock, setClock] = useState(() => new Date());
  const [inputEnabled, setInputEnabled] = useState(true);
  const [inputLocked, setInputLocked] = useState(false);
  const [cursorPos, setCursorPos] = useState({ x: 0, y: 0 });
  const [screenSize, setScreenSize] = useState({ width: 1920, height: 1080 });
  const [cursorImage, setCursorImage] = useState<CursorImagePayload | null>(null);
  const [cursorMode, setCursorMode] = useState<CursorMode>('client');
  // Client cursor mode: local mouse position on screen (for overlay rendering)
  const [clientCursorScreenPos, setClientCursorScreenPos] = useState({ x: 0, y: 0 });
  const [statusOpen, setStatusOpen] = useState(false);
  const [inputMenuOpen, setInputMenuOpen] = useState(false);
  const [keyboardEnabled, setKeyboardEnabled] = useState(true);
  const [touchEnabled, setTouchEnabled] = useState(true);
  const [roundTripMs, setRoundTripMs] = useState<number | null>(null);
  const [fps, setFps] = useState<number | null>(null);
  const [remoteKeysPressed, setRemoteKeysPressed] = useState<number[]>([]);

  const statusButtonRef = useRef<HTMLButtonElement | null>(null);
  const inputButtonRef = useRef<HTMLButtonElement | null>(null);
  const statusPopoverRef = useRef<HTMLDivElement | null>(null);
  const inputPopoverRef = useRef<HTMLDivElement | null>(null);

  const prevFramesRef = useRef<number | null>(null);
  const prevTimeRef = useRef<number | null>(null);

  cursorModeRef.current = cursorMode;
  touchEnabledRef.current = touchEnabled;

  const setInputEnabledSync = (enabled: boolean) => {
    inputEnabledRef.current = enabled;
    setInputEnabled(enabled);
    if (!enabled && document.pointerLockElement) {
      document.exitPointerLock();
    }
  };

  // ---- Coordinate mapping helpers ----

  /** Get the letterboxed content rect for the video element. Returns null if video not ready. */
  function getContentRect(): { left: number; top: number; width: number; height: number } | null {
    const video = videoRef.current;
    if (!video) return null;

    const containerRect = video.getBoundingClientRect();
    const videoWidth = video.videoWidth || screenSize.width;
    const videoHeight = video.videoHeight || screenSize.height;

    const containerAspect = containerRect.width / containerRect.height;
    const videoAspect = videoWidth / videoHeight;

    let contentLeft = containerRect.left;
    let contentTop = containerRect.top;
    let contentWidth = containerRect.width;
    let contentHeight = containerRect.height;

    if (containerAspect > videoAspect) {
      contentWidth = containerRect.height * videoAspect;
      contentLeft = containerRect.left + (containerRect.width - contentWidth) / 2;
    } else {
      contentHeight = containerRect.width / videoAspect;
      contentTop = containerRect.top + (containerRect.height - contentHeight) / 2;
    }
    return { left: contentLeft, top: contentTop, width: contentWidth, height: contentHeight };
  }

  /** Map a clientX/clientY (viewport pixels) to remote screen coordinates. Returns null if outside content area. */
  function mapClientToRemote(clientX: number, clientY: number): { x: number; y: number } | null {
    const cr = getContentRect();
    if (!cr) return null;
    if (clientX < cr.left || clientX > cr.left + cr.width || clientY < cr.top || clientY > cr.top + cr.height) {
      return null;
    }
    return {
      x: Math.round(((clientX - cr.left) / cr.width) * screenSize.width),
      y: Math.round(((clientY - cr.top) / cr.height) * screenSize.height),
    };
  }

  /** Map clientX/clientY to the screen position where the cursor overlay should be rendered. */
  function mapClientToScreenPos(clientX: number, clientY: number): { x: number; y: number } {
    const cr = getContentRect();
    if (!cr) return { x: clientX, y: clientY };
    // Clamp to content area
    const clampedX = Math.max(cr.left, Math.min(cr.left + cr.width, clientX));
    const clampedY = Math.max(cr.top, Math.min(cr.top + cr.height, clientY));
    return { x: clampedX, y: clampedY };
  }

  // ---- Main effect: WebSocket, WebRTC, event listeners ----

  useEffect(() => {
    const sendSignal = (message: SignalMessage) => {
      const ws = wsRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      ws.send(JSON.stringify(message));
    };

    const sendInput = (type: SignalMessage['type'], payload: unknown) => {
      if (!inputEnabledRef.current) return;
      if (type === 'input-mousemove' || type === 'input-mousemove-abs' || type === 'input-mousebtn' || type === 'input-scroll') {
        if (cursorModeRef.current === 'disabled') return;
      }
      if (type === 'input-keydown' || type === 'input-keyup') {
        if (!keyboardEnabled) return;
      }
      if (type === 'input-touch') {
        if (!touchEnabledRef.current) return;
        if (cursorModeRef.current === 'disabled') return;
      }
      sendSignal({ type, payload });
    };

    // Remote cursor mode: pointer lock → relative movement
    const handleMouseMoveRemote = (e: MouseEvent) => {
      if (cursorModeRef.current !== 'remote') return;
      if (!inputEnabledRef.current) return;
      if (document.pointerLockElement !== videoRef.current) return;
      sendInput('input-mousemove', { x: e.movementX, y: e.movementY });
    };

    // Mobile / touch — dual mode:
    //   touchEnabled ON  → native multi-touch via input-touch messages
    //   touchEnabled OFF → legacy single-finger mouse emulation

    // Track which fingers are currently active (for cancel cleanup).
    const activeTouches = new Set<number>();

    const handleTouchStart = (e: TouchEvent) => {
      if (!inputEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;

      if (touchEnabledRef.current) {
        e.preventDefault();
        // Build a set of changed identifiers so we can tell new from existing.
        const changedIds = new Set<number>();
        for (let i = 0; i < e.changedTouches.length; i++) {
          changedIds.add(e.changedTouches[i].identifier);
        }
        const touches: TouchContact[] = [];
        // Include ALL active touches so Windows sees the full contact set.
        for (let i = 0; i < e.touches.length; i++) {
          const t = e.touches[i];
          activeTouches.add(t.identifier);
          const remote = mapClientToRemote(t.clientX, t.clientY);
          if (!remote) continue;
          if (changedIds.has(t.identifier)) {
            touches.push({ id: t.identifier, x: remote.x, y: remote.y, phase: "start" });
          } else {
            // Existing contact — keep alive with a move update.
            touches.push({ id: t.identifier, x: remote.x, y: remote.y, phase: "move" });
          }
        }
        if (touches.length > 0) {
          sendInput("input-touch", { touches });
        }
        return;
      }

      // Legacy: single finger → absolute mouse + left button
      if (e.touches.length !== 1) return;
      const t = e.touches[0];
      const remote = mapClientToRemote(t.clientX, t.clientY);
      if (remote) {
        sendInput('input-mousemove-abs', { x: remote.x, y: remote.y });
        sendInput('input-mousebtn', { button: 1, pressed: true, x: remote.x, y: remote.y });
      }
    };

    const handleTouchMove = (e: TouchEvent) => {
      if (!inputEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;

      if (touchEnabledRef.current) {
        e.preventDefault();
        // Include ALL active touches so Windows sees the full contact set.
        const touches: TouchContact[] = [];
        for (let i = 0; i < e.touches.length; i++) {
          const t = e.touches[i];
          const remote = mapClientToRemote(t.clientX, t.clientY);
          if (remote) {
            touches.push({ id: t.identifier, x: remote.x, y: remote.y, phase: "move" });
          }
        }
        if (touches.length > 0) {
          sendInput("input-touch", { touches });
        }
        return;
      }

      // Legacy: single finger → absolute mouse move
      if (e.touches.length !== 1) return;
      const t = e.touches[0];
      const remote = mapClientToRemote(t.clientX, t.clientY);
      if (remote) {
        sendInput('input-mousemove-abs', { x: remote.x, y: remote.y });
      }
    };

    const handleTouchEnd = (e: TouchEvent) => {
      if (!inputEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;

      if (touchEnabledRef.current) {
        e.preventDefault(); // suppress browser-synthesized mouse events
        // Build a set of ended identifiers; other fingers stay alive.
        const endedIds = new Set<number>();
        for (let i = 0; i < e.changedTouches.length; i++) {
          endedIds.add(e.changedTouches[i].identifier);
          activeTouches.delete(e.changedTouches[i].identifier);
        }
        const touches: TouchContact[] = [];
        // Include ALL active touches (changed + remaining).
        for (let i = 0; i < e.touches.length; i++) {
          const t = e.touches[i];
          const remote = mapClientToRemote(t.clientX, t.clientY);
          if (!remote) continue;
          touches.push({
            id: t.identifier,
            x: remote.x,
            y: remote.y,
            phase: "move",
          });
        }
        // Now send the ended touches with phase=end and their last position.
        for (let i = 0; i < e.changedTouches.length; i++) {
          const t = e.changedTouches[i];
          const remote = mapClientToRemote(t.clientX, t.clientY);
          touches.push({
            id: t.identifier,
            x: remote ? remote.x : 0,
            y: remote ? remote.y : 0,
            phase: "end",
          });
        }
        if (touches.length > 0) {
          sendInput("input-touch", { touches });
        }
        return;
      }

      // Legacy: release left mouse button
      sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
    };

    const handleTouchCancel = (e: TouchEvent) => {
      if (!touchEnabledRef.current) return;
      // Cancel all active touches — send "end" for each.
      const touches: TouchContact[] = [];
      for (let i = 0; i < e.changedTouches.length; i++) {
        const t = e.changedTouches[i];
        activeTouches.delete(t.identifier);
        touches.push({ id: t.identifier, x: 0, y: 0, phase: 'end' });
      }
      if (touches.length > 0) {
        sendInput('input-touch', { touches });
      }
    };

    // Mouse buttons (shared between modes)
    // When touch is enabled, skip mouse events — they're synthesised by the browser
    // from touch interactions and we handle those via input-touch instead.
    const handleMouseDown = (e: MouseEvent) => {
      if (touchEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;
      if (!inputEnabledRef.current) return;
      const btn = e.button;
      let button = 1;
      if (btn === 0) button = 1;
      else if (btn === 2) button = 2;
      else if (btn === 1) button = 4;
      sendInput('input-mousebtn', { button, pressed: true, x: e.clientX, y: e.clientY });
    };

    const handleMouseUp = (e: MouseEvent) => {
      if (touchEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;
      if (!inputEnabledRef.current) return;
      const btn = e.button;
      let button = 1;
      if (btn === 0) button = 1;
      else if (btn === 2) button = 2;
      else if (btn === 1) button = 4;
      sendInput('input-mousebtn', { button, pressed: false, x: e.clientX, y: e.clientY });
    };

    const handleWheel = (e: WheelEvent) => {
      if (cursorModeRef.current === 'disabled') return;
      if (!inputEnabledRef.current) return;
      e.preventDefault();
      sendInput('input-scroll', { dx: e.deltaX, dy: e.deltaY });
    };

    // Track keys currently pressed so we can simulate repeat on the remote side.
    const heldKeys = new Set<number>();

    const handleKeyDown = (e: KeyboardEvent) => {
      if (!keyboardEnabled) return;
      if (!inputEnabledRef.current) return;
      if (
        e.keyCode === 32 || e.keyCode === 33 || e.keyCode === 34 || e.keyCode === 35 ||
        e.keyCode === 36 || e.keyCode === 37 || e.keyCode === 38 || e.keyCode === 39 ||
        e.keyCode === 40 || e.keyCode === 9
      ) {
        e.preventDefault();
      }
      if (e.repeat) {
        // Browser auto-repeat: simulate a fresh keystroke on the remote side.
        // Windows SendInput does not generate typematic auto-repeat for injected
        // input, so we must send a key-up + key-down pair for each repeat event.
        sendInput('input-keyup', { keyCode: e.keyCode });
        sendInput('input-keydown', { keyCode: e.keyCode });
        return;
      }
      heldKeys.add(e.keyCode);
      sendInput('input-keydown', { keyCode: e.keyCode });
    };

    const handleContextMenu = (e: MouseEvent) => {
      if (!inputEnabledRef.current) return;
      e.preventDefault();
    };

    const handleKeyUp = (e: KeyboardEvent) => {
      heldKeys.delete(e.keyCode);
      if (!keyboardEnabled) return;
      if (!inputEnabledRef.current) return;
      sendInput('input-keyup', { keyCode: e.keyCode });
    };

    const startPeer = async () => {
      if (startedRef.current) return;
      startedRef.current = true;

      const pc = new RTCPeerConnection({
        iceServers: [{ urls: 'stun:stun.l.google.com:19302' }],
      });
      pcRef.current = pc;

      const videoTransceiver = pc.addTransceiver('video', { direction: 'recvonly' });

      // Minimise jitter buffer for LAN (Chrome 129+/Edge 129+).
      // On LAN with near-zero packet loss we don't need the default 30-100ms buffer.
      const setLowLatency = () => {
        try {
          const r = videoTransceiver.receiver;
          if (r && 'playoutDelayHint' in r) {
            (r as any).playoutDelayHint = 0.05;
          }
        } catch { /* unsupported browser */ }
      };
      setLowLatency();

      pc.ontrack = (event) => {
        const [stream] = event.streams;
        if (videoRef.current && stream) {
          videoRef.current.srcObject = stream;
          const playResult = videoRef.current.play();
          if (playResult && playResult.catch) {
            playResult.catch(() => undefined);
          }
          // Some browsers only accept playoutDelayHint once tracks are flowing.
          setTimeout(setLowLatency, 100);
        }
      };

      pc.onicecandidate = (event) => {
        if (event.candidate) {
          sendSignal({ type: 'candidate', payload: event.candidate.toJSON() });
        }
      };

      pc.onconnectionstatechange = () => setPcState(pc.connectionState);
      pc.oniceconnectionstatechange = () => setIceState(pc.iceConnectionState);

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      if (pc.localDescription) {
        sendSignal({ type: 'offer', payload: pc.localDescription.toJSON() });
      }
    };

    let pingTimer: number | undefined;
    let statsTimer: number | undefined;
    let clockTimer: number | undefined;

    const ws = createSignalingSocket(room);
    wsRef.current = ws;

    ws.onopen = () => {
      setState('open');
      sendSignal({ type: 'hello', payload: { userAgent: navigator.userAgent } });

      pingTimer = window.setInterval(() => {
        sendSignal({ type: 'ping', payload: { ts: Date.now() } });
      }, 5000);

      statsTimer = window.setInterval(() => {
        const video = videoRef.current;
        const quality = video?.getVideoPlaybackQuality?.();
        if (!video || !quality) return;
        const currentTime = video.currentTime;
        const totalFrames = quality.totalVideoFrames;
        const droppedFrames = quality.droppedVideoFrames;
        setVideoStats(`time=${currentTime.toFixed(2)}s 解码=${totalFrames} 丢帧=${droppedFrames}`);
        if (prevTimeRef.current !== null && prevFramesRef.current !== null) {
          const dt = currentTime - prevTimeRef.current;
          const df = totalFrames - prevFramesRef.current;
          if (dt > 0) setFps(df / dt);
        }
        prevTimeRef.current = currentTime;
        prevFramesRef.current = totalFrames;
      }, 1000);

      clockTimer = window.setInterval(() => setClock(new Date()), 100);
    };

    ws.onmessage = async (event) => {
      const message = JSON.parse(event.data) as SignalMessage<{ clientId?: string; ts?: number }>;

      if (message.type === 'pong' && message.payload && typeof message.payload === 'object' && 'ts' in message.payload) {
        setRoundTripMs(Date.now() - (message.payload as { ts: number }).ts);
        return;
      }

      if (message.type === 'welcome' && message.payload?.clientId) {
        setClientId(message.payload.clientId);
        try { await startPeer(); } catch { setState('error'); }
        return;
      }

      const pc = pcRef.current;
      if (!pc) return;

      if (message.type === 'answer') {
        await pc.setRemoteDescription(message.payload as RTCSessionDescriptionInit);
      } else if (message.type === 'candidate' && message.payload) {
        await pc.addIceCandidate(message.payload as RTCIceCandidateInit);
      } else if (message.type === 'error') {
        setState('error');
      } else if (message.type === 'cursor-pos' && message.payload) {
        const pos = message.payload as { x: number; y: number };
        setCursorPos({ x: pos.x, y: pos.y });
      } else if (message.type === 'cursor-image' && message.payload) {
        const ci = message.payload as CursorImagePayload;
        console.log('[cursor-client] received:', ci.width + 'x' + ci.height,
          'hotspot=(' + ci.hotspotX + ',' + ci.hotspotY + ')',
          'dataLen=' + ci.data.length);
        setCursorImage(ci);
      } else if (message.type === 'screen-size' && message.payload) {
        const size = message.payload as { width: number; height: number };
        setScreenSize({ width: size.width, height: size.height });
      } else if (message.type === 'input-key-state' && message.payload) {
        setRemoteKeysPressed(message.payload as number[]);
      }
    };

    ws.onerror = () => setState('error');
    ws.onclose = () => setState('closed');

    const handlePointerLockChange = () => {
      const locked = document.pointerLockElement === videoRef.current;
      inputLockedRef.current = locked;
      setInputLocked(locked);
    };

    const handleDocumentClick = (e: MouseEvent) => {
      if (!inputEnabledRef.current) return;
      if (cursorModeRef.current !== 'remote') return;
      const video = videoRef.current;
      if (!video) return;
      if (e.target === video || video.contains(e.target as Node)) {
        if (document.pointerLockElement !== video) {
          video.requestPointerLock();
        }
      }
    };

    document.addEventListener('pointerlockchange', handlePointerLockChange);
    document.addEventListener('mousemove', handleMouseMoveRemote);
    document.addEventListener('mousedown', handleMouseDown);
    document.addEventListener('mouseup', handleMouseUp);
    const handleBlur = () => {
      for (const keyCode of heldKeys) {
        sendInput('input-keyup', { keyCode });
      }
      heldKeys.clear();
    };
    window.addEventListener('blur', handleBlur);

    document.addEventListener('wheel', handleWheel, { passive: false });
    document.addEventListener('keydown', handleKeyDown);
    document.addEventListener('keyup', handleKeyUp);
    document.addEventListener('click', handleDocumentClick);
    document.addEventListener('contextmenu', handleContextMenu);
    document.addEventListener('touchstart', handleTouchStart, { passive: false });
    document.addEventListener('touchmove', handleTouchMove, { passive: false });
    document.addEventListener('touchend', handleTouchEnd);
    document.addEventListener('touchcancel', handleTouchCancel);

    return () => {
      if (pingTimer !== undefined) window.clearInterval(pingTimer);
      if (statsTimer !== undefined) window.clearInterval(statsTimer);
      if (clockTimer !== undefined) window.clearInterval(clockTimer);
      if (document.pointerLockElement === videoRef.current) document.exitPointerLock();
      document.removeEventListener('pointerlockchange', handlePointerLockChange);
      document.removeEventListener('mousemove', handleMouseMoveRemote);
      document.removeEventListener('mousedown', handleMouseDown);
      document.removeEventListener('mouseup', handleMouseUp);
      document.removeEventListener('wheel', handleWheel);
      window.removeEventListener('blur', handleBlur);
      for (const keyCode of heldKeys) {
        sendInput('input-keyup', { keyCode });
      }
      heldKeys.clear();
      document.removeEventListener('keydown', handleKeyDown);
      document.removeEventListener('keyup', handleKeyUp);
      document.removeEventListener('click', handleDocumentClick);
      document.removeEventListener('contextmenu', handleContextMenu);
      document.removeEventListener('touchstart', handleTouchStart);
      document.removeEventListener('touchmove', handleTouchMove);
      document.removeEventListener('touchend', handleTouchEnd);
      document.removeEventListener('touchcancel', handleTouchCancel);
      pcRef.current?.close();
      pcRef.current = null;
      ws.close();
      wsRef.current = null;
      startedRef.current = false;
      if (videoRef.current) {
        videoRef.current.srcObject = null;
      }
    };
  }, [room, connectionKey, keyboardEnabled, touchEnabled]);

  // ---- Client / remote-render cursor mode: video mousemove → absolute position + cursor overlay ----

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;

    let lastTime = 0;

    const handler = (e: MouseEvent) => {
      const mode = cursorModeRef.current;
      if (mode !== 'client' && mode !== 'remote-render') return;
      if (!inputEnabledRef.current) return;

      // Update cursor overlay position instantly for client mode (zero latency)
      if (mode === 'client') {
        setClientCursorScreenPos(mapClientToScreenPos(e.clientX, e.clientY));
      }

      // Throttle network sends to ~60fps
      const now = Date.now();
      if (now - lastTime < 16) return;
      lastTime = now;

      const remote = mapClientToRemote(e.clientX, e.clientY);
      if (remote) {
        const ws = wsRef.current;
        if (!ws || ws.readyState !== WebSocket.OPEN) return;
        ws.send(JSON.stringify({ type: 'input-mousemove-abs', payload: { x: remote.x, y: remote.y } }));
      }
    };

    video.addEventListener('mousemove', handler);
    return () => video.removeEventListener('mousemove', handler);
  }, [cursorMode, inputEnabled]);

  // ---- Click outside to close popovers ----

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (
        statusOpen &&
        statusPopoverRef.current && statusButtonRef.current &&
        !statusPopoverRef.current.contains(e.target as Node) &&
        !statusButtonRef.current.contains(e.target as Node)
      ) {
        setStatusOpen(false);
      }
      if (
        inputMenuOpen &&
        inputPopoverRef.current && inputButtonRef.current &&
        !inputPopoverRef.current.contains(e.target as Node) &&
        !inputButtonRef.current.contains(e.target as Node)
      ) {
        setInputMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [statusOpen, inputMenuOpen]);

  const reconnect = () => {
    if (document.pointerLockElement === videoRef.current) {
      document.exitPointerLock();
    }
    setStatusOpen(false);
    setInputMenuOpen(false);
    setConnectionKey((k) => k + 1);
  };

  const sendReleaseAll = useCallback(() => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: 'input-release-all' }));
  }, []);

  const getOverlayText = () => {
    if (!inputEnabled) return null;
    if (cursorMode === 'disabled') return '输入已启用（鼠标已禁用）';
    if (cursorMode === 'remote') {
      return inputLocked ? '点击或按 ESC 释放' : '点击视频锁定鼠标';
    }
    if (cursorMode === 'remote-render') return '输入已启用（光标在视频中渲染）';
    return '输入已启用（客户端光标模式）';
  };

  const overlayText = getOverlayText();

  // ---- Render ----

  return (
    <main className="shell">
      <section className="iconBar" aria-label="控制栏">
        <button ref={statusButtonRef} className="iconButtonWrap" title="连接状态" onClick={() => setStatusOpen((o) => !o)}>
          <StatusIcon state={state} />
        </button>
        <button className="iconButtonWrap" title="重新连接" onClick={reconnect}>
          <RefreshIcon />
        </button>
        <button ref={inputButtonRef} className={`iconButtonWrap ${inputMenuOpen ? 'active' : ''}`} title="输入映射" onClick={() => setInputMenuOpen((o) => !o)}>
          <InputIcon />
        </button>
      </section>

      {statusOpen && (
        <div className="popover statusPopover" ref={statusPopoverRef}>
          <h3>状态</h3>
          <dl className="meta">
            <div><dt>连接状态</dt><dd>{state}</dd></div>
            <div><dt>客户端 ID</dt><dd>{clientId || '等待中'}</dd></div>
            <div><dt>对等连接</dt><dd>{pcState}</dd></div>
            <div><dt>ICE 状态</dt><dd>{iceState}</dd></div>
            <div><dt>帧率</dt><dd>{fps !== null ? fps.toFixed(1) : '-'}</dd></div>
            <div><dt>延迟</dt><dd>{roundTripMs !== null ? `${roundTripMs.toFixed(1)} 毫秒` : '-'}</dd></div>
            <div><dt>视频</dt><dd>{videoStats}</dd></div>
            <div>
              <dt>时钟</dt>
              <dd>{clock.toLocaleTimeString()}.{clock.getMilliseconds().toString().padStart(3, '0')}</dd>
            </div>
          </dl>
        </div>
      )}

      {inputMenuOpen && (
        <div className="popover inputPopover" ref={inputPopoverRef}>
          <h3>输入映射</h3>
          <Toggle label="启用输入" checked={inputEnabled} onChange={setInputEnabledSync} />
          <div className="selectRow">
            <span className="selectLabel">鼠标</span>
            <select
              className="cursorModeSelect"
              value={cursorMode}
              onChange={(e) => {
                const mode = e.target.value as CursorMode;
                setCursorMode(mode);
                const ws = wsRef.current;
                if (ws && ws.readyState === WebSocket.OPEN) {
                  ws.send(JSON.stringify({ type: 'input-mode', payload: { cursorMode: mode } }));
                }
              }}
            >
              <option value="disabled">禁用</option>
              <option value="remote">远程光标</option>
              <option value="client">客户端光标</option>
              <option value="remote-render">远程渲染光标</option>
            </select>
          </div>
          <Toggle label="键盘" checked={keyboardEnabled} onChange={setKeyboardEnabled} />
          <Toggle label="触摸" checked={touchEnabled} onChange={(v) => { setTouchEnabled(v); }} />
          <div className="keyStateSection">
            <span className="keyStateLabel">远端按键</span>
            {remoteKeysPressed.length === 0 ? (
              <span className="keyStateEmpty">无按键按下</span>
            ) : (
              <div className="keyStatePills">
                {remoteKeysPressed.map((code) => (
                  <span key={code} className="keyStatePill">{keyCodeToName(code)}</span>
                ))}
              </div>
            )}
            <button className="releaseAllBtn" onClick={sendReleaseAll} disabled={remoteKeysPressed.length === 0}>
              松开全部按键
            </button>
          </div>
          <div className="inputHint">
            {cursorMode === 'disabled'
              ? '鼠标输入已禁用'
              : cursorMode === 'remote'
                ? (inputLocked ? '点击或按 ESC 释放' : '点击视频锁定鼠标')
                : cursorMode === 'remote-render'
                  ? '移动鼠标到视频上即可控制远程（光标在视频中渲染）'
                  : '移动鼠标到视频上即可控制远程'}
          </div>
        </div>
      )}

      <section className="viewer">
        <video
          ref={videoRef}
          autoPlay
          playsInline
          muted
          controls={false}
          style={inputEnabled && (cursorMode === 'client' || cursorMode === 'remote-render') ? { cursor: 'none' } : undefined}
        />
        {inputEnabled && overlayText && (
          <div className="inputOverlay"><span>{overlayText}</span></div>
        )}
        {/* Remote cursor mode: overlay follows server cursor-pos */}
        {inputEnabled && cursorMode === 'remote' && (
          <RemoteCursor videoRef={videoRef} cursorPos={cursorPos} screenSize={screenSize} cursorImage={cursorImage} />
        )}
        {/* Client cursor mode: overlay follows local mouse position (zero latency) */}
        {inputEnabled && cursorMode === 'client' && (
          <CursorOverlay position={clientCursorScreenPos} cursorImage={cursorImage} />
        )}
        {/* Remote-render mode: cursor is rendered in the video stream — no overlay needed */}
      </section>
    </main>
  );
}
