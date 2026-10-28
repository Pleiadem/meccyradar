import { useEffect, useRef, useState, type CSSProperties, type FormEvent, type ReactNode, type RefObject } from 'react'
import { AnimatePresence, motion, useReducedMotion } from 'motion/react'
import { canToggleTunnel, remainingTimeLabel, tunnelButtonHint, tunnelButtonLabel } from './tunnel_button'
import { AlertTriangle, CheckCircle2, Copy, Eye, EyeOff, Loader2, Minus, Power, Radar, RefreshCw, ServerCog, Settings, X, XCircle } from 'lucide-react'
import './index.css'
import { isVersionSkipped, shouldOfferVersion, skipVersion } from './version_update.js'
import { createVersionCheckRunner, versionCheckButtonState } from './version_check.js'
import { alreadyAuthenticatedMessage, confirmSameActiveSessionConflict } from './session_conflict.js'
import { applyLinkAckEvent } from './link_ack_event.js'
import { sessionInvalidatedState } from './session_state.js'

type VersionInfo = { configured: boolean; version: string; downloadUrl: string; notes: string; force: boolean }
type VersionSnapshot = { info: VersionInfo; currentVersion: string; requiredVersion: string; downloadUrl: string; blocked: boolean; checking: boolean; checkError: string }

type InitState = {
  authenticated: boolean
  appName?: string
  expireAtStr?: string
  expireAtUnix?: number
  expiresIn?: number
  tunnelStatus: string
  leaseId?: string
  linkVersion?: number
  copyPending?: boolean
}

type AuthResult = {
  appName: string
  expireAtStr: string
  expireAtUnix: number
  tunnelStatus: string
  warning?: string
  leaseId?: string
  linkVersion?: number
  copyPending?: boolean
}

type ShareLinkAttempt = { attempted: boolean; copied: boolean; link?: string; error?: string }
type TunnelStatus = { status: string; pingMs?: number; expiresIn?: number; message?: string; timeout?: boolean; done?: boolean }
type PortStatus = { open: boolean }
type LogEvent = { message: string; at: string }
type LinkAckEvent = { accepted: boolean; leaseId: string; linkVersion: number }
// 后端算好的失败诊断：标题 + 解决建议（认不出类型时只有原始错误）
type Diagnosis = { level: 'warning' | 'error'; title: string; hint?: string; raw?: string }

type Backend = {
  GetInitState?: () => Promise<InitState>
  LoginByCard?: (cardKey: string) => Promise<AuthResult>
  StartTunnel?: () => Promise<void>
  StopTunnel?: () => Promise<void>
  CopyShareLink?: (password: string) => Promise<void>
  CopyLinkForDelivery?: (password: string, version: number) => Promise<ShareLinkAttempt>
  RadarPassword?: () => Promise<string>
  SaveRadarPassword?: (password: string) => Promise<void>
  RemeasurePing?: () => Promise<void>
  CloseWindow?: () => void
  SavedCardKey?: () => Promise<string>
  Logout?: () => Promise<void>
  ChangeServer?: () => Promise<AuthResult>
  AutostartEnabled?: () => Promise<boolean>
  AutostartSupported?: () => Promise<boolean>
  SetAutostart?: (enabled: boolean) => Promise<void>
  CheckVersion?: () => Promise<void>
  VersionState?: () => Promise<VersionSnapshot>
  IsVersionBlocked?: () => Promise<boolean>
  VersionCheckPending?: () => Promise<boolean>
  OpenVersionDownload?: () => Promise<void>
}

type Runtime = {
  EventsOn?: (eventName: string, callback: (...data: unknown[]) => void) => () => void
  WindowMinimise?: () => void
  Quit?: () => void
}

const backend = (window as Window & { go?: { main?: { App?: Backend } } }).go?.main?.App
const runtime = (window as Window & { runtime?: Runtime }).runtime

const changeServerSuccessNotice = '已更换服务器，请重新复制最新链接'
const versionStorage = {
  getItem: (key: string) => { try { return window.localStorage.getItem(key) } catch { return null } },
  setItem: (key: string, value: string) => { try { window.localStorage.setItem(key, value) } catch { throw new Error('本机设置存储不可用') } },
}

