import { useEffect, useRef, useState, type CSSProperties, type FormEvent, type ReactNode, type RefObject } from 'react'
import { motion } from 'motion/react'
import { Copy, Eye, EyeOff, Minus, Power, Radar, RefreshCw, X } from 'lucide-react'
import './index.css'

type InitState = {
  authenticated: boolean
  appName?: string
  expireAtStr?: string
  expireAtUnix?: number
  expiresIn?: number
  tunnelStatus: string
}

type AuthResult = {
  appName: string
  expireAtStr: string
  expireAtUnix: number
  tunnelStatus: string
}

type TunnelStatus = { status: string; pingMs?: number; expiresIn?: number; message?: string; timeout?: boolean; done?: boolean }
type PortStatus = { open: boolean }
type LogEvent = { message: string; at: string }

type Backend = {
  GetInitState?: () => Promise<InitState>
  LoginByCard?: (cardKey: string) => Promise<AuthResult>
  StartTunnel?: () => Promise<void>
  StopTunnel?: () => Promise<void>
  CopyShareLink?: (password: string) => Promise<void>
  RadarPassword?: () => Promise<string>
  SaveRadarPassword?: (password: string) => Promise<void>
  RemeasurePing?: () => Promise<void>
  CloseWindow?: () => void
  Logout?: () => Promise<void>
}

type Runtime = {
  EventsOn?: (eventName: string, callback: (...data: unknown[]) => void) => () => void
  WindowMinimise?: () => void
  Quit?: () => void
}

const backend = (window as Window & { go?: { main?: { App?: Backend } } }).go?.main?.App
const runtime = (window as Window & { runtime?: Runtime }).runtime

export default function App() {
  const [init, setInit] = useState<InitState>({ authenticated: false, tunnelStatus: 'idle' })
  const [hydrated, setHydrated] = useState(false)
  const [cardKey, setCardKey] = useState('')
  const [showCardKey, setShowCardKey] = useState(false)
  const [radarPassword, setRadarPassword] = useState('')
  const [showRadarPassword, setShowRadarPassword] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const [pingMs, setPingMs] = useState<number>()
  const [measuring, setMeasuring] = useState(false)
  const measureTimer = useRef<number | undefined>(undefined)
  const [portOpen, setPortOpen] = useState(false)
  const [logs, setLogs] = useState<LogEvent[]>([])
  const logBox = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (backend?.GetInitState) {
      void backend.GetInitState().then((state) => { if (state) setInit(state); setHydrated(true) }).catch(() => setHydrated(true))
    } else {
      setHydrated(true)
    }
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
      if (event.message) setError(event.message)
    })
    const offPort = runtime?.EventsOn?.('port:status', (...data) => {
      const event = data[0] as PortStatus | undefined
      if (event) setPortOpen(event.open)
    })
    const offLog = runtime?.EventsOn?.('log:append', (...data) => {
      const event = data[0] as LogEvent | undefined
      if (event) setLogs((current) => [...current.slice(-99), event])
    })
    return () => {
      window.clearTimeout(measureTimer.current)
      offStatus?.()
      offPort?.()
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
      setInit({ authenticated: true, appName: result.appName, expireAtStr: result.expireAtStr, expireAtUnix: result.expireAtUnix, tunnelStatus: result.tunnelStatus })
    } catch (cause) {
      setError(toMessage(cause))
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
    } catch (cause) { setError(toMessage(cause)) } finally { setBusy(false) }
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

  const copyLink = async () => {
    setBusy(true); setError(''); setCopied(false)
    try { await backend?.CopyShareLink?.(radarPassword); setCopied(true) } catch (cause) { setError(toMessage(cause)) } finally { setBusy(false) }
  }

  const logout = async () => {
    setBusy(true)
    try { await backend?.Logout?.(); setInit({ authenticated: false, tunnelStatus: 'idle' }); setError('') } catch (cause) { setError(toMessage(cause)) } finally { setBusy(false) }
  }

  if (!hydrated) return <div className="flex min-h-screen items-center justify-center bg-black text-sm text-white/50">正在加载…</div>
  return <WindowShell authenticated={init.authenticated} onLogout={logout}>
    {init.authenticated ? (
      <ConsoleView init={init} pingMs={pingMs} measuring={measuring} onRemeasure={remeasure} portOpen={portOpen} logs={logs} logBox={logBox} radarPassword={radarPassword} showRadarPassword={showRadarPassword} busy={busy} copied={copied} error={error} onPasswordChange={(value) => { setRadarPassword(value); void backend?.SaveRadarPassword?.(value) }} onTogglePassword={() => setShowRadarPassword(!showRadarPassword)} onToggleTunnel={toggleTunnel} onCopy={copyLink} onLogout={logout} />
    ) : (
      <LoginView cardKey={cardKey} showCardKey={showCardKey} busy={busy} error={error} onCardKeyChange={setCardKey} onToggleCardKey={() => setShowCardKey(!showCardKey)} onSubmit={handleLogin} />
    )}
  </WindowShell>
}

