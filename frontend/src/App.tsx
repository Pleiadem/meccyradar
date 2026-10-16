import { useEffect, useRef, useState, type CSSProperties, type FormEvent, type ReactNode, type RefObject } from 'react'
import { motion } from 'motion/react'
import { Circle, Copy, Eye, EyeOff, Minus, Power, X } from 'lucide-react'
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

type TunnelStatus = { status: string; pingMs?: number; expiresIn?: number; message?: string }
type PortStatus = { open: boolean }
type LogEvent = { message: string; at: string }

type Backend = {
  GetInitState?: () => Promise<InitState>
  LoginByCard?: (cardKey: string) => Promise<AuthResult>
  StartTunnel?: () => Promise<void>
  StopTunnel?: () => Promise<void>
  CopyShareLink?: (password: string) => Promise<void>
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
  const [portOpen, setPortOpen] = useState(false)
  const [logs, setLogs] = useState<LogEvent[]>([])
  const logBox = useRef<HTMLDivElement>(null)

  useEffect(() => {
    void backend?.GetInitState?.().then((state) => { if (state) setInit(state); setHydrated(true) }).catch(() => setHydrated(true))
    const offStatus = runtime?.EventsOn?.('tunnel:status', (...data) => {
      const event = data[0] as TunnelStatus | undefined
      if (!event) return
      setInit((current) => ({ ...current, tunnelStatus: event.status, expiresIn: event.expiresIn ?? current.expiresIn }))
      if (typeof event.pingMs === 'number') setPingMs(event.pingMs)
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
    return () => { offStatus?.(); offPort?.(); offLog?.() }
  }, [])

  useEffect(() => {
    if (logBox.current) logBox.current.scrollTop = logBox.current.scrollHeight
  }, [logs])

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

  const copyLink = async () => {
    setBusy(true); setError(''); setCopied(false)
    try { await backend?.CopyShareLink?.(radarPassword); setCopied(true) } catch (cause) { setError(toMessage(cause)) } finally { setBusy(false) }
  }

  const logout = async () => {
    setBusy(true)
    try { await backend?.Logout?.(); setInit({ authenticated: false, tunnelStatus: 'idle' }); setCardKey('') } catch (cause) { setError(toMessage(cause)) } finally { setBusy(false) }
  }

  if (!hydrated) return <div className="flex min-h-screen items-center justify-center bg-black text-sm text-white/50">正在加载…</div>
  return <WindowShell authenticated={init.authenticated} onLogout={logout}>
    {init.authenticated ? (
      <ConsoleView init={init} pingMs={pingMs} portOpen={portOpen} logs={logs} logBox={logBox} radarPassword={radarPassword} showRadarPassword={showRadarPassword} busy={busy} copied={copied} error={error} onPasswordChange={setRadarPassword} onTogglePassword={() => setShowRadarPassword(!showRadarPassword)} onToggleTunnel={toggleTunnel} onCopy={copyLink} onLogout={logout} />
    ) : (
      <LoginView cardKey={cardKey} showCardKey={showCardKey} busy={busy} error={error} onCardKeyChange={setCardKey} onToggleCardKey={() => setShowCardKey(!showCardKey)} onSubmit={handleLogin} />
    )}
  </WindowShell>
}

function WindowShell({ authenticated, onLogout, children }: { authenticated: boolean; onLogout: () => void; children: ReactNode }) {
  return <div className="min-h-screen w-full bg-black text-white">
    <header className="flex h-10 items-center justify-between border border-white/10 bg-brand-gray px-4 text-xs" style={{ '--wails-draggable': 'drag' } as CSSProperties}>
      <span className="font-semibold tracking-wide">MeccyRadar <span className="ml-2 text-white/40">:13133</span></span>
      <div className="flex items-center gap-1" style={{ '--wails-draggable': 'no-drag' } as CSSProperties}>
        {authenticated && <button aria-label="退出登录" onClick={onLogout} className="rounded-lg px-2 py-1.5 text-white/50 hover:bg-white/10 hover:text-white">退出登录</button>}
        <button aria-label="最小化" onClick={() => runtime?.WindowMinimise?.()} className="rounded-lg p-2 text-white/50 hover:bg-white/10 hover:text-white"><Minus size={14} /></button>
        <button aria-label="关闭" onClick={() => runtime?.Quit?.()} className="rounded-lg p-2 text-white/50 hover:bg-red-400/20 hover:text-white"><X size={14} /></button>
      </div>
    </header>
    <div className="h-[calc(100vh-2.5rem)]">{children}</div>
  </div>
}

function LoginView({ cardKey, showCardKey, busy, error, onCardKeyChange, onToggleCardKey, onSubmit }: { cardKey: string; showCardKey: boolean; busy: boolean; error: string; onCardKeyChange: (value: string) => void; onToggleCardKey: () => void; onSubmit: (event: FormEvent) => void }) {
  return <main className="flex min-h-screen w-full bg-black p-2 text-white selection:bg-white/30 transition-all duration-500 lg:h-screen lg:overflow-hidden lg:p-4">
    <section className="relative hidden h-full w-[52%] flex-col items-center justify-end overflow-hidden rounded-3xl px-12 pb-32 shadow-2xl lg:flex"><video autoPlay muted loop playsInline className="absolute inset-0 h-full w-full object-cover" aria-hidden="true"><source src="https://d8j0ntlcm91z4.cloudfront.net/user_38xzZboKViGWJOttwIXH07lWA1P/hf_20260506_081238_406ed0e3-5d83-436e-a512-0bbff7ec5b95.mp4" type="video/mp4" /></video><motion.div initial="hidden" animate="visible" variants={{ hidden: {}, visible: { transition: { staggerChildren: 0.15, delayChildren: 0.2 } } }} className="z-10 w-full max-w-xs space-y-8"><motion.div variants={reveal} className="flex items-center gap-2 text-xl font-semibold tracking-tight"><Circle size={20} fill="white" className="text-white" />Aurora</motion.div><motion.div variants={reveal} className="space-y-3"><h1 className="whitespace-nowrap text-4xl font-medium tracking-tight">Join Aurora</h1><p className="px-4 text-sm leading-relaxed text-white/60">Follow these 3 quick phases to activate your space.</p></motion.div><div className="space-y-4"><StepItem number={1} text="Register your identity" active /><StepItem number={2} text="Configure your studio" /><StepItem number={3} text="Finalize your profile" /></div></motion.div></section>
    <section className="flex flex-1 flex-col items-center justify-center overflow-y-auto px-4 py-12 sm:px-12 lg:overflow-hidden lg:px-16 lg:py-6 xl:px-24"><motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ duration: 0.8, ease: 'easeOut' }} className="w-full max-w-xl space-y-8 sm:space-y-10 lg:space-y-6"><header className="space-y-2"><h2 className="text-3xl font-medium tracking-tight">MeccyRadar</h2><p className="text-sm text-white/40">输入卡密以连接你的专属雷达。</p></header><form className="space-y-5" onSubmit={onSubmit}><label className="block text-sm font-medium text-white">卡密<span className="relative mt-2 block"><input required value={cardKey} onChange={(event) => onCardKeyChange(event.target.value)} type={showCardKey ? 'text' : 'password'} className="h-12 w-full rounded-xl border-none bg-brand-gray px-4 pr-12 text-white outline-none placeholder:text-white/20 focus:ring-2 focus:ring-white/20" placeholder="输入卡密" /><button type="button" aria-label={showCardKey ? '隐藏卡密' : '显示卡密'} onClick={onToggleCardKey} className="absolute right-4 top-1/2 -translate-y-1/2 text-white/40 hover:text-white">{showCardKey ? <EyeOff size={18} /> : <Eye size={18} />}</button></span></label>{error && <p role="alert" className="rounded-xl border border-red-400/30 bg-red-400/10 px-4 py-3 text-sm text-red-200">{error}</p>}<button disabled={busy} type="submit" className="mt-4 flex h-14 w-full items-center justify-center rounded-xl bg-white font-semibold text-black transition hover:bg-white/90 active:scale-[0.98] disabled:cursor-wait disabled:opacity-60">{busy ? '连接中…' : '登录'}</button></form></motion.div></section>
  </main>
}