export default function App() {
  const [init, setInit] = useState<InitState>({ authenticated: false, tunnelStatus: 'idle' })
  const initRef = useRef(init)
  initRef.current = init
  const lastLinkAckRef = useRef<LinkAckEvent | null>(null)
  const [hydrated, setHydrated] = useState(false)
  const [versionInfo, setVersionInfo] = useState<VersionInfo>()
  const [versionBlocked, setVersionBlocked] = useState(false)
  const [versionChecking, setVersionChecking] = useState(true)
  const [versionCheckError, setVersionCheckError] = useState('')
  const [versionUpToDate, setVersionUpToDate] = useState(false)
  const [versionPrompt, setVersionPrompt] = useState(false)
  const [versionDismissed, setVersionDismissed] = useState(false)
  const [currentVersion, setCurrentVersion] = useState('')
  const [requiredVersion, setRequiredVersion] = useState('')
  const [versionDownloadURL, setVersionDownloadURL] = useState('')
  const [cardKey, setCardKey] = useState('')
  const [showCardKey, setShowCardKey] = useState(false)
  const [radarPassword, setRadarPassword] = useState('')
  const [showRadarPassword, setShowRadarPassword] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const [linkPrompt, setLinkPrompt] = useState<{ version: number } | null>(null)
  const [copyFailure, setCopyFailure] = useState<{ link: string; message: string } | null>(null)
  const [pingMs, setPingMs] = useState<number>()
  const [measuring, setMeasuring] = useState(false)
  const measureTimer = useRef<number | undefined>(undefined)
  const [portOpen, setPortOpen] = useState(false)
  const [logs, setLogs] = useState<LogEvent[]>([])
  const [diagnosis, setDiagnosis] = useState<Diagnosis>()
  // 设置面板
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [autostart, setAutostart] = useState(false)
  const [autostartSupported, setAutostartSupported] = useState(true)
  const [changingServer, setChangingServer] = useState(false)
  const [confirmingChange, setConfirmingChange] = useState(false)
  const [settingsError, setSettingsError] = useState('')
  const [settingsNotice, setSettingsNotice] = useState('')
  const [settingsWarning, setSettingsWarning] = useState(false)
  const logBox = useRef<HTMLDivElement>(null)
  const versionRequest = useRef(false)
  const versionRunner = useRef<ReturnType<typeof createVersionCheckRunner> | undefined>(undefined)

  const refreshAuthoritativeInit = async () => {
    if (!backend?.GetInitState) return
    try {
      const state = await backend.GetInitState()
      if (!state) return
      const current = publishInitState(state)
      if (!current.authenticated) {
        setLinkPrompt(null)
        setCopyFailure(null)
      }
    } catch {
      // Keep the original action error visible when authoritative refresh fails.
    }
  }

  const publishInitState = (state: InitState): InitState => {
    const ack = lastLinkAckRef.current
    const matchesAck = Boolean(ack?.accepted && state.authenticated && state.leaseId === ack.leaseId && state.linkVersion === ack.linkVersion)
    const next = ack ? applyLinkAckEvent(state, ack) : state
    initRef.current = next
    setInit(next)
    if (matchesAck && ack) setLinkPrompt((prompt) => prompt?.version === ack.linkVersion ? null : prompt)
    return next
  }

  const applyVersionSnapshot = (snapshot: VersionSnapshot, manual: boolean) => {
    setCurrentVersion(snapshot.currentVersion)
    setRequiredVersion(snapshot.requiredVersion)
    setVersionDownloadURL(snapshot.downloadUrl)
    setVersionChecking(snapshot.checking || versionRequest.current)
    setVersionBlocked(snapshot.blocked)
    setVersionCheckError(snapshot.checkError || '')
    if (snapshot.info?.configured) {
      setVersionInfo(snapshot.info)
      const skipped = isVersionSkipped(versionStorage, snapshot.info.version)
      const offer = shouldOfferVersion({ currentVersion:snapshot.currentVersion, version:snapshot.info.version, force:snapshot.info.force, skipped, manual })
      setVersionPrompt(offer && !snapshot.blocked)
      setVersionDismissed(false)
    } else { setVersionInfo(undefined); setVersionPrompt(false) }
  }

  const refreshVersionCheck = async (manual: boolean) => {
    if (versionRequest.current) return
    if (!versionRunner.current) {
      versionRunner.current = createVersionCheckRunner({
        backend: backend ?? {},
        onKnown: (snapshot) => {
          setCurrentVersion(snapshot.currentVersion)
          setRequiredVersion(snapshot.requiredVersion)
          setVersionDownloadURL(snapshot.downloadUrl)
          setVersionBlocked(snapshot.blocked)
        },
        onSnapshot: (snapshot, isManual) => applyVersionSnapshot(snapshot, isManual),
        onError: (message) => {
          setVersionCheckError(message)
          setVersionChecking(false)
          void backend?.IsVersionBlocked?.().then(setVersionBlocked).catch(() => setVersionBlocked(false))
        },
      })
    }
    versionRequest.current = true
    setVersionChecking(true)
    setVersionUpToDate(false)
    setVersionCheckError('')
    try {
      const result = await versionRunner.current.run(manual)
      if (result && manual) setVersionUpToDate(!shouldOfferVersion({ currentVersion: result.currentVersion, version: result.info?.version ?? '', force: result.info?.force ?? false, skipped: isVersionSkipped(versionStorage, result.info?.version ?? ''), manual: true }))
    } finally {
      versionRequest.current = false
      setVersionChecking(false)
    }
  }

  useEffect(() => {
    void refreshVersionCheck(false)
    if (backend?.GetInitState) {
      void backend.GetInitState().then((state) => {
        if (state) {
          const current = publishInitState(state)
          if (current.authenticated && current.copyPending && current.linkVersion) setLinkPrompt({ version: current.linkVersion })
        }
        setHydrated(true)
        // 没进主界面说明自动登录没成（或还没跑完），把上次的卡密填好，
        // 用户只需点一下登录，不用重新手打。
        if (!state?.authenticated && backend?.SavedCardKey) {
          void backend.SavedCardKey().then((saved) => { if (saved) setCardKey((current) => current || saved) }).catch(() => {})
        }
      }).catch(() => setHydrated(true))
    } else {
      setHydrated(true)
    }
    const offVersion = runtime?.EventsOn?.('version:checked', (...data) => {
      const snapshot = data[0] as VersionSnapshot | undefined
      if (snapshot) applyVersionSnapshot(snapshot, false)
    })
    const offStatus = runtime?.EventsOn?.('tunnel:status', (...data) => {
      const event = data[0] as TunnelStatus | undefined
      if (!event) return
      setInit((current) => ({ ...current, tunnelStatus: event.status, expiresIn: event.expiresIn ?? current.expiresIn }))
      // 直接采用后端算好的平均值（去极值后的均值），不再自己取最小值：
      // 平均值可能比上一次高，那正是要如实反映的结果。
      if (typeof event.pingMs === 'number') setPingMs(event.pingMs)
      // 只有整轮结束（done）或错误才算终态。
      // 中间样本同样是成功事件：若拿它解除"测速中"，按钮会在测速还没跑完时
      // 重新可点，用户第二次点击会被后端以"测速进行中"拒绝，
      // 而那一轮又不会再发终态事件 —— 界面就卡在"测速中…"了。
      if (event.done || event.status === 'error') stopMeasuring()
      // 隧道重新连上就把上一次的失败提示收起来，避免旧提示一直挂着。
      if (event.status === 'connected' || event.status === 'connecting') setDiagnosis(undefined)
      if (event.message) setError(event.message)
    })
    const offPort = runtime?.EventsOn?.('port:status', (...data) => {
      const event = data[0] as PortStatus | undefined
      if (event) setPortOpen(event.open)
    })
    const offSessionInvalidated = runtime?.EventsOn?.('session:invalidated', () => {
      publishInitState(sessionInvalidatedState())
      setLinkPrompt(null)
      setCopyFailure(null)
    })
    const offLinkAck = runtime?.EventsOn?.('link:acknowledged', (...data) => {
      const event = data[0] as LinkAckEvent | undefined
      if (!event) return
      lastLinkAckRef.current = event
      const current = initRef.current
      const matchesAck = current.authenticated && current.leaseId === event.leaseId && current.linkVersion === event.linkVersion
      const next = applyLinkAckEvent(current, event)
      if (next !== current) {
        initRef.current = next
        setInit(next)
      }
      if (matchesAck) setLinkPrompt((prompt) => prompt?.version === event.linkVersion ? null : prompt)
    })
    // 自动登录是在后端后台完成的，前端挂载时可能还没结束。
    // 没有这个事件的话，自动登录成功后界面仍然停在登录页。
    const offRestored = runtime?.EventsOn?.('session:restored', (...data) => {
      const state = data[0] as InitState | undefined
      if (state) {
        const current = publishInitState(state)
        if (current.copyPending && current.linkVersion) setLinkPrompt({ version: current.linkVersion })
      }
      // 自动登录成功后把记住的雷达密码也填回去，否则界面是空的、
      // 复制出来的链接会缺 k=。
      if (backend?.RadarPassword) {
        void backend.RadarPassword().then((saved) => { if (saved) setRadarPassword(saved) }).catch(() => {})
      }
    })
    const offDiagnosis = runtime?.EventsOn?.('tunnel:diagnosis', (...data) => {
      const event = data[0] as Diagnosis | undefined
      if (event?.title) setDiagnosis(event)
    })
    const offLog = runtime?.EventsOn?.('log:append', (...data) => {
      const event = data[0] as LogEvent | undefined
      if (event) setLogs((current) => [...current.slice(-99), event])
    })
    return () => {
      window.clearTimeout(measureTimer.current)
      offVersion?.()
      offStatus?.()
      offPort?.()
      offLinkAck?.()
      offSessionInvalidated?.()
      offRestored?.()
      offDiagnosis?.()
      offLog?.()
    }
  }, [])

  useEffect(() => {
    if (!init.authenticated || !backend?.RadarPassword) return
    void backend.RadarPassword().then((saved) => { if (saved) setRadarPassword(saved) }).catch(() => { /* 回填失败不阻塞界面 */ })
  }, [init.authenticated])

  useEffect(() => {
    if (logBox.current) logBox.current.scrollTop = logBox.current.scrollHeight
  }, [logs])

  useEffect(() => {
    if (import.meta.env.DEV && new URLSearchParams(window.location.search).get('preview') === 'console') {
      setInit({ authenticated: true, appName: 'MeccyRadar', expireAtStr: '测试授权', expireAtUnix: Math.floor(Date.now() / 1000) + 3 * 86400, tunnelStatus: 'connected' })
      setPingMs(28)
      setPortOpen(true)
      setCardKey('CARD-1')
      setRadarPassword('sample-pass')
      setLogs([{ at: '12:30:04', message: '隧道连接成功，等待客户端请求' }, { at: '12:30:08', message: '本地代理状态正常' }])
    }
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => {
      setInit((current) => current.expireAtUnix ? { ...current, expiresIn: Math.max(0, current.expireAtUnix - Math.floor(Date.now() / 1000)) } : current)
    }, 1000)
    return () => window.clearInterval(timer)
  }, [])

  const handleLogin = async (event: FormEvent) => {
    event.preventDefault()
    if (!cardKey.trim() || !backend?.LoginByCard) return
    setBusy(true); setError('')
    try {
      const result = await backend.LoginByCard(cardKey.trim())
      const current = publishInitState({ authenticated: true, appName: result.appName, expireAtStr: result.expireAtStr, expireAtUnix: result.expireAtUnix, tunnelStatus: result.tunnelStatus, leaseId: result.leaseId, linkVersion: result.linkVersion, copyPending: result.copyPending })
      setCopyFailure(null)
      if (current.copyPending && current.linkVersion) setLinkPrompt({ version: current.linkVersion })
    } catch (cause) {
      const message = toMessage(cause)
      // An automatic restore may finish immediately before this manual call.
      // Treat only the same-card duplicate as already successful, confirmed by
      // backend state and stored identity (never by the error text alone).
      if (message === alreadyAuthenticatedMessage && backend?.GetInitState && backend?.SavedCardKey) {
        const state = await confirmSameActiveSessionConflict(message, cardKey, backend.GetInitState, backend.SavedCardKey)
        if (state) {
          const current = publishInitState(state)
          if (current.copyPending && current.linkVersion) setLinkPrompt({ version: current.linkVersion })
          setError('')
          return
        }
      }
      setError(message)
    } finally { setBusy(false) }
  }

  const toggleTunnel = async () => {
    if (busy) return
    setBusy(true); setError('')
    try {
      if (init.tunnelStatus === 'connected' || init.tunnelStatus === 'connecting') {
        await backend?.StopTunnel?.()
        setInit((current) => ({ ...current, tunnelStatus: 'idle' }))
      } else {
        await backend?.StartTunnel?.()
        setInit((current) => ({ ...current, tunnelStatus: 'connecting' }))
      }
    } catch (cause) {
      const message = toMessage(cause)
      await refreshAuthoritativeInit()
      setError(message)
    } finally { setBusy(false) }
  }

  const stopMeasuring = () => {
    window.clearTimeout(measureTimer.current)
    setMeasuring(false)
  }

  const startMeasuring = () => {
    setMeasuring(true)
    window.clearTimeout(measureTimer.current)
    // 兜底：即使后端一个事件都没回来（进程被杀、事件通道断开），
    // 界面也不会永久停在"测速中…"。
    measureTimer.current = window.setTimeout(() => {
      setMeasuring(false)
      setError('测速超时，点击重试')
    }, 30000)
  }

  const remeasure = async () => {
    if (measuring) return
    startMeasuring()
    try {
      await backend?.RemeasurePing?.()
    } catch (cause) {
      const message = toMessage(cause)
      // 已有测速在跑：保持禁用，等那一轮的 done 事件解除；不算错误。
      // 重新计时兜底定时器，确保即使那个事件丢了也不会永久卡住。
      if (message.includes('测速进行中')) {
        startMeasuring()
        return
      }
      stopMeasuring()
      setError(message)
    }
  }

  const performCopyAttempt = async (version: number): Promise<ShareLinkAttempt> => {
    if (!backend?.CopyLinkForDelivery) throw new Error('链接复制服务不可用，请重启客户端')
    return backend.CopyLinkForDelivery(radarPassword, version)
  }

  const copyLink = async () => {
    const promptedVersion = linkPrompt?.version
    if (promptedVersion) setLinkPrompt(null)
    setBusy(true); setError(''); setCopied(false)
    try {
      const version = promptedVersion ?? init.linkVersion
      if (version && backend?.CopyLinkForDelivery) {
        const result = await performCopyAttempt(version)
        if (result.attempted && result.copied) { setCopied(true); setCopyFailure(null) }
        else if (result.attempted && result.link) setCopyFailure({ link: result.link, message: result.error || '剪贴板写入失败；你可以手动选择下方链接复制' })
      } else {
        await backend?.CopyShareLink?.(radarPassword)
        setCopied(true)
      }
    } catch (cause) { setError(toMessage(cause)) } finally { setBusy(false) }
  }

  const logout = async () => {
    setBusy(true)
    try { await backend?.Logout?.(); publishInitState({ authenticated: false, tunnelStatus: 'idle' }); setLinkPrompt(null); setError('') } catch (cause) { setError(toMessage(cause)) } finally { setBusy(false) }
  }

  const openSettings = async () => {
    setSettingsOpen(true); setSettingsError(''); setSettingsNotice(''); setSettingsWarning(false); setConfirmingChange(false)
    try {
      const supported = await backend?.AutostartSupported?.()
      setAutostartSupported(supported ?? false)
      const enabled = await backend?.AutostartEnabled?.()
      setAutostart(Boolean(enabled))
    } catch { /* 读不到就按默认（关、不支持）显示，不打断用户 */ }
  }

  const closeSettings = () => {
    if (changingServer) return
    setSettingsOpen(false); setConfirmingChange(false); setSettingsError(''); setSettingsNotice(''); setSettingsWarning(false)
  }

  const toggleAutostart = async () => {
    const next = !autostart
    setSettingsError(''); setSettingsNotice('')
    try {
      await backend?.SetAutostart?.(next)
      setAutostart(next)
      setSettingsNotice(next ? '已开启开机自动启动' : '已关闭开机自动启动')
    } catch (cause) { setSettingsError(toMessage(cause)) }
  }

  // 换服用两步确认（先点一次变"确认更换"，再点才执行）：
  // 无边框窗口里 window.confirm 的表现依赖 WebView2 配置，不如自己做稳。
  const requestChangeServer = () => {
    setSettingsError(''); setSettingsNotice(''); setSettingsWarning(false)
    if (!confirmingChange) { setConfirmingChange(true); return }
    void runChangeServer()
  }

  const runChangeServer = async () => {
    setChangingServer(true); setConfirmingChange(false); setSettingsError(''); setSettingsNotice(''); setSettingsWarning(false)
    try {
      const result = await backend?.ChangeServer?.()
      if (!result) throw new Error('换服服务不可用，请稍后重试')
      const next = publishInitState({ ...init, ...result, authenticated: true })
      setCopied(false)
      setSettingsWarning(Boolean(result.warning))
      setSettingsNotice('')
      setCopyFailure(null)
      if (next.copyPending && next.linkVersion) setLinkPrompt({ version: next.linkVersion })
    } catch (cause) {
      const message = toMessage(cause)
      await refreshAuthoritativeInit()
      setError(message)
      setSettingsError(message)
    } finally { setChangingServer(false) }
  }

  const logoutFromSettings = async () => {
    setSettingsOpen(false)
    await logout()
  }

  if (!hydrated) return <div className="flex min-h-screen items-center justify-center bg-black text-sm text-white/50">正在加载…</div>
  if (versionBlocked) {
    const knownRequiredVersion = Boolean(requiredVersion)
    const title = knownRequiredVersion ? '必须更新客户端' : '无法验证版本状态'
    const description = knownRequiredVersion ? '此版本已停止使用。请打开下载链接并手动下载、运行新版；安装新版后此提示会自动解除。' : '服务端或本机版本状态无法安全验证。为防止绕过更新策略，暂时阻止登录和隧道连接；请修复状态后重试。'
    return <WindowShell authenticated={false} onOpenSettings={() => {}}><section className="glass-panel mx-auto mt-24 max-w-lg space-y-4 p-6 text-white"><h1 className="text-xl font-semibold">{title}</h1><p>{description}</p><p>当前版本：{currentVersion || '未知'}{requiredVersion ? ` · 要求版本：${requiredVersion}` : ''}</p>{versionInfo?.notes&&<p>{versionInfo.notes}</p>}{versionCheckError&&<p role="alert" className="text-amber-200">{versionCheckError}</p>}<div className="flex gap-3">{versionDownloadURL&&<button className="rounded-lg bg-white px-4 py-2 text-slate-900 hover:bg-white/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white active:scale-95" onClick={()=>void backend?.OpenVersionDownload?.()}>打开下载页面</button>}<button className="rounded-lg border border-white/30 px-4 py-2 hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white active:scale-95" onClick={()=>void refreshVersionCheck(false)}>重新检查</button></div></section></WindowShell>
  }

  return <WindowShell
    authenticated={init.authenticated}
    onOpenSettings={() => { void openSettings() }}
    overlay={settingsOpen ? (
      <>
      <SettingsModal
        autostart={autostart}
        autostartSupported={autostartSupported}
        changingServer={changingServer}
        confirmingChange={confirmingChange}
        error={settingsError}
        notice={settingsNotice}
        warning={settingsWarning}
        versionChecking={versionChecking}
        versionCheckError={versionCheckError}
        versionUpToDate={versionUpToDate}
        onManualVersionCheck={() => { void refreshVersionCheck(true) }}
        onToggleAutostart={() => { void toggleAutostart() }}
        onChangeServer={requestChangeServer}
        onCancelChange={() => setConfirmingChange(false)}
        onLogout={() => { void logoutFromSettings() }}
        onClose={closeSettings}
      />
      {!versionBlocked && versionPrompt && versionInfo && <VersionUpdateModal info={versionInfo} onDownload={() => void backend?.OpenVersionDownload?.()} onLater={() => { setVersionDismissed(true); setVersionPrompt(false) }} onSkip={() => { if (skipVersion(versionStorage, versionInfo.version)) { setVersionPrompt(false); setVersionDismissed(true) } else { setVersionCheckError('无法保存跳过版本设置，请检查本机存储空间') } }} />}
      </>
    ) : versionBlocked ? undefined : versionPrompt && versionInfo ? (
      <VersionUpdateModal info={versionInfo} onDownload={() => void backend?.OpenVersionDownload?.()} onLater={() => { setVersionDismissed(true); setVersionPrompt(false) }} onSkip={() => { if (skipVersion(versionStorage, versionInfo.version)) { setVersionPrompt(false); setVersionDismissed(true) } else { setVersionCheckError('无法保存跳过版本设置，请检查本机存储空间') } }} />
    ) : versionCheckError && !versionDismissed ? (
      <OfflineVersionNotice message={versionCheckError} onDismiss={() => setVersionDismissed(true)} onRetry={() => { setVersionDismissed(false); void refreshVersionCheck(false) }} />
    ) : undefined}
  >
    {init.authenticated ? (
      <>
        <ConsoleView init={init} linkNotice={Boolean(linkPrompt && linkPrompt.version === init.linkVersion)} pingMs={pingMs} measuring={measuring} onRemeasure={remeasure} diagnosis={diagnosis} portOpen={portOpen} logs={logs} logBox={logBox} radarPassword={radarPassword} showRadarPassword={showRadarPassword} busy={busy} copied={copied} error={error} onPasswordChange={(value) => { setRadarPassword(value); void backend?.SaveRadarPassword?.(value) }} onTogglePassword={() => setShowRadarPassword(!showRadarPassword)} onToggleTunnel={toggleTunnel} onCopy={copyLink} onLogout={logout} />
        {copyFailure && <ShareLinkCopyFailure link={copyFailure.link} message={copyFailure.message} onDismiss={() => setCopyFailure(null)} />}
      </>
    ) : (
      <LoginView cardKey={cardKey} showCardKey={showCardKey} busy={busy} error={error} onCardKeyChange={setCardKey} onToggleCardKey={() => setShowCardKey(!showCardKey)} onSubmit={handleLogin} />
    )}
  </WindowShell>
}

