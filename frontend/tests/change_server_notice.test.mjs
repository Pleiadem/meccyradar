import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

test('换服成功提示用户复制最新链接，不声称只需重新连接', () => {
  const source = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')
  assert.match(source, /const changeServerSuccessNotice = '已更换服务器，请重新复制最新链接'/)
  assert.match(source, /setSettingsNotice\(result\.warning \? `\$\{changeServerSuccessNotice\}；\$\{result\.warning\}` : changeServerSuccessNotice\)/)
  assert.match(source, /setCopied\(false\)/)
  assert.match(source, /if \(!result\) throw new Error\('换服服务不可用，请稍后重试'\)/)
  assert.match(source, /warning \? 'bg-amber-300\/10 text-amber-200'/)
  assert.doesNotMatch(source, /更换成功，已重新连接/)
})