function WindowShell({ authenticated, onLogout, children }: { authenticated: boolean; onLogout: () => void; children: ReactNode }) {
  return <div className="relative h-[100dvh] w-full overflow-hidden bg-black text-white">
    <div className="absolute left-0 top-0 z-20 h-11 w-[calc(100%-112px)]" style={{ '--wails-draggable': 'drag' } as CSSProperties}>
      {authenticated && <span className="absolute left-4 top-1/2 flex -translate-y-1/2 items-center gap-2 text-xs font-semibold tracking-wide text-white/85"><Radar size={15} strokeWidth={1.8} />MeccyRadar</span>}
    </div>
    <div className="absolute right-2 top-1.5 z-30 flex items-center gap-1" style={{ '--wails-draggable': 'no-drag' } as CSSProperties}>
      {authenticated && <button aria-label="退出登录" onClick={onLogout} className="mr-1 rounded-lg px-2.5 py-2 text-xs text-white/55 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40">退出登录</button>}
      <button aria-label="最小化" title="最小化" onClick={() => runtime?.WindowMinimise?.()} className="flex h-9 w-9 items-center justify-center rounded-lg text-white/75 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40"><Minus size={15} strokeWidth={1.8} /></button>
      <button aria-label="关闭窗口" title="关闭窗口" onClick={() => backend?.CloseWindow?.()} className="flex h-9 w-9 items-center justify-center rounded-lg text-white/75 transition-colors hover:bg-rose-500/80 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-rose-300/70"><X size={15} strokeWidth={1.8} /></button>
    </div>
    <div className="h-full w-full">{children}</div>
  </div>
}