function WindowShell({ authenticated, onOpenSettings, overlay, children }: { authenticated: boolean; onOpenSettings: () => void; overlay?: ReactNode; children: ReactNode }) {
  return <div className="glass-bg relative h-[100dvh] w-full overflow-hidden text-white">
    <div className="absolute left-0 top-0 z-20 h-11 w-[calc(100%-112px)]" style={{ '--wails-draggable': 'drag' } as CSSProperties}>
      {authenticated && <span className="absolute left-4 top-1/2 flex -translate-y-1/2 items-center gap-2 text-xs font-semibold tracking-wide text-white/85"><Radar size={15} strokeWidth={1.8} />MeccyRadar</span>}
    </div>
    <div className="absolute right-2 top-1.5 z-30 flex items-center gap-1" style={{ '--wails-draggable': 'no-drag' } as CSSProperties}>
      {authenticated && <button aria-label="设置" title="设置" onClick={onOpenSettings} className="mr-1 flex h-9 w-9 items-center justify-center rounded-lg text-white/75 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40"><Settings size={15} strokeWidth={1.8} /></button>}
      <button aria-label="最小化" title="最小化" onClick={() => runtime?.WindowMinimise?.()} className="flex h-9 w-9 items-center justify-center rounded-lg text-white/75 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40"><Minus size={15} strokeWidth={1.8} /></button>
      <button aria-label="关闭窗口" title="关闭窗口" onClick={() => backend?.CloseWindow?.()} className="flex h-9 w-9 items-center justify-center rounded-lg text-white/75 transition-colors hover:bg-rose-500/80 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-rose-300/70"><X size={15} strokeWidth={1.8} /></button>
    </div>
    <div className="h-full w-full">{children}</div>
    {overlay}
  </div>
}

