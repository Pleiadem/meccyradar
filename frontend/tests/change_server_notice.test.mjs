import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')

test('server changes show a non-modal green notice below the radar password and copy controls', () => {
  assert.match(source, /const changeServerSuccessNotice = '已更换服务器，请重新复制最新链接'/)
  assert.match(source, /linkNotice=\{Boolean\(linkPrompt && linkPrompt\.version === init\.linkVersion\)\}/)
  assert.match(source, /\{linkNotice && <p role="status" className="mt-2 rounded-lg border border-emerald-300\/20 bg-emerald-300\/10[^>]*>\{changeServerSuccessNotice\}<\/p>\}/)
  assert.doesNotMatch(source, /LinkDeliveryModal|aria-modal="true"[^\n]*服务器连接配置已更新/)
  assert.doesNotMatch(source, /服务器连接配置已更新（版本/)
  assert.match(source, /current\.authenticated && current\.copyPending && current\.linkVersion/)
  assert.match(source, /if \(next\.copyPending && next\.linkVersion\) setLinkPrompt\(\{ version: next\.linkVersion \}\)/)
})

test('existing copy button immediately hides the notice and keeps the captured version for the async attempt', () => {
  assert.match(source, /const promptedVersion = linkPrompt\?\.version\s+if \(promptedVersion\) setLinkPrompt\(null\)/)
  assert.match(source, /const version = promptedVersion \?\? init\.linkVersion\s+if \(version && backend\?\.CopyLinkForDelivery\) \{\s+const result = await performCopyAttempt\(version\)/)
  assert.match(source, /if \(result\.attempted && result\.copied\) \{ setCopied\(true\); setCopyFailure\(null\) \}\s+else if \(result\.attempted && result\.link\) setCopyFailure/)
  assert.doesNotMatch(source, /setInit\(\(current\) => \(\{ \.\.\.current, copyPending: false \}\)\)/)
})

test('ordinary copy remains available and clipboard failures retain the manual-selection fallback', () => {
  assert.match(source, /onCopy=\{copyLink\}/)
  assert.match(source, /ShareLinkCopyFailure link=\{copyFailure\.link\}/)
  assert.match(source, /onFocus=\{\(event\) => event\.currentTarget\.select\(\)\}/)
  assert.doesNotMatch(source, /console\.(log|error).*link|emitLog\([^\n]*result\.link/)
})

test('login copyPending, accepted acknowledgements, and version changes use lease/version matching', () => {
  assert.match(source, /if \(current\.copyPending && current\.linkVersion\) setLinkPrompt\(\{ version: current\.linkVersion \}\)/)
  assert.match(source, /if \(matchesAck\) setLinkPrompt\(\(prompt\) => prompt\?\.version === event\.linkVersion \? null : prompt\)/)
  assert.match(source, /linkPrompt\.version === init\.linkVersion/)
  assert.match(source, /if \(version && backend\?\.CopyLinkForDelivery\)/)
})

test('login card outer gutters are controlled by one container at desktop breakpoints', () => {
  const login = source.slice(source.indexOf('function LoginView'), source.indexOf('function ConsoleView'))
  assert.match(login, /overflow-hidden px-3 pt-12 pb-12[^\"]*min-\[700px\]:px-8/)
  assert.match(login, /items-center justify-center px-4 py-3 min-\[700px\]:items-end min-\[700px\]:px-0/)
  assert.doesNotMatch(login, /sm:p-4|sm:px-8/)
  assert.match(login, /relative hidden h-full[^\"]*min-\[700px\]:flex/)
})