function LoginView({ cardKey, showCardKey, busy, error, onCardKeyChange, onToggleCardKey, onSubmit }: { cardKey: string; showCardKey: boolean; busy: boolean; error: string; onCardKeyChange: (value: string) => void; onToggleCardKey: () => void; onSubmit: (event: FormEvent) => void }) {
  return <main className="grid h-full min-h-0 w-full grid-cols-1 overflow-hidden bg-black p-3 pt-12 pb-12 text-white selection:bg-white/30 transition-colors duration-300 sm:p-4 sm:pt-12 sm:pb-12 min-[700px]:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
    <section className="relative hidden h-full min-h-0 min-w-0 flex-col justify-center overflow-hidden rounded-2xl border border-white/10 bg-[#111] px-7 shadow-xl min-[700px]:flex"><div aria-hidden="true" className="absolute inset-0 bg-[radial-gradient(ellipse_at_28%_35%,rgba(72,98,117,0.38),transparent_52%),linear-gradient(145deg,#1c2228_0%,#090a0b_70%)]" /><motion.div initial="hidden" animate="visible" variants={{ hidden: {}, visible: { transition: { staggerChildren: 0.12, delayChildren: 0.08 } } }} className="relative z-10 w-full max-w-xs space-y-7"><motion.div variants={reveal} className="flex items-center gap-3 text-lg font-semibold tracking-tight"><span className="flex h-10 w-10 items-center justify-center rounded-xl border border-white/10 bg-white/[0.06]"><Radar size={20} strokeWidth={1.7} /></span><span>MeccyRadar</span></motion.div><motion.div variants={reveal} className="space-y-3"><h1 className="text-3xl font-medium tracking-tight">欢迎回来</h1><p className="text-sm leading-relaxed text-white/70">输入卡密，连接你的专属雷达。</p></motion.div><motion.div variants={reveal} className="flex items-center gap-3 border-t border-white/10 pt-5 text-xs text-white/60"><span className="h-2 w-2 rounded-full bg-emerald-300 shadow-[0_0_12px_rgba(110,231,183,0.35)]" />安全授权，稳定连接</motion.div></motion.div></section>
    <section className="flex min-h-0 min-w-0 flex-col items-center justify-center px-4 py-3 sm:px-8"><motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.4, ease: 'easeOut' }} className="w-full max-w-sm space-y-6"><header className="space-y-2"><p className="text-[11px] font-medium tracking-[0.18em] text-white/55">MECCY RADAR</p><h2 className="text-2xl font-medium tracking-tight">卡密登录</h2><p className="text-[13px] text-white/65">授权后管理隧道连接和雷达分享。</p></header><form className="space-y-4" onSubmit={onSubmit}><label className="block text-[13px] font-medium text-white/90">卡密<span className="relative mt-2 block"><input required value={cardKey} onChange={(event) => onCardKeyChange(event.target.value)} type={showCardKey ? 'text' : 'password'} className="h-11 w-full rounded-xl border border-white/15 bg-[#171717] px-4 pr-12 text-sm text-white outline-none transition-colors placeholder:text-white/45 hover:border-white/25 focus:border-white/40 focus:ring-2 focus:ring-white/15" placeholder="请输入卡密" /><button type="button" aria-label={showCardKey ? '隐藏卡密' : '显示卡密'} onClick={onToggleCardKey} className="absolute right-1 top-1/2 flex h-9 w-9 -translate-y-1/2 items-center justify-center rounded-lg text-white/65 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40">{showCardKey ? <EyeOff size={16} /> : <Eye size={16} />}</button></span></label>{error && <motion.p initial={{ opacity: 0, y: -4 }} animate={{ opacity: 1, y: 0 }} role="alert" className="rounded-xl border border-rose-400/25 bg-rose-400/10 px-4 py-3 text-xs text-rose-200">{error}</motion.p>}<button disabled={busy} type="submit" className="group flex h-11 w-full items-center justify-center gap-2 rounded-xl bg-white text-sm font-semibold text-black shadow-[0_8px_24px_rgba(255,255,255,0.07)] transition duration-200 hover:-translate-y-0.5 hover:bg-white/90 active:translate-y-0 active:scale-[0.99] disabled:cursor-wait disabled:opacity-60">{busy ? <><span className="h-4 w-4 animate-spin rounded-full border-2 border-black/25 border-t-black" />正在验证</> : '登录并连接'}</button></form><p className="text-[11px] leading-relaxed text-white/50">卡密用于识别授权状态，请勿分享给他人。</p></motion.div></section>
  </main>
}