function LoginView({ cardKey, showCardKey, busy, error, onCardKeyChange, onToggleCardKey, onSubmit }: { cardKey: string; showCardKey: boolean; busy: boolean; error: string; onCardKeyChange: (value: string) => void; onToggleCardKey: () => void; onSubmit: (event: FormEvent) => void }) {
  return <main className="glass-bg grid h-full min-h-0 w-full grid-cols-1 overflow-hidden px-3 pt-12 pb-12 text-white selection:bg-white/30 transition-colors duration-300 min-[700px]:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] min-[700px]:gap-6 min-[700px]:px-8">
    <section className="glass-panel relative hidden h-full min-h-0 min-w-0 flex-col justify-center overflow-hidden px-7 min-[700px]:flex"><div aria-hidden="true" className="absolute inset-0 bg-[radial-gradient(ellipse_at_28%_35%,rgba(72,98,117,0.38),transparent_52%),linear-gradient(145deg,#1c2228_0%,#090a0b_70%)]" /><motion.div initial="hidden" animate="visible" variants={{ hidden: {}, visible: { transition: { staggerChildren: 0.12, delayChildren: 0.08 } } }} className="relative z-10 w-full max-w-xs space-y-7"><motion.div variants={reveal} className="flex items-center gap-3 text-lg font-semibold tracking-tight"><span className="flex h-10 w-10 items-center justify-center rounded-xl border border-white/10 bg-white/[0.06]"><Radar size={20} strokeWidth={1.7} /></span><span>MeccyRadar</span></motion.div><motion.div variants={reveal} className="space-y-3"><h1 className="text-3xl font-medium tracking-tight">欢迎回来</h1><p className="text-sm leading-relaxed text-white/70">输入卡密，连接你的专属雷达。</p></motion.div><motion.div variants={reveal} className="flex items-center gap-3 border-t border-white/10 pt-5 text-xs text-white/60"><span className="h-2 w-2 rounded-full bg-emerald-300 shadow-[0_0_12px_rgba(110,231,183,0.35)]" />安全授权，稳定连接</motion.div></motion.div></section>
    <section className="flex min-h-0 min-w-0 flex-col items-center justify-center px-4 py-3 min-[700px]:items-end min-[700px]:px-0"><motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.4, ease: 'easeOut' }} className="glass-panel w-full max-w-sm space-y-6 p-6"><header className="space-y-2"><p className="text-[11px] font-medium tracking-[0.18em] text-white/55">MECCY RADAR</p><h2 className="text-2xl font-medium tracking-tight">卡密登录</h2><p className="text-[13px] text-white/65">授权后管理隧道连接和雷达分享。</p></header><form className="space-y-4" onSubmit={onSubmit}><label className="block text-[13px] font-medium text-white/90">卡密<span className="relative mt-2 block"><input required value={cardKey} onChange={(event) => onCardKeyChange(event.target.value)} type={showCardKey ? 'text' : 'password'} className="glass-field h-11 w-full px-4 pr-12 text-sm text-white outline-none transition-colors placeholder:text-white/45 hover:border-white/25 focus:border-white/40 focus:ring-2 focus:ring-white/15" placeholder="请输入卡密" /><button type="button" aria-label={showCardKey ? '隐藏卡密' : '显示卡密'} onClick={onToggleCardKey} className="absolute right-1 top-1/2 flex h-9 w-9 -translate-y-1/2 items-center justify-center rounded-lg text-white/65 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40">{showCardKey ? <EyeOff size={16} /> : <Eye size={16} />}</button></span></label>{error && <motion.p initial={{ opacity: 0, y: -4 }} animate={{ opacity: 1, y: 0 }} role="alert" className="rounded-xl border border-rose-400/25 bg-rose-400/10 px-4 py-3 text-xs text-rose-200">{error}</motion.p>}<button disabled={busy} type="submit" className="group flex h-11 w-full items-center justify-center gap-2 rounded-xl bg-white text-sm font-semibold text-black shadow-[0_8px_24px_rgba(255,255,255,0.07)] transition duration-200 hover:-translate-y-0.5 hover:bg-white/90 active:translate-y-0 active:scale-[0.99] disabled:cursor-wait disabled:opacity-60">{busy ? <><span className="h-4 w-4 animate-spin rounded-full border-2 border-black/25 border-t-black" />登录中…</> : '登录并连接'}</button></form><p className="text-[11px] leading-relaxed text-white/50">卡密用于识别授权状态，请勿分享给他人。</p></motion.div></section>
  </main>
}

