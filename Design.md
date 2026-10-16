# Aurora Sign Up Design System

## Design read

深色、沉浸、克制的注册界面。左侧视频负责情绪和品牌记忆，右侧表单负责清晰、快速、低摩擦地完成注册。

## Tokens

- Surface: `#000000`
- Brand gray: `#1A1A1A`
- Primary text: `#FFFFFF`
- Muted text: `text-white/40` 到 `text-white/60`
- Divider: `border-white/10`
- Font: Inter, 300/400/500/600/700
- Radius: `rounded-xl` for controls, `rounded-3xl` for the hero frame
- Accent: white only. No gradient, tint, or extra accent color.

## Layout

- Root: full-width black canvas with `p-2`, expanding to `lg:p-4`.
- Hero: hidden below `lg`, exactly `w-[52%]` above `lg`.
- Form: `flex-1`, centered with responsive horizontal padding.
- Hero frame: rounded 3xl, clipped video, no overlay.
- Form width: `max-w-xl`.

## Components

### StepItem

Props:

```ts
type StepItemProps = {
  number: number
  text: string
  active?: boolean
}
```

States:

- Active row: `bg-white text-black border border-white`.
- Active number: `bg-black text-white`.
- Inactive row: `bg-brand-gray text-white border-none`.
- Inactive number: `bg-white/10 text-white/40`.

### SocialButton

Props:

```ts
type SocialButtonProps = {
  icon: ReactNode
  label: string
}
```

Style:

```text
bg-black border border-white/10 rounded-xl hover:bg-white/5
```

### InputGroup

Props:

```ts
type InputGroupProps = {
  label: string
  placeholder: string
  type?: string
}
```

Label:

```text
text-sm font-medium text-white
```

Input:

```text
bg-brand-gray border-none rounded-xl h-11 px-4 text-white
placeholder:text-white/20 focus:ring-2 focus:ring-white/20
```

## Motion

- Hero content uses `motion.div` with `staggerChildren: 0.15` and `delayChildren: 0.2`.
- Each hero child fades from `opacity: 0, y: 10` to `opacity: 1, y: 0` over `0.5s`.
- Form content fades in over `0.8s` with `easeOut`.
- All interactive controls use short hover and active feedback.
- Reduced-motion users receive near-instant CSS transitions.

## Content rules

- Brand name: Aurora.
- Heading: Join Aurora.
- Form title: Create New Profile.
- Social options: Google and Github.
- Password helper: Requires at least 8 symbols.
- Primary action: Create Account.
- Footer action: Member of the team? Log in.

## Do not change

- The exact CloudFront video source in `App.tsx`.
- The no-overlay requirement on the video.
- The two-column breakpoint behavior.
- `export default function App()` as the primary App export.
- The component states and class contracts above without an explicit design decision.
