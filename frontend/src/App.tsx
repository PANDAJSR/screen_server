import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createSignalingSocket, type SignalMessage } from './signaling';
import { logger, setLogWs } from './logger';

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

// ---- Touch mode types ----

type TouchMode = 'disabled' | 'multi-touch' | 'mouse' | 'trackpad';
type DragMethod = 'long-press' | 'double-tap' | 'three-finger';

interface FingerTrack {
  id: number;
  startClientX: number;
  startClientY: number;
  lastClientX: number;
  lastClientY: number;
  startTime: number;
  totalDelta: number;
  moved: boolean;
}

interface DragState {
  active: boolean;
  method: DragMethod | null;
  buttonDown: boolean;
  averageStartX: number;
  averageStartY: number;
}

interface LastTapInfo {
  time: number;
  x: number;
  y: number;
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
  captureRegion,
}: {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  cursorPos: { x: number; y: number };
  screenSize: { width: number; height: number };
  cursorImage: CursorImagePayload | null;
  captureRegion: { offsetX: number; offsetY: number; width: number; height: number } | null;
}) {
  const [position, setPosition] = useState({ x: 0, y: 0 });

  useEffect(() => {
    const updatePosition = () => {
      const video = videoRef.current;
      if (!video) return;

      const containerRect = video.getBoundingClientRect();
      // Use capture-region dimensions when available; they are the ground truth
      // for the video's intrinsic size and don't go stale on resolution changes.
      const videoWidth = (captureRegion && captureRegion.width > 0) ? captureRegion.width : (video.videoWidth || screenSize.width);
      const videoHeight = (captureRegion && captureRegion.height > 0) ? captureRegion.height : (video.videoHeight || screenSize.height);

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

      // Normalize cursor position relative to the capture region so the
      // overlay is correct when viewing a sub-region (window/display mode).
      // cursorPos from the server is in absolute screen coordinates;
      // subtract the region offset and divide by region dimensions.
      let percentX: number;
      let percentY: number;
      if (captureRegion && captureRegion.width > 0 && captureRegion.height > 0 &&
          (captureRegion.offsetX !== 0 || captureRegion.offsetY !== 0 ||
           captureRegion.width !== screenSize.width || captureRegion.height !== screenSize.height)) {
        percentX = (cursorPos.x - captureRegion.offsetX) / captureRegion.width;
        percentY = (cursorPos.y - captureRegion.offsetY) / captureRegion.height;
      } else {
        percentX = cursorPos.x / screenSize.width;
        percentY = cursorPos.y / screenSize.height;
      }

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
  }, [videoRef, cursorPos, screenSize, captureRegion]);

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

function QualityIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <circle cx="12" cy="12" r="3" />
      <path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" strokeLinecap="round" />
    </svg>
  );
}

function CaptureSettingsIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <rect x="2" y="3" width="20" height="14" rx="2" />
      <path d="M8 21h8M12 17v4" />
      <circle cx="12" cy="10" r="2" fill="currentColor" />
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
  const touchModeRef = useRef<TouchMode>('multi-touch');
  const dragMethodsRef = useRef<Set<DragMethod>>(new Set(['long-press']));
  // Touch tracking state — lives in refs to avoid handler rebuilds and re-renders
  const fingerMapRef = useRef<Map<number, FingerTrack>>(new Map());
  const dragStateRef = useRef<DragState>({ active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 });
  const lastTapRef = useRef<LastTapInfo | null>(null);
  const longPressTimerRef = useRef<number | null>(null);
  const threeFingerTimerRef = useRef<number | null>(null);
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
  type QualityPreset = 'smooth' | 'balanced' | 'quality';
  const [qualityPreset, setQualityPreset] = useState<QualityPreset>('balanced');
  const [qualityOpen, setQualityOpen] = useState(false);
  type CaptureMode = 'desktop' | 'display' | 'window';
  type WindowTransparencyBg = '' | 'black' | 'white';
  const [captureSettingsOpen, setCaptureSettingsOpen] = useState(false);
  const [captureMode, setCaptureMode] = useState<CaptureMode>('desktop');
  const [selectedDisplayIndex, setSelectedDisplayIndex] = useState(0);
  const [selectedWindowTitle, setSelectedWindowTitle] = useState('');
  const [windowTransparencyBg, setWindowTransparencyBg] = useState<WindowTransparencyBg>('');
  const [sessionId, setSessionId] = useState(0);
  const [sessions, setSessions] = useState<{id:number;name:string;state:string;userName:string}[]>([]);
  const [displays, setDisplays] = useState<{index:number;name:string;x:number;y:number;width:number;height:number;primary:boolean}[]>([]);
  const [windows, setWindows] = useState<{title:string;class:string}[]>([]);
  type CaptureRegion = { offsetX: number; offsetY: number; width: number; height: number } | null;
  const [captureRegion, setCaptureRegion] = useState<CaptureRegion>(null);
  const captureRegionRef = useRef<CaptureRegion>(null);
  const videoResizeHandlerRef = useRef<(() => void) | null>(null); // cleanup for video resize listener
  const captureMapLogCount = useRef(0); // throttled log counter for capture-map
  const [keyboardEnabled, setKeyboardEnabled] = useState(true);
  const [touchMode, setTouchMode] = useState<TouchMode>('multi-touch');
  const [dragMethods, setDragMethods] = useState<Set<DragMethod>>(new Set(['long-press']));
  const [roundTripMs, setRoundTripMs] = useState<number | null>(null);
  const [fps, setFps] = useState<number | null>(null);
  const [remoteKeysPressed, setRemoteKeysPressed] = useState<number[]>([]);
  const [connectionMode, setConnectionMode] = useState<string>('—');
  const iceServersRef = useRef<RTCIceServer[]>([{ urls: 'stun:stun.l.google.com:19302' }]);

  // ---- Screen latency detection (blue→red flash test) ----
  type LatencyState = 'idle' | 'waiting-blue' | 'waiting-red' | 'done';
  const [latencyState, setLatencyState] = useState<LatencyState>('idle');
  const [videoLatencyMs, setVideoLatencyMs] = useState<number | null>(null);
  const [latencyError, setLatencyError] = useState<string | null>(null);
  const latencyRafRef = useRef<number | null>(null);
  const latencyTimeoutRef = useRef<number | null>(null);

  const statusButtonRef = useRef<HTMLButtonElement | null>(null);
  const inputButtonRef = useRef<HTMLButtonElement | null>(null);
  const qualityButtonRef = useRef<HTMLButtonElement | null>(null);
  const statusPopoverRef = useRef<HTMLDivElement | null>(null);
  const inputPopoverRef = useRef<HTMLDivElement | null>(null);
  const qualityPopoverRef = useRef<HTMLDivElement | null>(null);
  const captureSettingsButtonRef = useRef<HTMLButtonElement | null>(null);
  const captureSettingsPopoverRef = useRef<HTMLDivElement | null>(null);

  const prevFramesRef = useRef<number | null>(null);
  const prevTimeRef = useRef<number | null>(null);

  cursorModeRef.current = cursorMode;
  captureRegionRef.current = captureRegion;
  touchModeRef.current = touchMode;
  dragMethodsRef.current = dragMethods;

  const setInputEnabledSync = (enabled: boolean) => {
    inputEnabledRef.current = enabled;
    setInputEnabled(enabled);
    if (!enabled && document.pointerLockElement) {
      document.exitPointerLock();
    }
  };

  // ---- Coordinate mapping helpers ----

  /** Get the letterboxed content rect for the video element. Returns null if video not ready.
   *  When a capture-region is active, its width/height are the ground truth from
   *  the server and are used in preference to video.videoWidth, which may not
   *  update correctly when WebRTC resolution changes in-band (e.g. desktop →
   *  window capture restart). */
  function getContentRect(): { left: number; top: number; width: number; height: number } | null {
    const video = videoRef.current;
    if (!video) return null;

    const containerRect = video.getBoundingClientRect();
    const region = captureRegionRef.current;
    const videoWidth = (region && region.width > 0) ? region.width : (video.videoWidth || screenSize.width);
    const videoHeight = (region && region.height > 0) ? region.height : (video.videoHeight || screenSize.height);

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

  /** Map a clientX/clientY (viewport pixels) to remote screen coordinates. Returns null if outside content area.
   *  When a capture-region is active (single display or window mode), coordinates
   *  are offset so they map to the correct absolute screen position. */
  function mapClientToRemote(clientX: number, clientY: number): { x: number; y: number } | null {
    const video = videoRef.current;
    if (!video) return null;
    const cr = getContentRect();
    if (!cr) return null;
    if (clientX < cr.left || clientX > cr.left + cr.width || clientY < cr.top || clientY > cr.top + cr.height) {
      return null;
    }

    // Use capture-region dimensions as ground truth — the server sends the
    // exact capture area size. This avoids relying on video.videoWidth which
    // can be stale after WebRTC in-band resolution changes (ffmpeg restart
    // with different capture dimensions).
    const region = captureRegionRef.current;
    const remoteW = (region && region.width > 0) ? region.width : (video.videoWidth || screenSize.width);
    const remoteH = (region && region.height > 0) ? region.height : (video.videoHeight || screenSize.height);

    let x = Math.round(((clientX - cr.left) / cr.width) * remoteW);
    let y = Math.round(((clientY - cr.top) / cr.height) * remoteH);

    // Apply capture-region offset: when capturing a sub-region of the desktop
    // (single display or window), video pixel (0,0) maps to screen (offsetX, offsetY).
    // Since remoteW/remoteH already match the region dimensions, no scaling needed.
    if (region && (region.offsetX !== 0 || region.offsetY !== 0)) {
      x = region.offsetX + x;
      y = region.offsetY + y;
    }

    // Throttled debug log: every ~120 calls (~2s at 60fps input)
    captureMapLogCount.current++;
    if (captureMapLogCount.current % 120 === 0 && region) {
      console.debug('capture-map', {
        clientX, clientY,
        remoteW, remoteH,
        videoX: Math.round(((clientX - cr.left) / cr.width) * remoteW),
        videoY: Math.round(((clientY - cr.top) / cr.height) * remoteH),
        region: JSON.stringify(region),
        finalX: x, finalY: y,
        screenSize: JSON.stringify(screenSize),
      });
    }

    return { x, y };
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
        if (touchModeRef.current !== 'multi-touch') return;
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

    // ---- Touch mode: dispatcher → mode-specific handlers ----

    const clearLongPressTimer = () => {
      if (longPressTimerRef.current != null) {
        clearTimeout(longPressTimerRef.current);
        longPressTimerRef.current = null;
      }
    };

    const clearThreeFingerTimer = () => {
      if (threeFingerTimerRef.current != null) {
        clearTimeout(threeFingerTimerRef.current);
        threeFingerTimerRef.current = null;
      }
    };

    // Reset all touch tracking state (used on mode switch, reconnect, cancel).
    const resetTouchState = () => {
      clearLongPressTimer();
      clearThreeFingerTimer();
      fingerMapRef.current.clear();
      lastTapRef.current = null;
      if (dragStateRef.current.buttonDown) {
        sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
      }
      dragStateRef.current = { active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 };
    };

    // ── Multi-touch helpers (native touch injection) ──

    // Track which fingers are currently active for multi-touch cancel cleanup.
    const activeTouches = new Set<number>();

    const handleMultiTouchStart = (e: TouchEvent) => {
      e.preventDefault();
      const changedIds = new Set<number>();
      for (let i = 0; i < e.changedTouches.length; i++) {
        changedIds.add(e.changedTouches[i].identifier);
      }
      const touches: TouchContact[] = [];
      for (let i = 0; i < e.touches.length; i++) {
        const t = e.touches[i];
        activeTouches.add(t.identifier);
        const remote = mapClientToRemote(t.clientX, t.clientY);
        if (!remote) continue;
        if (changedIds.has(t.identifier)) {
          touches.push({ id: t.identifier, x: remote.x, y: remote.y, phase: "start" });
        } else {
          touches.push({ id: t.identifier, x: remote.x, y: remote.y, phase: "move" });
        }
      }
      if (touches.length > 0) {
        sendInput("input-touch", { touches });
      }
    };

    const handleMultiTouchMove = (e: TouchEvent) => {
      e.preventDefault();
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
    };

    const handleMultiTouchEnd = (e: TouchEvent) => {
      e.preventDefault();
      const endedIds = new Set<number>();
      for (let i = 0; i < e.changedTouches.length; i++) {
        endedIds.add(e.changedTouches[i].identifier);
        activeTouches.delete(e.changedTouches[i].identifier);
      }
      const touches: TouchContact[] = [];
      for (let i = 0; i < e.touches.length; i++) {
        const t = e.touches[i];
        const remote = mapClientToRemote(t.clientX, t.clientY);
        if (!remote) continue;
        touches.push({ id: t.identifier, x: remote.x, y: remote.y, phase: "move" });
      }
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
    };

    const handleMultiTouchCancel = (e: TouchEvent) => {
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

    // ── Mouse mode helpers (single-finger, absolute positioning) ──

    let mouseTrackedId: number | null = null;

    const handleMouseModeStart = (e: TouchEvent) => {
      // Only track the first finger
      if (mouseTrackedId != null) return;
      if (e.changedTouches.length === 0) return;
      const t = e.changedTouches[0];
      mouseTrackedId = t.identifier;
      const f = fingerMapRef.current;
      f.set(t.identifier, {
        id: t.identifier,
        startClientX: t.clientX,
        startClientY: t.clientY,
        lastClientX: t.clientX,
        lastClientY: t.clientY,
        startTime: performance.now(),
        totalDelta: 0,
        moved: false,
      });
      // No immediate input — defer to move/end for tap vs drag detection
    };

    const handleMouseModeMove = (e: TouchEvent) => {
      const f = fingerMapRef.current;
      for (let i = 0; i < e.changedTouches.length; i++) {
        const t = e.changedTouches[i];
        if (t.identifier !== mouseTrackedId) continue;
        const ft = f.get(t.identifier);
        if (!ft) continue;
        const dx = Math.abs(t.clientX - ft.startClientX);
        const dy = Math.abs(t.clientY - ft.startClientY);
        ft.totalDelta = dx + dy;
        ft.lastClientX = t.clientX;
        ft.lastClientY = t.clientY;
        if (ft.totalDelta > 5) {
          ft.moved = true;
          // Start drag if not already dragging
          if (!dragStateRef.current.active) {
            dragStateRef.current = { active: true, method: null, buttonDown: true, averageStartX: 0, averageStartY: 0 };
            const remote = mapClientToRemote(ft.startClientX, ft.startClientY);
            if (remote) {
              sendInput('input-mousemove-abs', { x: remote.x, y: remote.y });
              sendInput('input-mousebtn', { button: 1, pressed: true, x: remote.x, y: remote.y });
            }
          }
        }
        if (ft.moved && dragStateRef.current.active) {
          const remote = mapClientToRemote(t.clientX, t.clientY);
          if (remote) {
            sendInput('input-mousemove-abs', { x: remote.x, y: remote.y });
          }
        }
      }
    };

    const handleMouseModeEnd = (e: TouchEvent) => {
      const f = fingerMapRef.current;
      for (let i = 0; i < e.changedTouches.length; i++) {
        const t = e.changedTouches[i];
        if (t.identifier !== mouseTrackedId) continue;
        const ft = f.get(t.identifier);
        mouseTrackedId = null;
        if (!ft) return;
        f.delete(t.identifier);

        if (dragStateRef.current.active && dragStateRef.current.buttonDown) {
          // Release drag
          sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
          dragStateRef.current = { active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 };
        } else {
          // Short tap → move + click
          const duration = performance.now() - ft.startTime;
          if (duration < 300 && ft.totalDelta < 10) {
            const remote = mapClientToRemote(ft.startClientX, ft.startClientY);
            if (remote) {
              sendInput('input-mousemove-abs', { x: remote.x, y: remote.y });
              sendInput('input-mousebtn', { button: 1, pressed: true, x: remote.x, y: remote.y });
              sendInput('input-mousebtn', { button: 1, pressed: false, x: remote.x, y: remote.y });
            }
          }
        }
        break; // only process the tracked finger
      }
    };

    const handleMouseModeCancel = () => {
      if (dragStateRef.current.buttonDown) {
        sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
      }
      fingerMapRef.current.clear();
      mouseTrackedId = null;
      dragStateRef.current = { active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 };
    };

    // ── Trackpad mode helpers (relative movement + drag state machine) ──

    const handleTrackpadStart = (e: TouchEvent) => {
      e.preventDefault();
      const f = fingerMapRef.current;
      const now = performance.now();

      for (let i = 0; i < e.changedTouches.length; i++) {
        const t = e.changedTouches[i];
        f.set(t.identifier, {
          id: t.identifier,
          startClientX: t.clientX,
          startClientY: t.clientY,
          lastClientX: t.clientX,
          lastClientY: t.clientY,
          startTime: now,
          totalDelta: 0,
          moved: false,
        });
      }

      // Double-tap detection: second tap within 0.5s after a single tap
      if (f.size === 1 && lastTapRef.current && dragMethodsRef.current.has('double-tap')) {
        const tap = lastTapRef.current;
        if (now - tap.time < 500) {
          // Enter double-tap drag mode
          clearLongPressTimer();
          lastTapRef.current = null;
          dragStateRef.current = { active: true, method: 'double-tap', buttonDown: true, averageStartX: 0, averageStartY: 0 };
          const remote = mapClientToRemote(tap.x, tap.y);
          if (remote) {
            sendInput('input-mousebtn', { button: 1, pressed: true, x: remote.x, y: remote.y });
          }
          return;
        }
      }

      // Three-finger drag: enter or re-enter
      if (f.size >= 3 && dragMethodsRef.current.has('three-finger')) {
        clearLongPressTimer();
        clearThreeFingerTimer();

        if (dragStateRef.current.active && dragStateRef.current.method === 'three-finger') {
          // Re-grab within 0.6s — continue seamlessly
          dragStateRef.current.buttonDown = true;
        } else if (!dragStateRef.current.active) {
          // Start three-finger drag
          // Use first 3 fingers for average start position
          const fingers = Array.from(f.values()).slice(0, 3);
          const avgX = fingers.reduce((s, ft) => s + ft.startClientX, 0) / fingers.length;
          const avgY = fingers.reduce((s, ft) => s + ft.startClientY, 0) / fingers.length;
          const remote = mapClientToRemote(avgX, avgY);
          dragStateRef.current = { active: true, method: 'three-finger', buttonDown: true, averageStartX: remote ? remote.x : 0, averageStartY: remote ? remote.y : 0 };
          if (remote) {
            sendInput('input-mousebtn', { button: 1, pressed: true, x: remote.x, y: remote.y });
          }
        }
        return;
      }

      // Long-press timer for single finger, if not already dragging
      if (f.size === 1 && !dragStateRef.current.active && dragMethodsRef.current.has('long-press')) {
        const ft = f.values().next().value as FingerTrack;
        clearLongPressTimer();
        longPressTimerRef.current = window.setTimeout(() => {
          // Check finger is still down and hasn't moved significantly
          const cur = f.get(ft.id);
          if (cur && !cur.moved) {
            if (navigator.vibrate) navigator.vibrate(50);
            const remote = mapClientToRemote(cur.startClientX, cur.startClientY);
            if (remote) {
              dragStateRef.current = { active: true, method: 'long-press', buttonDown: true, averageStartX: 0, averageStartY: 0 };
              sendInput('input-mousebtn', { button: 1, pressed: true, x: remote.x, y: remote.y });
            }
          }
          longPressTimerRef.current = null;
        }, 800);
      }
    };

    const handleTrackpadMove = (e: TouchEvent) => {
      e.preventDefault();
      const f = fingerMapRef.current;

      // Compute scale factor: 1 client pixel → remote pixels.
      // Without this the cursor moves only a fraction of the expected distance
      // when the remote desktop is higher-res than the viewport content area.
      const cr = getContentRect();
      let scaleX = 1, scaleY = 1;
      if (cr) {
        const video = videoRef.current;
        const remoteW = (video && video.videoWidth) || screenSize.width;
        const remoteH = (video && video.videoHeight) || screenSize.height;
        if (cr.width > 0 && cr.height > 0) {
          scaleX = remoteW / cr.width;
          scaleY = remoteH / cr.height;
        }
      }

      // Phase 1: compute deltas for all changed fingers (before updating lastClientX/Y)
      const deltas = new Map<number, { dx: number; dy: number }>();
      for (let i = 0; i < e.changedTouches.length; i++) {
        const t = e.changedTouches[i];
        const ft = f.get(t.identifier);
        if (!ft) continue;
        const dx = Math.round((t.clientX - ft.lastClientX) * scaleX);
        const dy = Math.round((t.clientY - ft.lastClientY) * scaleY);
        deltas.set(t.identifier, { dx, dy });
        const d = Math.abs(dx) + Math.abs(dy);
        ft.totalDelta += d;
        if (ft.totalDelta > 3 * Math.max(scaleX, scaleY)) {
          ft.moved = true;
          clearLongPressTimer(); // cancel long-press if finger moved
        }
      }

      // Phase 2: send movement using recorded deltas
      if (dragStateRef.current.active && dragStateRef.current.buttonDown) {
        if (dragStateRef.current.method === 'three-finger') {
          if (f.size >= 3) {
            let totalDx = 0, totalDy = 0, count = 0;
            for (const [id] of f) {
              const d = deltas.get(id);
              if (d) { totalDx += d.dx; totalDy += d.dy; count++; }
            }
            if (count > 0) {
              const dx = Math.round(totalDx / count);
              const dy = Math.round(totalDy / count);
              if (dx !== 0 || dy !== 0) {
                sendInput('input-mousemove', { x: dx, y: dy });
              }
            }
          }
        } else {
          // long-press or double-tap: single finger delta
          if (f.size === 1) {
            const ft = f.values().next().value as FingerTrack;
            const d = deltas.get(ft.id);
            if (d && (d.dx !== 0 || d.dy !== 0)) {
              sendInput('input-mousemove', { x: d.dx, y: d.dy });
            }
          }
        }
      } else {
        // Not dragging — normal relative movement
        if (f.size === 1) {
          const ft = f.values().next().value as FingerTrack;
          const d = deltas.get(ft.id);
          if (d && (d.dx !== 0 || d.dy !== 0)) {
            sendInput('input-mousemove', { x: d.dx, y: d.dy });
          }
        } else if (f.size === 2) {
          // Two-finger scroll
          const fingers = Array.from(f.values());
          const d1 = deltas.get(fingers[0].id);
          const d2 = deltas.get(fingers[1].id);
          if (d1 && d2) {
            const avgDx = (d1.dx + d2.dx) / 2;
            const avgDy = (d1.dy + d2.dy) / 2;
            if (Math.abs(avgDy) >= Math.abs(avgDx)) {
              if (Math.abs(avgDy) > 0.5) {
                sendInput('input-scroll', { dx: 0, dy: avgDy * 2 });
              }
            } else {
              if (Math.abs(avgDx) > 0.5) {
                sendInput('input-mousemove', { x: avgDx, y: avgDy });
              }
            }
          }
        }
      }

      // Phase 3: update tracking positions AFTER sending movement
      for (let i = 0; i < e.changedTouches.length; i++) {
        const t = e.changedTouches[i];
        const ft = f.get(t.identifier);
        if (ft) {
          ft.lastClientX = t.clientX;
          ft.lastClientY = t.clientY;
        }
      }
    };

    const handleTrackpadEnd = (e: TouchEvent) => {
      e.preventDefault();
      const f = fingerMapRef.current;
      const now = performance.now();

      if (dragStateRef.current.active && dragStateRef.current.buttonDown) {
        if (dragStateRef.current.method === 'three-finger') {
          // Check if any fingers remain; if not, start 0.6s release timer
          // (only count fingers in current changed list that are being removed)
          const endedIds = new Set<number>();
          for (let i = 0; i < e.changedTouches.length; i++) {
            endedIds.add(e.changedTouches[i].identifier);
          }
          // Remove ended fingers from our tracking
          for (const id of endedIds) {
            f.delete(id);
          }
          if (f.size < 3) {
            // Start 0.6s grace period for re-grab
            clearThreeFingerTimer();
            threeFingerTimerRef.current = window.setTimeout(() => {
              if (dragStateRef.current.buttonDown) {
                sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
                dragStateRef.current = { active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 };
              }
              threeFingerTimerRef.current = null;
            }, 600);
          }
          return; // Don't clean up fingers — they're already removed above
        } else {
          // long-press or double-tap: release mouse on any finger up
          sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
          dragStateRef.current = { active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 };
        }
      } else {
        // Not dragging — detect taps
        // Two-finger tap → right click
        if (e.changedTouches.length === 2) {
          const t1 = e.changedTouches[0];
          const t2 = e.changedTouches[1];
          const ft1 = f.get(t1.identifier);
          const ft2 = f.get(t2.identifier);
          if (ft1 && ft2) {
            const d1 = performance.now() - ft1.startTime;
            const d2 = performance.now() - ft2.startTime;
            if (d1 < 300 && d2 < 300 && ft1.totalDelta < 10 && ft2.totalDelta < 10) {
              const remote = mapClientToRemote(
                (ft1.startClientX + ft2.startClientX) / 2,
                (ft1.startClientY + ft2.startClientY) / 2,
              );
              if (remote) {
                sendInput('input-mousebtn', { button: 2, pressed: true, x: remote.x, y: remote.y });
                sendInput('input-mousebtn', { button: 2, pressed: false, x: remote.x, y: remote.y });
              }
            }
          }
        }

        // Single-finger tap → left click (only if double-tap didn't trigger)
        if (e.changedTouches.length === 1) {
          const t = e.changedTouches[0];
          const ft = f.get(t.identifier);
          if (ft) {
            const duration = now - ft.startTime;
            if (!ft.moved && duration < 300) {
              // Only register last-tap for double-tap if not already in drag mode
              // and the tap didn't just trigger drag (checked above)
              if (dragMethodsRef.current.has('double-tap') && !dragStateRef.current.active) {
                lastTapRef.current = { time: now, x: t.clientX, y: t.clientY };
              }
              const remote = mapClientToRemote(t.clientX, t.clientY);
              if (remote) {
                sendInput('input-mousebtn', { button: 1, pressed: true, x: remote.x, y: remote.y });
                sendInput('input-mousebtn', { button: 1, pressed: false, x: remote.x, y: remote.y });
              }
            }
          }
        }
      }

      // Clean up ended fingers from tracking
      for (let i = 0; i < e.changedTouches.length; i++) {
        f.delete(e.changedTouches[i].identifier);
      }

      // Clear last-tap after 0.5s if no double-tap happened
      if (lastTapRef.current && now - lastTapRef.current.time > 500) {
        lastTapRef.current = null;
      }
    };

    const handleTrackpadCancel = (_e: TouchEvent) => {
      clearLongPressTimer();
      clearThreeFingerTimer();
      if (dragStateRef.current.buttonDown) {
        sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
      }
      fingerMapRef.current.clear();
      lastTapRef.current = null;
      dragStateRef.current = { active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 };
    };

    // ── Top-level dispatcher handlers (attached to video element) ──

    const handleTouchStart = (e: TouchEvent) => {
      if (!inputEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;

      switch (touchModeRef.current) {
        case 'disabled': return;
        case 'multi-touch': handleMultiTouchStart(e); break;
        case 'mouse': handleMouseModeStart(e); break;
        case 'trackpad': handleTrackpadStart(e); break;
      }
    };

    const handleTouchMove = (e: TouchEvent) => {
      if (!inputEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;

      switch (touchModeRef.current) {
        case 'disabled': return;
        case 'multi-touch': handleMultiTouchMove(e); break;
        case 'mouse': handleMouseModeMove(e); break;
        case 'trackpad': handleTrackpadMove(e); break;
      }
    };

    const handleTouchEnd = (e: TouchEvent) => {
      if (!inputEnabledRef.current) return;
      if (cursorModeRef.current === 'disabled') return;

      switch (touchModeRef.current) {
        case 'disabled': return;
        case 'multi-touch': handleMultiTouchEnd(e); break;
        case 'mouse': handleMouseModeEnd(e); break;
        case 'trackpad': handleTrackpadEnd(e); break;
      }
    };

    const handleTouchCancel = (e: TouchEvent) => {
      switch (touchModeRef.current) {
        case 'disabled': return;
        case 'multi-touch': handleMultiTouchCancel(e); break;
        case 'mouse': handleMouseModeCancel(); break;
        case 'trackpad': handleTrackpadCancel(e); break;
      }
    };

    // Helper: find a touch by identifier in a TouchList
    function findTouchById(list: TouchList, id: number): Touch | undefined {
      for (let i = 0; i < list.length; i++) {
        if (list[i].identifier === id) return list[i];
      }
      return undefined;
    }

    // Mouse buttons (shared between modes)
    // Physical mouse clicks are always handled here regardless of touch mode.
    // Touch synthesised mouse events are suppressed by preventDefault in touch handlers.
    const handleMouseDown = (e: MouseEvent) => {
      if (cursorModeRef.current === 'disabled') return;
      if (!inputEnabledRef.current) return;
      const btn = e.button;
      let button = 1;
      if (btn === 0) button = 1;
      else if (btn === 2) button = 2;
      else if (btn === 1) button = 4;
      // Map through the same coordinate pipeline used by mousemove so clicks land
      // exactly where the cursor was placed.
      const remote = mapClientToRemote(e.clientX, e.clientY);
      if (!remote) return;
      sendInput('input-mousebtn', { button, pressed: true, x: remote.x, y: remote.y });
    };

    const handleMouseUp = (e: MouseEvent) => {
      if (cursorModeRef.current === 'disabled') return;
      if (!inputEnabledRef.current) return;
      const btn = e.button;
      let button = 1;
      if (btn === 0) button = 1;
      else if (btn === 2) button = 2;
      else if (btn === 1) button = 4;
      const remote = mapClientToRemote(e.clientX, e.clientY);
      if (!remote) return;
      sendInput('input-mousebtn', { button, pressed: false, x: remote.x, y: remote.y });
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
        iceServers: iceServersRef.current,
      });
      pcRef.current = pc;

      const videoTransceiver = pc.addTransceiver('video', { direction: 'recvonly' });

      // Minimise jitter buffer for LAN (Chrome 129+/Edge 129+).
      // On LAN with near-zero packet loss we ask for the minimum playout delay
      // (0) so frames display as soon as decoded instead of buffering ~50ms.
      const setLowLatency = () => {
        try {
          const r = videoTransceiver.receiver;
          // setJitterBufferMinimumDelay is the modern API (replaces
          // playoutDelayHint, which Chrome deprecates). Call both for coverage.
          if (r && 'setJitterBufferMinimumDelay' in r) {
            (r as any).setJitterBufferMinimumDelay(0);
          }
          if (r && 'playoutDelayHint' in r) {
            (r as any).playoutDelayHint = 0;
          }
        } catch { /* unsupported browser */ }
      };
      setLowLatency();

      pc.ontrack = (event) => {
        const [stream] = event.streams;
        if (videoRef.current && stream) {
          // Use video metadata as authoritative screen size — it IS the
          // captured desktop, so its intrinsic resolution trumps the server-
          // reported GetSystemMetrics value (which can be wrong under DPI
          // virtualization or multi-monitor setups).
          // Listen for resize events to detect in-band resolution changes
          // (e.g. desktop → window capture restart). loadedmetadata only
          // fires once; resize fires whenever intrinsic dimensions change.
          const onResize = () => {
            const v = videoRef.current;
            if (v && v.videoWidth > 0 && v.videoHeight > 0) {
              setScreenSize({ width: v.videoWidth, height: v.videoHeight });
            }
          };
          videoRef.current.addEventListener('loadedmetadata', onResize, { once: true });
          videoRef.current.addEventListener('resize', onResize);
          videoResizeHandlerRef.current = onResize;
          videoRef.current.srcObject = stream;
          // If metadata already loaded (e.g. reconnection), apply immediately.
          if (videoRef.current.videoWidth > 0) onResize();
          const playResult = videoRef.current.play();
          if (playResult && playResult.catch) {
            playResult.catch(() => undefined);
          }
          // Some browsers only accept playoutDelayHint once tracks are flowing.
          setTimeout(setLowLatency, 100);

          // Per-frame display timing via requestVideoFrameCallback (Chrome/Edge).
          // Logs mediaTime, presentation delay, and frame gap every 60 frames.
          if ('requestVideoFrameCallback' in (videoRef.current as any)) {
            let fc = 0;
            let lastMedia: number | null = null;
            const onFrame = (_now: DOMHighResTimeStamp, md: any) => {
              fc++;
              if (fc % 60 === 0) {
                const gap = lastMedia !== null ? md.mediaTime - lastMedia : 0;
                const delay = md.presentationTime - md.expectedDisplayTime;
                logger.info('frame-presented', {
                  mediaTime: Number(md.mediaTime.toFixed(3)),
                  gapMs: Number((gap * 1000).toFixed(2)),
                  delayMs: Number((delay * 1000).toFixed(2)),
                  presented: md.presentedFrames,
                });
              }
              lastMedia = md.mediaTime;
              (videoRef.current as any)?.requestVideoFrameCallback(onFrame);
            };
            (videoRef.current as any).requestVideoFrameCallback(onFrame);
          }
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
    let iceStatsTimer: number | undefined;

    const ws = createSignalingSocket(room);
    wsRef.current = ws;

    ws.onopen = () => {
      setState('open');
      setLogWs(ws);
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

      // Poll ICE stats every 2s to detect connection mode (direct vs relay).
      const pollIceMode = () => {
        const pc = pcRef.current;
        if (!pc || pc.connectionState === 'closed') return;
        pc.getStats().then((stats) => {
          let localCandidateId = '';
          stats.forEach((r) => {
            if (r.type === 'candidate-pair' && r.state === 'succeeded') {
              localCandidateId = r.localCandidateId;
            }
          });
          if (!localCandidateId) return;
          stats.forEach((r) => {
            if (r.type === 'local-candidate' && r.id === localCandidateId) {
              const ct: string = r.candidateType || '';
              if (ct === 'relay') setConnectionMode('中继');
              else if (ct === 'srflx') setConnectionMode('直连 (STUN)');
              else if (ct === 'host') setConnectionMode('直连 (局域网)');
              else if (ct === 'prflx') setConnectionMode('直连 (P2P)');
            }
          });
        }).catch(() => undefined);
      };
      iceStatsTimer = window.setInterval(pollIceMode, 2000);
      pollIceMode(); // fire once immediately after a short delay for ICE to settle
      setTimeout(pollIceMode, 5000);

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
        // Use ICE servers from the server config if provided, otherwise keep
        // the default STUN-only fallback.
        const payload = message.payload as { clientId: string; iceServers?: RTCIceServer[] };
        if (payload.iceServers && payload.iceServers.length > 0) {
          iceServersRef.current = payload.iceServers;
        }
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
        logger.info('cursor-image received', { w: ci.width, h: ci.height, hx: ci.hotspotX, hy: ci.hotspotY, len: ci.data.length });
        setCursorImage(ci);
      } else if (message.type === 'screen-size' && message.payload) {
        const size = message.payload as { width: number; height: number };
        setScreenSize({ width: size.width, height: size.height });
      } else if (message.type === 'capture-region' && message.payload) {
        const region = message.payload as CaptureRegion;
        logger.info('capture-region received', region);
        setCaptureRegion(region);
      } else if (message.type === 'input-key-state' && message.payload) {
        setRemoteKeysPressed(message.payload as number[]);
      }
    };

    ws.onerror = () => { setState('error'); setLogWs(null); };
    ws.onclose = () => { setState('closed'); setLogWs(null); };

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
    // Mouse click / scroll / contextmenu handlers attach to the video element so
    // clicks on popovers, the icon bar, and other page chrome do not trigger
    // remote mouse events on the controlled machine.
    const videoEl = videoRef.current;
    if (videoEl) {
      videoEl.addEventListener('mousedown', handleMouseDown);
      videoEl.addEventListener('mouseup', handleMouseUp);
      videoEl.addEventListener('wheel', handleWheel, { passive: false });
    }
    const handleBlur = () => {
      for (const keyCode of heldKeys) {
        sendInput('input-keyup', { keyCode });
      }
      heldKeys.clear();
    };
    window.addEventListener('blur', handleBlur);
    document.addEventListener('keydown', handleKeyDown);
    document.addEventListener('keyup', handleKeyUp);
    if (videoEl) {
      videoEl.addEventListener('click', handleDocumentClick);
      videoEl.addEventListener('contextmenu', handleContextMenu);
    }
    // Touch handlers attach to the video element so that touches on
    // popovers, the icon bar, and other page chrome are not swallowed by
    // the preventDefault call inside the handler (which suppresses click events).
    if (videoEl) {
      videoEl.addEventListener('touchstart', handleTouchStart, { passive: false });
      videoEl.addEventListener('touchmove', handleTouchMove, { passive: false });
      videoEl.addEventListener('touchend', handleTouchEnd);
      videoEl.addEventListener('touchcancel', handleTouchCancel);
    }

    return () => {
      if (pingTimer !== undefined) window.clearInterval(pingTimer);
      if (statsTimer !== undefined) window.clearInterval(statsTimer);
      if (clockTimer !== undefined) window.clearInterval(clockTimer);
      if (iceStatsTimer !== undefined) window.clearInterval(iceStatsTimer);
      if (latencyRafRef.current != null) cancelAnimationFrame(latencyRafRef.current);
      if (latencyTimeoutRef.current != null) clearTimeout(latencyTimeoutRef.current);
      // Reset touch tracking state on reconnect
      if (longPressTimerRef.current != null) clearTimeout(longPressTimerRef.current);
      if (threeFingerTimerRef.current != null) clearTimeout(threeFingerTimerRef.current);
      fingerMapRef.current.clear();
      lastTapRef.current = null;
      if (dragStateRef.current.buttonDown) {
        // If mouse button is held, release it before reconnect
        sendInput('input-mousebtn', { button: 1, pressed: false, x: 0, y: 0 });
      }
      dragStateRef.current = { active: false, method: null, buttonDown: false, averageStartX: 0, averageStartY: 0 };
      if (document.pointerLockElement === videoRef.current) document.exitPointerLock();
      document.removeEventListener('pointerlockchange', handlePointerLockChange);
      document.removeEventListener('mousemove', handleMouseMoveRemote);
      window.removeEventListener('blur', handleBlur);
      if (videoRef.current) {
        videoRef.current.removeEventListener('mousedown', handleMouseDown);
        videoRef.current.removeEventListener('mouseup', handleMouseUp);
        videoRef.current.removeEventListener('wheel', handleWheel);
        videoRef.current.removeEventListener('click', handleDocumentClick);
        videoRef.current.removeEventListener('contextmenu', handleContextMenu);
      }
      for (const keyCode of heldKeys) {
        sendInput('input-keyup', { keyCode });
      }
      heldKeys.clear();
      document.removeEventListener('keydown', handleKeyDown);
      document.removeEventListener('keyup', handleKeyUp);
      if (videoRef.current) {
        videoRef.current.removeEventListener('touchstart', handleTouchStart);
        videoRef.current.removeEventListener('touchmove', handleTouchMove);
        videoRef.current.removeEventListener('touchend', handleTouchEnd);
        videoRef.current.removeEventListener('touchcancel', handleTouchCancel);
      }
      pcRef.current?.close();
      pcRef.current = null;
      setLogWs(null);
      ws.close();
      wsRef.current = null;
      startedRef.current = false;
      if (videoRef.current) {
        if (videoResizeHandlerRef.current) {
          videoRef.current.removeEventListener('resize', videoResizeHandlerRef.current);
          videoResizeHandlerRef.current = null;
        }
        videoRef.current.srcObject = null;
      }
    };
  }, [room, connectionKey, keyboardEnabled]);

  // ---- Client / remote-render cursor mode: video mousemove → absolute position + cursor overlay ----

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;

    let lastTime = 0;

    const handler = (e: MouseEvent) => {
      const mode = cursorModeRef.current;
      if (mode !== 'client' && mode !== 'remote-render') return;
      if (!inputEnabledRef.current) return;
      // Don't send absolute mouse position from physical mouse when touch mode
      // is trackpad or mouse — the touch handlers manage all input.
      if (touchModeRef.current === 'trackpad' || touchModeRef.current === 'mouse') return;

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
      if (
        qualityOpen &&
        qualityPopoverRef.current && qualityButtonRef.current &&
        !qualityPopoverRef.current.contains(e.target as Node) &&
        !qualityButtonRef.current.contains(e.target as Node)
      ) {
        setQualityOpen(false);
      }
      if (
        captureSettingsOpen &&
        captureSettingsPopoverRef.current && captureSettingsButtonRef.current &&
        !captureSettingsPopoverRef.current.contains(e.target as Node) &&
        !captureSettingsButtonRef.current.contains(e.target as Node)
      ) {
        setCaptureSettingsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [statusOpen, inputMenuOpen, qualityOpen, captureSettingsOpen]);

  // Fetch sessions/displays/windows when capture settings popover opens.
  useEffect(() => {
    if (!captureSettingsOpen) return;
    fetch('/api/sessions').then(r => r.json()).then(setSessions).catch(console.error);
    fetch('/api/displays').then(r => r.json()).then(setDisplays).catch(console.error);
    fetch('/api/windows').then(r => r.json()).then(data => {
      setWindows(data);
      logger.info('windows list fetched', { count: data?.length, titles: data?.map((w: { title: string }) => w.title) });
      // Auto-select first window if in window mode with no title.
      if (captureMode === 'window' && !selectedWindowTitle && data && data.length > 0) {
        logger.info('auto-selecting first window', { title: data[0].title });
        setSelectedWindowTitle(data[0].title);
      }
    }).catch(err => { logger.error('fetch windows failed', err); console.error(err); });
  }, [captureSettingsOpen]);

  const sendCaptureSettings = (
    sid: number, mode: CaptureMode, displayIdx: number,
    windowTitle: string, transpBg: WindowTransparencyBg,
  ) => {
    const ws = wsRef.current;
    logger.info('capture-settings sending', { mode, displayIdx, windowTitle, transpBg, sessionId: sid });
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({
        type: 'capture-settings',
        payload: { sessionId: sid, captureMode: mode, displayIndex: displayIdx, windowTitle, windowTransparencyBg: transpBg },
      }));
    } else {
      logger.warn('capture-settings: WebSocket not open', { readyState: ws?.readyState });
    }
  };

  const reconnect = () => {
    if (document.pointerLockElement === videoRef.current) {
      document.exitPointerLock();
    }
    setStatusOpen(false);
    setInputMenuOpen(false);
    setQualityOpen(false);
    setCaptureSettingsOpen(false);
    setConnectionKey((k) => k + 1);
  };

  const sendReleaseAll = useCallback(() => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: 'input-release-all' }));
  }, []);

  // ---- Screen latency detection ----
  // The server shows a topmost blue window; we watch the decoded video's center
  // pixel. When blue appears we start a timer and tell the server to flip to
  // red; when red appears we stop the timer. The interval spans
  // signaling-up + server-draw + video-down — real interactive latency.

  const stopLatencySampling = useCallback(() => {
    if (latencyRafRef.current != null) {
      cancelAnimationFrame(latencyRafRef.current);
      latencyRafRef.current = null;
    }
    if (latencyTimeoutRef.current != null) {
      clearTimeout(latencyTimeoutRef.current);
      latencyTimeoutRef.current = null;
    }
  }, []);

  const sendLatency = (type: 'latency-start' | 'latency-blue' | 'latency-red') => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type }));
    }
  };

  const runLatencyTest = () => {
    const video = videoRef.current;
    if (!video || video.videoWidth === 0) {
      setLatencyError('视频未就绪');
      return;
    }
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      setLatencyError('未连接');
      return;
    }

    stopLatencySampling();
    setVideoLatencyMs(null);
    setLatencyError(null);
    setLatencyState('waiting-blue');
    sendLatency('latency-start');

    const sampleSize = 16;
    const canvas = document.createElement('canvas');
    canvas.width = sampleSize;
    canvas.height = sampleSize;
    const ctx = canvas.getContext('2d', { willReadFrequently: true });
    if (!ctx) {
      setLatencyError('无法创建画布');
      setLatencyState('idle');
      return;
    }

    let phase: 'waiting-blue' | 'waiting-red' = 'waiting-blue';
    let tBlue = 0;

    const classify = (r: number, g: number, b: number): 'blue' | 'red' | null => {
      if (b > 150 && r < 100 && g < 100) return 'blue';
      if (r > 150 && b < 100 && g < 100) return 'red';
      return null;
    };

    const sample = () => {
      const v = videoRef.current;
      if (!v || v.videoWidth === 0) {
        latencyRafRef.current = requestAnimationFrame(sample);
        return;
      }
      const sx = Math.max(0, Math.floor(v.videoWidth / 2 - sampleSize / 2));
      const sy = Math.max(0, Math.floor(v.videoHeight / 2 - sampleSize / 2));
      try {
        ctx.drawImage(v, sx, sy, sampleSize, sampleSize, 0, 0, sampleSize, sampleSize);
      } catch {
        latencyRafRef.current = requestAnimationFrame(sample);
        return;
      }
      const data = ctx.getImageData(0, 0, sampleSize, sampleSize).data;
      let r = 0, g = 0, b = 0;
      const n = sampleSize * sampleSize;
      for (let i = 0; i < data.length; i += 4) {
        r += data[i];
        g += data[i + 1];
        b += data[i + 2];
      }
      const c = classify(r / n, g / n, b / n);

      if (phase === 'waiting-blue' && c === 'blue') {
        tBlue = performance.now();
        phase = 'waiting-red';
        setLatencyState('waiting-red');
        sendLatency('latency-blue');
      } else if (phase === 'waiting-red' && c === 'red') {
        const ms = performance.now() - tBlue;
        stopLatencySampling();
        setVideoLatencyMs(ms);
        setLatencyState('done');
        sendLatency('latency-red');
        return;
      }
      latencyRafRef.current = requestAnimationFrame(sample);
    };

    latencyRafRef.current = requestAnimationFrame(sample);

    // Abort + close the server window if nothing is detected in time.
    latencyTimeoutRef.current = window.setTimeout(() => {
      stopLatencySampling();
      setLatencyState('idle');
      setLatencyError('检测超时（未观察到颜色变化）');
      sendLatency('latency-red');
    }, 10000);
  };

  const getOverlayText = () => {
    if (!inputEnabled) return null;
    if (cursorMode === 'disabled') return '输入已启用（鼠标已禁用）';
    if (cursorMode === 'remote') {
      return inputLocked ? '点击或按 ESC 释放' : '点击视频锁定鼠标';
    }
    if (cursorMode === 'remote-render') return '输入已启用（光标在视频中渲染）';
    if (touchMode === 'trackpad') return '触摸板模式（相对移动）';
    if (touchMode === 'mouse') return '模拟鼠标模式（绝对定位）';
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
        <button ref={qualityButtonRef} className={`iconButtonWrap ${qualityOpen ? 'active' : ''}`} title="画质设置" onClick={() => setQualityOpen((o) => !o)}>
          <QualityIcon />
        </button>
        <button ref={captureSettingsButtonRef} className={`iconButtonWrap ${captureSettingsOpen ? 'active' : ''}`} title="采集设置" onClick={() => setCaptureSettingsOpen((o) => !o)}>
          <CaptureSettingsIcon />
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
            <div><dt>连接模式</dt><dd>{connectionMode}</dd></div>
            <div><dt>帧率</dt><dd>{fps !== null ? fps.toFixed(1) : '-'}</dd></div>
            <div><dt>延迟</dt><dd>{roundTripMs !== null ? `${roundTripMs.toFixed(1)} 毫秒` : '-'}</dd></div>
            <div><dt>视频</dt><dd>{videoStats}</dd></div>
            <div>
              <dt>时钟</dt>
              <dd>{clock.toLocaleTimeString()}.{clock.getMilliseconds().toString().padStart(3, '0')}</dd>
            </div>
          </dl>
          <div className="latencyTestSection">
            <button
              className="latencyTestBtn"
              onClick={runLatencyTest}
              disabled={latencyState === 'waiting-blue' || latencyState === 'waiting-red'}
            >
              {latencyState === 'waiting-blue' || latencyState === 'waiting-red' ? '检测中…' : '画面延迟检测'}
            </button>
            {videoLatencyMs !== null && (
              <span className="latencyTestResult">画面延迟 {videoLatencyMs.toFixed(1)} 毫秒</span>
            )}
            {latencyState === 'waiting-blue' && <span className="latencyTestHint">等待蓝色…</span>}
            {latencyState === 'waiting-red' && <span className="latencyTestHint">等待红色…</span>}
            {latencyError && <span className="latencyTestError">{latencyError}</span>}
          </div>
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
          {/* Touch mode dropdown */}
          <div className="selectRow">
            <span className="selectLabel">触摸</span>
            <select
              className="cursorModeSelect"
              value={touchMode}
              onChange={(e) => {
                const mode = e.target.value as TouchMode;
                setTouchMode(mode);
                touchModeRef.current = mode;
              }}
            >
              <option value="disabled">禁用</option>
              <option value="multi-touch">多点触摸</option>
              <option value="mouse">模拟鼠标</option>
              <option value="trackpad">模拟触摸板</option>
            </select>
          </div>

          {/* Drag method checkboxes — only shown in trackpad mode */}
          {touchMode === 'trackpad' && (
            <div className="checkboxGroup">
              <span className="selectLabel">拖动方法</span>
              {(['long-press', 'double-tap', 'three-finger'] as DragMethod[]).map((method) => {
                const labels: Record<DragMethod, string> = {
                  'long-press': '长按',
                  'double-tap': '双击',
                  'three-finger': '三指',
                };
                const checked = dragMethods.has(method);
                return (
                  <label key={method} className="checkboxRow">
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={(e) => {
                        const next = new Set(dragMethods);
                        e.target.checked ? next.add(method) : next.delete(method);
                        setDragMethods(next);
                        dragMethodsRef.current = next;
                      }}
                    />
                    <span className="checkboxLabel">{labels[method]}</span>
                  </label>
                );
              })}
            </div>
          )}
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
              : touchMode === 'trackpad'
                ? '触摸板模式：滑动=移动光标，点按=点击，双指=右键，双指滑动=滚动'
                : touchMode === 'mouse'
                  ? '模拟鼠标模式：点按=点击，拖动=拖拽'
                : cursorMode === 'remote'
                  ? (inputLocked ? '点击或按 ESC 释放' : '点击视频锁定鼠标')
                  : cursorMode === 'remote-render'
                    ? '移动鼠标到视频上即可控制远程（光标在视频中渲染）'
                    : '移动鼠标到视频上即可控制远程'}
          </div>
        </div>
      )}

      {qualityOpen && (
        <div className="popover qualityPopover" ref={qualityPopoverRef}>
          <h3>画质设置</h3>
          {(['smooth', 'balanced', 'quality'] as QualityPreset[]).map((preset) => {
            const labels: Record<QualityPreset, { title: string; desc: string }> = {
              smooth: { title: '流畅优先', desc: '低码率 · 最低延迟 · 适合弱网' },
              balanced: { title: '均衡', desc: '中等码率 · 兼顾画质与延迟' },
              quality: { title: '画质优先', desc: '高码率 · 最佳画质 · 适合局域网' },
            };
            const info = labels[preset];
            return (
              <label
                key={preset}
                className={`presetOption ${qualityPreset === preset ? 'presetOption--selected' : ''}`}
              >
                <input
                  type="radio"
                  name="qualityPreset"
                  value={preset}
                  checked={qualityPreset === preset}
                  onChange={() => {
                    setQualityPreset(preset);
                    const ws = wsRef.current;
                    if (ws && ws.readyState === WebSocket.OPEN) {
                      ws.send(JSON.stringify({ type: 'quality-preset', payload: { preset } }));
                      console.log('quality-preset sent:', preset);
                    }
                  }}
                />
                <span className="presetTitle">{info.title}</span>
                <span className="presetDesc">{info.desc}</span>
              </label>
            );
          })}
        </div>
      )}

      {captureSettingsOpen && (
        <div className="popover captureSettingsPopover" ref={captureSettingsPopoverRef}>
          <h3>采集设置</h3>

          {/* Session selector */}
          <div className="selectRow">
            <span className="selectLabel">会话</span>
            <select className="cursorModeSelect" value={sessionId}
              onChange={(e) => {
                const id = Number(e.target.value);
                setSessionId(id);
                sendCaptureSettings(id, captureMode, selectedDisplayIndex, selectedWindowTitle, windowTransparencyBg);
              }}>
              {sessions.map(s => (
                <option key={s.id} value={s.id}>{s.name} — {s.userName || '(无用户)'} ({s.state})</option>
              ))}
            </select>
          </div>

          {/* Capture mode radio cards */}
          <div className="captureModeCards">
            {([
              { value: 'desktop' as CaptureMode, title: '全部显示器', desc: '显示所有显示器组合画面' },
              { value: 'display' as CaptureMode, title: '指定显示器', desc: '选择单个显示器进行采集' },
              { value: 'window' as CaptureMode, title: '指定窗口', desc: '只采集指定窗口的画面' },
            ]).map(opt => (
              <label key={opt.value}
                className={`presetOption ${captureMode === opt.value ? 'presetOption--selected' : ''}`}>
                <input type="radio" name="captureMode" value={opt.value}
                  checked={captureMode === opt.value}
                  onChange={() => {
                    setCaptureMode(opt.value);
                    logger.info('capture mode changed', { from: captureMode, to: opt.value });
                    // Auto-select first window when switching to window mode with no title set.
                    let winTitle = selectedWindowTitle;
                    let transpBg = windowTransparencyBg;
                    if (opt.value === 'window' && !winTitle && windows.length > 0) {
                      winTitle = windows[0].title;
                      setSelectedWindowTitle(winTitle);
                      logger.info('auto-selected window on mode switch', { title: winTitle });
                    }
                    sendCaptureSettings(sessionId, opt.value, selectedDisplayIndex, winTitle, transpBg);
                  }} />
                <span className="presetTitle">{opt.title}</span>
                <span className="presetDesc">{opt.desc}</span>
              </label>
            ))}
          </div>

          {/* Display selector — only in display mode */}
          {captureMode === 'display' && (
            <div className="selectRow">
              <span className="selectLabel">显示器</span>
              <select className="cursorModeSelect" value={selectedDisplayIndex}
                onChange={(e) => {
                  const idx = Number(e.target.value);
                  setSelectedDisplayIndex(idx);
                  sendCaptureSettings(sessionId, captureMode, idx, selectedWindowTitle, windowTransparencyBg);
                }}>
                {displays.map(d => (
                  <option key={d.index} value={d.index}>{d.name}{d.primary ? ' (主)' : ''} — {d.width}x{d.height}</option>
                ))}
              </select>
            </div>
          )}

          {/* Window selector + transparency — only in window mode */}
          {captureMode === 'window' && (
            <>
              <div className="selectRow">
                <span className="selectLabel">窗口</span>
                <select className="cursorModeSelect" value={selectedWindowTitle}
                  onChange={(e) => {
                    const title = e.target.value;
                    logger.info('window selected', { title, previousTitle: selectedWindowTitle });
                    setSelectedWindowTitle(title);
                    sendCaptureSettings(sessionId, captureMode, selectedDisplayIndex, title, windowTransparencyBg);
                  }}>
                  {windows.map(w => (
                    <option key={w.title} value={w.title}>{w.title}</option>
                  ))}
                </select>
              </div>
              <div className="selectRow">
                <span className="selectLabel">透明背景</span>
                <select className="cursorModeSelect" value={windowTransparencyBg}
                  onChange={(e) => {
                    const bg = e.target.value as WindowTransparencyBg;
                    setWindowTransparencyBg(bg);
                    sendCaptureSettings(sessionId, captureMode, selectedDisplayIndex, selectedWindowTitle, bg);
                  }}>
                  <option value="">保持透明（穿透）</option>
                  <option value="black">纯黑</option>
                  <option value="white">纯白</option>
                </select>
              </div>
            </>
          )}

          <div className="inputHint">
            {captureMode === 'desktop' ? '采集全部显示器组合画面，输入映射到完整桌面' :
             captureMode === 'display' ? '只采集选中显示器，输入自动映射到正确位置' :
             '采集指定窗口，透明区域可选纯色背景'}
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
          style={inputEnabled && (cursorMode === 'client' || cursorMode === 'remote-render' || touchMode === 'trackpad' || touchMode === 'mouse') ? { cursor: 'none' } : undefined}
        />
        {inputEnabled && overlayText && (
          <div className="inputOverlay"><span>{overlayText}</span></div>
        )}
        {/* Remote cursor overlay: shown for remote mode, or when touchMode is trackpad/mouse */}
        {inputEnabled && (cursorMode === 'remote' || touchMode === 'trackpad' || touchMode === 'mouse') && (
          <RemoteCursor videoRef={videoRef} cursorPos={cursorPos} screenSize={screenSize} cursorImage={cursorImage} captureRegion={captureRegion} />
        )}
        {/* Client cursor overlay: only when cursorMode=client AND not in trackpad/mouse touch mode */}
        {inputEnabled && cursorMode === 'client' && touchMode !== 'trackpad' && touchMode !== 'mouse' && (
          <CursorOverlay position={clientCursorScreenPos} cursorImage={cursorImage} />
        )}
        {/* Remote-render mode: cursor is rendered in the video stream — no overlay needed */}
      </section>
    </main>
  );
}