function ConsoleView({ init, linkNotice, pingMs, measuring, onRemeasure, diagnosis, portOpen, logs, logBox, radarPassword, showRadarPassword, busy, copied, error, onPasswordChange, onTogglePassword, onToggleTunnel, onCopy }: { init: InitState; linkNotice: boolean; pingMs?: number; measuring: boolean; onRemeasure: () => void; diagnosis?: Diagnosis; portOpen: boolean; logs: LogEvent[]; logBox: RefObject<HTMLDivElement | null>; radarPassword: string; showRadarPassword: boolean; busy: boolean; copied: boolean; error: string; onPasswordChange: (value: string) => void; onTogglePassword: () => void; onToggleTunnel: () => void; onCopy: () => void; onLogout: () => void }) {
  const status = init.tunnelStatus
  const active = status === 'connected' || status === 'connecting'
  // 到期后不该还能点"启动隧道"：那只会让 FRPC 吐出英文登录失败。
  // 已连接/连接中仍然可点，否则过期账号会卡在连接状态里出不来。
  const canConnect = canToggleTunnel(init)
  const connectHint = tunnelButtonHint(init)
  // 隧道状态直接用后端已有的 tunnelStatus，不新增后端逻辑。
  // 只做文案与文字颜色，不再有任何光晕/发光效果。
  const tunnelLabel = status === 'connected' ? '运行中' : status === 'connecting' ? '连接中' : status === 'error' ? '已断开' : '未启动'
  const tunnelColor = status === 'connected' ? 'text-emerald-200' : status === 'connecting' ? 'text-amber-200' : status === 'error' ? 'text-rose-200' : 'text-white/45'
  return <main className="glass-bg h-full min-h-0 w-full overflow-hidden p-3 pt-12 text-white sm:p-4 sm:pt-12"><div className="grid h-full min-h-0 grid-cols-1 gap-3 min-[620px]:grid-cols-[minmax(0,0.88fr)_minmax(0,1.12fr)]"><motion.section initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.32 }} className="glass-panel flex min-h-0 flex-col justify-between p-4 sm:p-5"><div><div className="flex items-center justify-between"><p className="text-xs font-medium tracking-wide text-white/55">授权状态</p><span className={`rounded-full px-2.5 py-1 text-[11px] font-medium ${init.authenticated ? 'bg-emerald-300/10 text-emerald-200' : 'bg-white/10 text-white/50'}`}>{init.authenticated ? '已授权' : '未授权'}</span></div><p className="mt-5 text-2xl font-semibold tracking-tight sm:text-3xl">{remainingTimeLabel(init)}</p><p className="mt-2 text-xs text-white/55">到期时间：{init.expireAtStr ?? '—'}</p></div><div className="space-y-3"><button type="button" onClick={onRemeasure} disabled={measuring} title="点击重新测速" className="flex w-full items-center justify-between glass-field px-3 py-2.5 text-sm transition-colors hover:bg-white/[0.075] disabled:cursor-wait"><span className="text-white/70">节点延迟</span><span className="flex items-center gap-2 font-mono text-white/80">{measuring ? <><RefreshCw size={13} className="animate-spin" /><span className="text-white/60">测速中…</span></> : pingMs === undefined ? '点击测速' : `${pingMs} ms`}</span></button><div className="flex items-center justify-between glass-field px-3 py-2.5 text-sm"><span className="text-white/70">隧道状态</span><span className={tunnelColor}>{tunnelLabel}</span></div><div className="flex items-center justify-between glass-field px-3 py-2.5 text-sm"><span className="text-white/70">本地端口</span><span className={portOpen ? 'text-emerald-200' : 'text-white/45'}>{portOpen ? '已监听' : '未监听'}</span></div><button onClick={onToggleTunnel} disabled={busy || !canConnect} title={connectHint} className={`flex h-12 w-full items-center justify-center gap-2.5 rounded-xl font-semibold transition duration-200 active:scale-[0.985] disabled:cursor-not-allowed disabled:opacity-45 ${active ? 'bg-white text-black hover:bg-white/90' : 'border border-white/15 bg-white/[0.045] text-white hover:border-white/25 hover:bg-white/10'}`}><Power size={17} strokeWidth={1.8} />{tunnelButtonLabel(init)}</button></div></motion.section><section className="flex min-h-0 min-w-0 flex-col gap-3"><motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.36, delay: 0.05 }} className="glass-panel shrink-0 p-4"><div className="flex items-center justify-between gap-3"><label htmlFor="radar-password" className="text-sm font-medium text-white/90">雷达链接（请输入副机卡密后八位）</label></div><div className="mt-3 flex min-w-0 gap-2"><div className="relative min-w-0 flex-1"><input id="radar-password" value={radarPassword} onChange={(event) => onPasswordChange(event.target.value)} type={showRadarPassword ? 'text' : 'password'} className="glass-field h-11 w-full px-3 pr-10 text-sm text-white outline-none transition-colors placeholder:text-white/35 hover:border-white/25 focus:border-white/40 focus:ring-2 focus:ring-white/15" placeholder="输入副机卡密后八位" /><button type="button" onClick={onTogglePassword} className="absolute right-1 top-1/2 flex h-9 w-9 -translate-y-1/2 items-center justify-center rounded-lg text-white/50 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40" aria-label={showRadarPassword ? '隐藏密码' : '显示密码'}>{showRadarPassword ? <EyeOff size={16} /> : <Eye size={16} />}</button></div><button onClick={onCopy} disabled={busy} title={radarPassword.trim().length < 8 ? '副机卡密后八位通常为 8 个字符' : undefined} className={`flex h-11 shrink-0 items-center justify-center gap-2 rounded-xl bg-white px-3 text-sm font-semibold text-black transition duration-200 hover:-translate-y-0.5 hover:bg-white/90 active:translate-y-0 active:scale-[0.98] disabled:cursor-wait disabled:opacity-60 ${radarPassword.trim().length < 8 ? 'opacity-[0.45]' : ''}`}><Copy size={15} />{copied ? '已复制' : '复制'}</button></div>{linkNotice && <p role="status" className="mt-2 rounded-lg border border-emerald-300/20 bg-emerald-300/10 px-3 py-2 text-xs text-emerald-200 motion-reduce:transition-none">{changeServerSuccessNotice}</p>}{error && <motion.p initial={{ opacity: 0, y: -3 }} animate={{ opacity: 1, y: 0 }} role="alert" className="mt-2 rounded-lg bg-rose-400/10 px-3 py-2 text-xs text-rose-200">{error}</motion.p>}</motion.div><div className="glass-panel flex min-h-0 flex-1 flex-col overflow-hidden"><div className="flex h-10 shrink-0 items-center justify-between border-b border-white/[0.07] px-4"><span className="text-xs font-medium text-white/65">运行日志</span><span className="text-[11px] text-white/35">实时</span></div><div ref={logBox} className="min-h-0 flex-1 overflow-y-auto px-4 py-3 font-mono text-[11px] leading-5 text-white/55" aria-live="polite">{diagnosis && <motion.div initial={{ opacity: 0, y: -4 }} animate={{ opacity: 1, y: 0 }} role="alert" className={`mb-3 rounded-xl border px-3 py-2.5 font-sans text-xs ${diagnosis.level === 'warning' ? 'border-amber-300/30 bg-amber-300/10' : 'border-rose-400/30 bg-rose-400/10'}`}><div className={`flex items-center gap-2 font-semibold ${diagnosis.level === 'warning' ? 'text-amber-200' : 'text-rose-200'}`}>{diagnosis.level === 'warning' ? <AlertTriangle size={14} strokeWidth={2} /> : <XCircle size={14} strokeWidth={2} />}{diagnosis.title}</div>{diagnosis.hint ? <p className="mt-1 leading-relaxed text-white/60">{diagnosis.hint}</p> : diagnosis.raw ? <p className="mt-1 break-words leading-relaxed text-white/60">{diagnosis.raw}</p> : null}</motion.div>}{logs.length === 0 ? <p className="text-white/35">等待隧道日志…</p> : logs.map((log, index) => <p key={`${log.at}-${index}`} className="break-words"><span className="mr-2 text-white/35">{log.at}</span>{log.message}</p>)}</div></div></section></div></main>
}