function ConsoleView({ init, pingMs, measuring, onRemeasure, portOpen, logs, logBox, radarPassword, showRadarPassword, busy, copied, error, onPasswordChange, onTogglePassword, onToggleTunnel, onCopy }: { init: InitState; pingMs?: number; measuring: boolean; onRemeasure: () => void; portOpen: boolean; logs: LogEvent[]; logBox: RefObject<HTMLDivElement | null>; radarPassword: string; showRadarPassword: boolean; busy: boolean; copied: boolean; error: string; onPasswordChange: (value: string) => void; onTogglePassword: () => void; onToggleTunnel: () => void; onCopy: () => void; onLogout: () => void }) {
  const status = init.tunnelStatus
  const active = status === 'connected' || status === 'connecting'
  // 面板外发光随状态变化，让用户在余光里也能看出隧道状态。
  const glow = status === 'connected' ? 'status-glow-connected' : status === 'connecting' ? 'status-glow-connecting' : status === 'error' ? 'status-glow-error' : ''
  return <main className="h-full min-h-0 w-full overflow-hidden bg-black p-3 pt-12 text-white sm:p-4 sm:pt-12"><div className="grid h-full min-h-0 grid-cols-1 gap-3 min-[620px]:grid-cols-[minmax(0,0.88fr)_minmax(0,1.12fr)]"><motion.section initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.32 }} className={`status-glow ${glow} flex min-h-0 flex-col justify-between rounded-2xl border border-white/10 bg-[#141414] p-4 sm:p-5`}><div><div className="flex items-center justify-between"><p className="text-xs font-medium tracking-wide text-white/55">授权状态</p><span className={`rounded-full px-2.5 py-1 text-[11px] font-medium ${init.authenticated ? 'bg-emerald-300/10 text-emerald-200' : 'bg-white/10 text-white/50'}`}>{init.authenticated ? '已授权' : '未授权'}</span></div><p className="mt-5 text-2xl font-semibold tracking-tight sm:text-3xl">{formatRemaining(init.expiresIn ?? 0)}</p><p className="mt-2 text-xs text-white/55">到期时间：{init.expireAtStr ?? '—'}</p></div><div className="space-y-3"><button type="button" onClick={onRemeasure} disabled={measuring} title="点击重新测速" className="flex w-full items-center justify-between rounded-xl bg-white/[0.035] px-3 py-2.5 text-sm transition-colors hover:bg-white/[0.075] disabled:cursor-wait"><span className="text-white/70">节点延迟</span><span className="flex items-center gap-2 font-mono text-white/80">{measuring ? <><RefreshCw size={13} className="animate-spin" /><span className="text-white/60">测速中…</span></> : pingMs === undefined ? '点击测速' : `${pingMs} ms`}</span></button><div className="flex items-center justify-between rounded-xl bg-white/[0.035] px-3 py-2.5 text-sm"><span className="text-white/70">本地端口</span><span className={portOpen ? 'text-emerald-200' : 'text-white/45'}>{portOpen ? '已启动' : '未启动'}</span></div><button onClick={onToggleTunnel} disabled={busy} className={`flex h-12 w-full items-center justify-center gap-2.5 rounded-xl font-semibold transition duration-200 active:scale-[0.985] disabled:cursor-wait disabled:opacity-60 ${active ? 'bg-white text-black hover:bg-white/90' : 'border border-white/15 bg-white/[0.045] text-white hover:border-white/25 hover:bg-white/10'}`}><Power size={17} strokeWidth={1.8} />{status === 'connecting' ? '连接中…' : active ? '停止隧道' : '启动隧道'}</button></div></motion.section><section className="flex min-h-0 min-w-0 flex-col gap-3"><motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.36, delay: 0.05 }} className="shrink-0 rounded-2xl border border-white/10 bg-[#141414] p-4"><div className="flex items-center justify-between gap-3"><label htmlFor="radar-password" className="text-sm font-medium text-white/90">雷达密码</label><span className="text-[11px] text-white/40">必填</span></div><div className="mt-3 flex min-w-0 gap-2"><div className="relative min-w-0 flex-1"><input id="radar-password" value={radarPassword} onChange={(event) => onPasswordChange(event.target.value)} type={showRadarPassword ? 'text' : 'password'} className="h-11 w-full rounded-xl border border-white/10 bg-black/70 px-3 pr-10 text-sm text-white outline-none transition-colors placeholder:text-white/35 hover:border-white/20 focus:border-white/35 focus:ring-2 focus:ring-white/15" placeholder="输入雷达密码（链接必需）" /><button type="button" onClick={onTogglePassword} className="absolute right-1 top-1/2 flex h-9 w-9 -translate-y-1/2 items-center justify-center rounded-lg text-white/50 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/40" aria-label={showRadarPassword ? '隐藏密码' : '显示密码'}>{showRadarPassword ? <EyeOff size={16} /> : <Eye size={16} />}</button></div><button onClick={onCopy} disabled={busy || !radarPassword.trim()} title={!radarPassword.trim() ? '请先填写雷达密码' : undefined} className="flex h-11 shrink-0 items-center justify-center gap-2 rounded-xl bg-white px-3 text-sm font-semibold text-black transition duration-200 hover:-translate-y-0.5 hover:bg-white/90 active:translate-y-0 active:scale-[0.98] disabled:cursor-wait disabled:opacity-60"><Copy size={15} />{copied ? '已复制' : '复制'}</button></div>{error && <motion.p initial={{ opacity: 0, y: -3 }} animate={{ opacity: 1, y: 0 }} role="alert" className="mt-2 rounded-lg bg-rose-400/10 px-3 py-2 text-xs text-rose-200">{error}</motion.p>}</motion.div><div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-2xl border border-white/10 bg-[#101010]"><div className="flex h-10 shrink-0 items-center justify-between border-b border-white/[0.07] px-4"><span className="text-xs font-medium text-white/65">运行日志</span><span className="text-[11px] text-white/35">实时</span></div><div ref={logBox} className="min-h-0 flex-1 overflow-y-auto px-4 py-3 font-mono text-[11px] leading-5 text-white/55" aria-live="polite">{logs.length === 0 ? <p className="text-white/35">等待隧道日志…</p> : logs.map((log, index) => <p key={`${log.at}-${index}`} className="break-words"><span className="mr-2 text-white/35">{log.at}</span>{log.message}</p>)}</div></div></section></div></main>
}

