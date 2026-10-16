import { useState, type ReactNode } from 'react'
import { motion } from 'motion/react'
import { Chrome, Circle, Eye, EyeOff, Github, Check } from 'lucide-react'
import './index.css'

export default function App() {
  const [showPassword, setShowPassword] = useState(false)
  const [submitted, setSubmitted] = useState(false)

  return (
    <main className="flex min-h-screen w-full bg-black p-2 text-white selection:bg-white/30 transition-all duration-500 lg:h-screen lg:overflow-hidden lg:p-4">
      <section className="relative hidden h-full w-[52%] flex-col items-center justify-end overflow-hidden rounded-3xl px-12 pb-32 shadow-2xl lg:flex">
        <video autoPlay muted loop playsInline className="absolute inset-0 h-full w-full object-cover" aria-hidden="true">
          <source src="https://d8j0ntlcm91z4.cloudfront.net/user_38xzZboKViGWJOttwIXH07lWA1P/hf_20260506_081238_406ed0e3-5d83-436e-a512-0bbff7ec5b95.mp4" type="video/mp4" />
        </video>
        <motion.div initial="hidden" animate="visible" variants={{ hidden: {}, visible: { transition: { staggerChildren: 0.15, delayChildren: 0.2 } } }} className="z-10 w-full max-w-xs space-y-8">
          <motion.div variants={reveal} className="flex items-center gap-2 text-xl font-semibold tracking-tight"><Circle size={20} fill="white" className="text-white" />Aurora</motion.div>
          <motion.div variants={reveal} className="space-y-3"><h1 className="whitespace-nowrap text-4xl font-medium tracking-tight">Join Aurora</h1><p className="px-4 text-sm leading-relaxed text-white/60">Follow these 3 quick phases to activate your space.</p></motion.div>
          <div className="space-y-4"><StepItem number={1} text="Register your identity" active /><StepItem number={2} text="Configure your studio" /><StepItem number={3} text="Finalize your profile" /></div>
        </motion.div>
      </section>
      <section className="flex flex-1 flex-col items-center justify-center overflow-y-auto px-4 py-12 sm:px-12 lg:overflow-hidden lg:px-16 lg:py-6 xl:px-24">
        <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ duration: 0.8, ease: 'easeOut' }} className="w-full max-w-xl space-y-8 sm:space-y-10 lg:space-y-6">
          <header className="space-y-2"><h2 className="text-3xl font-medium tracking-tight">Create New Profile</h2><p className="text-sm text-white/40">Input your basic details to begin the journey.</p></header>
          <div className="grid grid-cols-2 gap-4"><SocialButton label="Google" icon={<Chrome size={17} />} /><SocialButton label="Github" icon={<Github size={17} />} /></div>
          <div className="relative flex items-center"><div className="w-full border-t border-white/10" /><span className="absolute left-1/2 -translate-x-1/2 bg-black px-4 text-xs font-medium uppercase tracking-widest text-white/40">Or</span></div>
          <form className="space-y-5" onSubmit={(event) => { event.preventDefault(); setSubmitted(true) }}>
            <div className="grid grid-cols-1 gap-5 sm:grid-cols-2"><InputGroup label="First Name" placeholder="Maya" /><InputGroup label="Last Name" placeholder="Chen" /></div>
            <InputGroup label="Email" placeholder="maya@example.com" type="email" />
            <div><label className="block text-sm font-medium text-white">Password</label><span className="relative mt-2 block"><input required minLength={8} type={showPassword ? 'text' : 'password'} className="h-11 w-full rounded-xl border-none bg-brand-gray px-4 pr-12 text-white outline-none placeholder:text-white/20 focus:ring-2 focus:ring-white/20" placeholder="Create a password" /><button type="button" aria-label={showPassword ? 'Hide password' : 'Show password'} onClick={() => setShowPassword(!showPassword)} className="absolute right-4 top-1/2 -translate-y-1/2 text-white/40 hover:text-white">{showPassword ? <EyeOff size={18} /> : <Eye size={18} />}</button></span><span className="mt-2 block text-xs text-white/35">Requires at least 8 symbols.</span></div>
            <button type="submit" className="mt-4 flex h-14 w-full items-center justify-center rounded-xl bg-white font-semibold text-black transition hover:bg-white/90 active:scale-[0.98]">{submitted ? 'Profile Created' : 'Create Account'}</button>
          </form>
          <p className="text-center text-sm text-white/40">Member of the team? <button type="button" className="font-medium text-white underline decoration-white/25 underline-offset-4 hover:decoration-white">Log in</button></p>
        </motion.div>
      </section>
    </main>
  )
}

type StepItemProps = { number: number; text: string; active?: boolean }
type SocialButtonProps = { icon: ReactNode; label: string }
type InputGroupProps = { label: string; placeholder: string; type?: string }

const reveal = { hidden: { opacity: 0, y: 10 }, visible: { opacity: 1, y: 0, transition: { duration: 0.5 } } }

function StepItem({ number, text, active = false }: StepItemProps) {
  return <motion.div variants={reveal} className={`flex items-center gap-3 rounded-xl px-3 py-2 text-sm ${active ? 'border border-white bg-white text-black' : 'border-none bg-brand-gray text-white'}`}><span className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs ${active ? 'bg-black text-white' : 'bg-white/10 text-white/40'}`}>{active ? <Check size={14} strokeWidth={2.5} /> : number}</span><span>{text}</span></motion.div>
}

function SocialButton({ icon, label }: SocialButtonProps) {
  return <button type="button" className="flex h-12 items-center justify-center gap-3 rounded-xl border border-white/10 bg-black text-sm font-medium text-white transition hover:bg-white/5 active:scale-[0.98]">{icon}{label}</button>
}

function InputGroup({ label, placeholder, type = 'text' }: InputGroupProps) {
  return <label className="block text-sm font-medium text-white">{label}<input required type={type} className="mt-2 h-11 w-full rounded-xl border-none bg-brand-gray px-4 text-white outline-none placeholder:text-white/20 focus:ring-2 focus:ring-white/20" placeholder={placeholder} /></label>
}