function VersionUpdateModal({ info, onDownload, onLater, onSkip }: { info: VersionInfo; onDownload: () => void; onLater: () => void; onSkip: () => void }) {
  const reducedMotion = useReducedMotion()
  return <div className="absolute inset-0 z-50 flex items-center justify-center bg-black/60 p-4 backdrop-blur-sm" role="presentation"><motion.section initial={reducedMotion ? false : { opacity:0, y:8 }} animate={{ opacity:1, y:0 }} transition={{ duration:reducedMotion ? 0 : 0.18 }} role="dialog" aria-modal="true" aria-label="发现新版本" className="glass-panel w-full max-w-md space-y-4 p-6"><h2 className="text-lg font-semibold">发现新版本 {info.version}</h2><p className="text-sm text-white/70">{info.notes || '此版本包含改进。下载后请手动运行新版安装程序。'}</p><p className="text-xs text-white/45">下载链接将在系统浏览器打开；请手动核对并运行下载文件。客户端不会自动下载或安装。</p><div className="flex flex-wrap justify-end gap-2"><button type="button" onClick={onLater} autoFocus className="rounded-lg border border-white/20 px-3 py-2 text-sm hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40 active:scale-95 motion-reduce:transition-none">稍后</button><button type="button" onClick={onSkip} className="rounded-lg border border-white/20 px-3 py-2 text-sm hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40 active:scale-95">跳过此版本</button><button type="button" onClick={onDownload} className="rounded-lg bg-white px-3 py-2 text-sm font-semibold text-slate-900 hover:bg-white/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 active:scale-95">打开下载页</button></div></motion.section></div>
}