function formatRemaining(seconds: number) { if (seconds <= 0) return '已到期'; const days = Math.floor(seconds / 86400); const hours = Math.floor((seconds % 86400) / 3600); const minutes = Math.floor((seconds % 3600) / 60); return days > 0 ? `${days}天 ${hours}小时` : `${hours}小时 ${minutes}分钟` }
function toMessage(cause: unknown) { return cause instanceof Error ? cause.message : typeof cause === 'string' ? cause : '操作失败，请稍后重试' }

type StepItemProps = { number: number; text: string; active?: boolean }
type SocialButtonProps = { icon: ReactNode; label: string }
type InputGroupProps = { label: string; placeholder: string; type?: string }
const reveal = { hidden: { opacity: 0, y: 10 }, visible: { opacity: 1, y: 0, transition: { duration: 0.5 } } }
function StepItem({ number, text, active = false }: StepItemProps) { return <motion.div variants={reveal} className={`flex items-center gap-3 rounded-xl px-3 py-2 text-sm ${active ? 'border border-white bg-white text-black' : 'border-none bg-brand-gray text-white'}`}><span className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs ${active ? 'bg-black text-white' : 'bg-white/10 text-white/40'}`}>{number}</span><span>{text}</span></motion.div> }
function SocialButton({ icon, label }: SocialButtonProps) { return <button type="button" className="flex h-12 items-center justify-center gap-3 rounded-xl border border-white/10 bg-black text-sm font-medium text-white transition hover:bg-white/5 active:scale-[0.98]">{icon}{label}</button> }
function InputGroup({ label, placeholder, type = 'text' }: InputGroupProps) { return <label className="block text-sm font-medium text-white">{label}<input required type={type} className="mt-2 h-11 w-full rounded-xl border-none bg-brand-gray px-4 text-white outline-none placeholder:text-white/20 focus:ring-2 focus:ring-white/20" placeholder={placeholder} /></label> }