function ConsoleView({ init, pingMs, portOpen, logs, logBox, radarPassword, showRadarPassword, busy, copied, error, onPasswordChange, onTogglePassword, onToggleTunnel, onCopy, onLogout }: { init: InitState; pingMs?: number; portOpen: boolean; logs: LogEvent[]; logBox: RefObject<HTMLDivElement | null>; radarPassword: string; showRadarPassword: boolean; busy: boolean; copied: boolean; error: string; onPasswordChange: (value: string) => void; onTogglePassword: () => void; onToggleTunnel: () => void; onCopy: () => void; onLogout: () => void }) {
  const status = init.tunnelStatus
  const active = status === 'connected' || status === 'connecting'
  return <main className="min-h-screen w-full bg-black p-2 text-white lg:h-screen lg:overflow-hidden lg:p-4"><div className="grid h-[calc(100%-0rem)] grid-cols-1 gap-3 rounded-2xl border border-white/10 bg-black p-4 lg:grid-cols-[42%_58%]"><section className="flex flex-col justify-between rounded-2xl bg-brand-gray p-5"><div><p className="text-xs uppercase tracking-[0.2em] text-white/40">授权状态</p><p className="mt-3 text-3xl font-semibold">{formatRemaining(init.expiresIn ?? 0)}</p><p className="mt-2 text-xs text-white/50">到期：{init.expireAtStr ?? '—'}</p></div><div><div className="flex items-center justify-between text-sm"><span>节点延迟</span><span className="font-mono text-white/60">{pingMs === undefined ? '—' : `${pingMs} ms`}</span></div><div className="mt-3 flex items-center justify-between text-sm"><span>本地端口</span><span className={portOpen ? 'text-emerald-300' : 'text-white/40'}>{portOpen ? '已连接' : '未连接'}</span></div><button onClick={onToggleTunnel} disabled={busy} className={`mt-6 flex h-16 w-full items-center justify-center gap-3 rounded-2xl font-semibold transition active:scale-[0.98] ${active ? 'bg-white text-black hover:bg-white/90' : 'border border-white/15 bg-black text-white hover:bg-white/10'}`}><Power size={19} />{status === 'connecting' ? '连接中…' : active ? '停止隧道' : '启动隧道'}</button></div></section><section className="flex min-h-0 flex-col gap-3"><div className="rounded-2xl border border-white/10 bg-brand-gray p-4"><label className="text-xs uppercase tracking-[0.2em] text-white/40">雷达密码</label><div className="mt-3 flex gap-2"><div className="relative flex-1"><input value={radarPassword} onChange={(event) => onPasswordChange(event.target.value)} type={showRadarPassword ? 'text' : 'password'} className="h-11 w-full rounded-xl border-none bg-black px-4 pr-11 text-white outline-none placeholder:text-white/20 focus:ring-2 focus:ring-white/20" placeholder="可留空" /><button type="button" onClick={onTogglePassword} className="absolute right-3 top-1/2 -translate-y-1/2 text-white/40 hover:text-white" aria-label={showRadarPassword ? '隐藏密码' : '显示密码'}>{showRadarPassword ? <EyeOff size={17} /> : <Eye size={17} />}</button></div><button onClick={onCopy} disabled={busy} className="flex h-11 items-center gap-2 rounded-xl bg-white px-4 text-sm font-semibold text-black hover:bg-white/90"><Copy size={16} />{copied ? '已复制' : '复制链接'}</button></div>{error && <p role="alert" className="mt-3 text-xs text-red-200">{error}</p>}</div><div ref={logBox} className="min-h-0 flex-1 overflow-y-auto rounded-2xl border border-white/10 bg-[#090909] p-4 font-mono text-xs text-white/55" aria-live="polite">{logs.length === 0 ? <p className="text-white/25">等待隧道日志…</p> : logs.map((log, index) => <p key={`${log.at}-${index}`}><span className="mr-2 text-white/25">{log.at}</span>{log.message}</p>)}</div></section></div></main>
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