function ShareLinkCopyFailure({ link, message, onDismiss }: { link: string; message: string; onDismiss: () => void }) {
  return <section role="status" aria-label="复制结果" className="absolute inset-x-3 bottom-3 z-50 mx-auto max-w-2xl rounded-xl border border-amber-300/25 bg-slate-950/95 p-4 shadow-xl backdrop-blur">
    <div className="flex items-start justify-between gap-3"><div><h2 className="text-sm font-semibold text-amber-100">{message}</h2><p className="mt-1 text-xs text-white/65">复制尝试已发起，提示已关闭。可点击文本框并全选手动复制；无需再次确认。</p></div><button type="button" aria-label="关闭复制失败提示" onClick={onDismiss} className="rounded-lg p-1 text-white/70 transition hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60 active:scale-95"><X size={16}/></button></div>
    <textarea aria-label="可手动选择的雷达链接" readOnly value={link} onFocus={(event) => event.currentTarget.select()} className="mt-3 max-h-20 min-h-12 w-full select-all resize-y rounded-lg border border-white/15 bg-black/40 p-2 font-mono text-xs text-white/90 outline-none focus-visible:ring-2 focus-visible:ring-white/50" />
  </section>
}

function OfflineVersionNotice({ message, onDismiss, onRetry }: { message: string; onDismiss: () => void; onRetry: () => void }) {
  return <div className="pointer-events-none absolute inset-x-0 top-12 z-40 flex justify-center p-3"><section role="status" aria-label="版本检查状态" className="glass-panel pointer-events-auto w-full max-w-md space-y-3 p-4"><h2 className="font-semibold">无法确认更新状态</h2><p className="text-sm text-amber-200">{message}</p><p className="text-xs text-white/55">没有已知的未满足强制更新时，网络错误不会阻止使用；已知强制更新仍保持拦截。</p><div className="flex justify-end gap-2"><button type="button" onClick={onRetry} className="rounded-lg border border-white/20 px-3 py-2 text-sm hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40 active:scale-95">重试</button><button type="button" onClick={onDismiss} className="rounded-lg border border-white/20 px-3 py-2 text-sm hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40 active:scale-95">关闭提示</button></div></section></div>
}

function toMessage(cause: unknown) { return cause instanceof Error ? cause.message : typeof cause === 'string' ? cause : '操作失败，请稍后重试' }

type StepItemProps = { number: number; text: string; active?: boolean }
type SocialButtonProps = { icon: ReactNode; label: string }
type InputGroupProps = { label: string; placeholder: string; type?: string }
const reveal = { hidden: { opacity: 0, y: 10 }, visible: { opacity: 1, y: 0, transition: { duration: 0.5 } } }
function StepItem({ number, text, active = false }: StepItemProps) { return <motion.div variants={reveal} className={`flex items-center gap-3 rounded-xl px-3 py-2 text-sm ${active ? 'border border-white bg-white text-black' : 'border-none bg-brand-gray text-white'}`}><span className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs ${active ? 'bg-black text-white' : 'bg-white/10 text-white/40'}`}>{number}</span><span>{text}</span></motion.div> }
function SocialButton({ icon, label }: SocialButtonProps) { return <button type="button" className="flex h-12 items-center justify-center gap-3 rounded-xl border border-white/10 bg-black text-sm font-medium text-white transition hover:bg-white/5 active:scale-[0.98]">{icon}{label}</button> }
function InputGroup({ label, placeholder, type = 'text' }: InputGroupProps) { return <label className="block text-sm font-medium text-white">{label}<input required type={type} className="mt-2 h-11 w-full rounded-xl border-none bg-brand-gray px-4 text-white outline-none placeholder:text-white/20 focus:ring-2 focus:ring-white/20" placeholder={placeholder} /></label> }

// 设置面板。
//
// 沿用 Glass 风格：半透明面板 + 1px 半透明白边 + 12px 圆角。
// 三项：开机自启、更换服务器、退出登录。
function SettingsModal({
  autostart,
  autostartSupported,
  changingServer,
  confirmingChange,
  error,
  notice,
  warning,
  versionChecking,
  versionCheckError,
  versionUpToDate,
  onManualVersionCheck,
  onToggleAutostart,
  onChangeServer,
  onCancelChange,
  onLogout,
  onClose,
}: {
  autostart: boolean
  autostartSupported: boolean
  changingServer: boolean
  confirmingChange: boolean
  error: string
  notice: string
  warning: boolean
  versionChecking: boolean
  versionCheckError: string
  versionUpToDate: boolean
  onManualVersionCheck: () => void
  onToggleAutostart: () => void
  onChangeServer: () => void
  onCancelChange: () => void
  onLogout: () => void
  onClose: () => void
}) {
  const reducedMotion = useReducedMotion()
  const checkButtonState = versionCheckButtonState({ checking: versionChecking, error: versionCheckError, upToDate: versionUpToDate })

  return (
    <div className="absolute inset-0 z-40 flex items-center justify-center bg-black/60 p-4 backdrop-blur-sm" onClick={changingServer ? undefined : onClose}>
      <motion.div
        initial={{ opacity: 0, scale: 0.97, y: 6 }}
        animate={{ opacity: 1, scale: 1, y: 0 }}
        transition={{ duration: 0.18 }}
        role="dialog"
        aria-modal="true"
        aria-label="设置"
        onClick={(event) => event.stopPropagation()}
        className="glass-panel w-full max-w-sm space-y-4 p-5"
      >
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold tracking-tight">设置</h2>
          <button
            type="button"
            aria-label="关闭设置"
            onClick={onClose}
            disabled={changingServer}
            className="flex h-8 w-8 items-center justify-center rounded-lg text-white/60 transition-colors hover:bg-white/10 hover:text-white disabled:opacity-40"
          >
            <X size={15} strokeWidth={1.8} />
          </button>
        </div>

        <div className="space-y-2 border-b border-white/10 pb-4">
          <div className="flex items-center justify-between gap-3"><div><p className="text-sm text-white/90">客户端版本</p><p className="mt-0.5 text-[11px] text-white/45">手动检查会忽略已跳过版本</p></div><button type="button" onClick={onManualVersionCheck} disabled={versionChecking} className="flex w-[8.5rem] shrink-0 items-center justify-center rounded-lg border border-white/15 px-3 py-2 text-xs transition-colors hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40 active:scale-95 disabled:cursor-wait disabled:opacity-50 disabled:hover:bg-transparent motion-reduce:active:scale-100">
            <AnimatePresence mode="wait" initial={false}>
              <motion.span
                key={checkButtonState}
                role={checkButtonState === 'up-to-date' ? 'status' : undefined}
                aria-live={checkButtonState === 'up-to-date' ? 'polite' : undefined}
                initial={reducedMotion ? false : { opacity: 0, y: 4 }}
                animate={{ opacity: 1, y: 0 }}
                exit={reducedMotion ? undefined : { opacity: 0, y: -4 }}
                transition={{ duration: reducedMotion ? 0 : 0.14 }}
                className={`inline-flex items-center justify-center whitespace-nowrap ${checkButtonState === 'up-to-date' ? 'text-emerald-300' : ''}`}
              >
                {checkButtonState === 'checking' ? <><Loader2 size={13} aria-hidden="true" className="mr-1 animate-spin motion-reduce:animate-none" />检查中…</> : checkButtonState === 'up-to-date' ? <><CheckCircle2 size={14} aria-hidden="true" className="mr-1" />无更新</> : checkButtonState === 'error' ? '重试检查' : '检查更新'}
              </motion.span>
            </AnimatePresence>
          </button></div>
          {versionCheckError ? <p role="alert" className="text-xs text-amber-200">{versionCheckError}</p> : null}
        </div>

        {/* 开机自动启动 */}
        <div className="flex items-center justify-between gap-3 border-b border-white/10 pb-4">
          <div className="min-w-0">
            <p className="text-sm text-white/90">开机自动启动</p>
            <p className="mt-0.5 text-[11px] text-white/45">
              {autostartSupported ? '启动后自动登录并连接隧道' : '当前系统不支持'}
            </p>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={autostart}
            aria-label="开机自动启动"
            disabled={!autostartSupported || changingServer}
            onClick={onToggleAutostart}
            className={`relative h-6 w-11 shrink-0 rounded-full transition-colors disabled:opacity-40 ${autostart ? 'bg-emerald-300/80' : 'bg-white/15'}`}
          >
            <span className={`absolute top-0.5 h-5 w-5 rounded-full bg-white transition-all ${autostart ? 'left-[22px]' : 'left-0.5'}`} />
          </button>
        </div>

        {/* 更换服务器 */}
        <div className="space-y-2 pb-1">
          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="text-sm text-white/90">更换服务器</p>
              <p className="mt-0.5 text-[11px] text-white/45">换到另一台可用节点，每小时只能更换一次</p>
            </div>
            <button
              type="button"
              onClick={onChangeServer}
              disabled={changingServer}
              className="flex h-9 shrink-0 items-center gap-1.5 rounded-lg border border-white/15 bg-white/[0.06] px-3 text-xs font-medium text-white transition-colors hover:border-white/30 hover:bg-white/10 disabled:cursor-wait disabled:opacity-50"
            >
              {changingServer ? <Loader2 size={13} className="animate-spin" /> : <ServerCog size={13} strokeWidth={1.8} />}
              {changingServer ? '更换中…' : confirmingChange ? '确认更换' : '更换'}
            </button>
          </div>
          {confirmingChange && (
            <div className="flex items-center justify-between gap-2 rounded-lg bg-amber-300/10 px-2.5 py-1.5">
              <p className="text-[11px] text-amber-200">会断开当前隧道并重新连接</p>
              <button type="button" onClick={onCancelChange} className="shrink-0 text-[11px] text-white/60 underline-offset-2 hover:text-white hover:underline">
                取消
              </button>
            </div>
          )}
        </div>

        {error && (
          <p role="alert" className="rounded-lg bg-rose-400/10 px-3 py-2 text-xs text-rose-200">{error}</p>
        )}
        {notice && !error && (
          <p role="status" className={`rounded-lg px-3 py-2 text-xs ${warning ? 'bg-amber-300/10 text-amber-200' : 'bg-emerald-300/10 text-emerald-200'}`}>{notice}</p>
        )}

        {/* 退出登录 */}
        <button
          type="button"
          onClick={onLogout}
          disabled={changingServer}
          className="flex h-10 w-full items-center justify-center rounded-xl border border-rose-400/25 bg-rose-400/10 text-sm font-medium text-rose-200 transition-colors hover:bg-rose-400/15 disabled:opacity-40"
        >
          退出登录
        </button>
      </motion.div>
    </div>
  )
}
