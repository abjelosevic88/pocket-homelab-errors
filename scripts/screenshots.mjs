// README screenshots and a browser smoke test, against a demo instance
// (DEMO_DATA=true makes up a month of errors from a shop website, a Go API,
// a Python import and two Pocket apps' container logs):
//
//   DATA_DIR=/tmp/err-demo DEMO_DATA=true ADMIN_EMAIL=demo@example.com ADMIN_PASSWORD=demodemo1 ADMIN_NAME=Amar PORT=18256 ./pocket-errors
//   CDP=http://127.0.0.1:9356 BASE=http://127.0.0.1:18256 node scripts/screenshots.mjs   # any Chrome with remote debugging
//   SHOTS=0 … node scripts/screenshots.mjs                                                # checks only
// playwright-core comes from web/node_modules, or set PW_FROM to a package.json next to an install of it.
import { createRequire } from 'node:module'
import assert from 'node:assert/strict'

const require = createRequire(process.env.PW_FROM || new URL('../web/package.json', import.meta.url))
let pw
try { pw = require('playwright') } catch { pw = require('playwright-core') }
const { chromium } = pw
const base = process.env.BASE || 'http://127.0.0.1:18256'
const shots = process.env.SHOTS !== '0'
const only = process.env.ONLY || ''
const dir = process.env.OUT || new URL('../docs/screenshots/', import.meta.url).pathname
const browser = process.env.CDP ? await chromium.connectOverCDP(process.env.CDP) : await chromium.launch()
const errors = []

async function session({ phone = false, dark = false } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: phone ? 3 : 2, isMobile: phone, hasTouch: phone, colorScheme: dark ? 'dark' : 'light',
    userAgent: phone ? 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1' : undefined,
  })
  const page = await ctx.newPage()
  page.on('pageerror', e => errors.push(`pageerror: ${e}`))
  page.on('console', m => { if (m.type() === 'error' && !/Failed to load resource|Content Security Policy|pocket-ask/.test(m.text())) errors.push(m.text()) })
  await page.goto(base + '/login')
  const res = await page.evaluate(() => fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'demo@example.com', password: 'demodemo1' }) }).then(r => r.status))
  assert.equal(res, 200, 'demo sign-in')
  return { ctx, page }
}
async function go(page, path, wait = 1200) {
  await page.goto(base + path, { waitUntil: 'networkidle' })
  // headless Chrome freezes CSS fade-ins: show everything at once
  await page.addStyleTag({ content: '*, *::before, *::after { transition: none !important; animation: none !important; }' }).catch(() => {})
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(wait)
}
async function shot(page, name) { if (shots && (!only || only.split(',').includes(name))) await page.screenshot({ path: dir + name + '.png' }) }
const api = async (page, path) => page.evaluate(p => fetch('/api/v1' + p).then(r => r.json()), path)

// ---- desktop ----
{
  const { ctx, page } = await session()
  await go(page, '/')
  assert.ok(await page.locator('.issue-row').count() >= 6, 'open issues are listed')
  assert.ok(await page.locator('.overview .bars-svg').count() === 1, 'the last 24 hours chart')
  await shot(page, 'desktop-issues')
  const issues = (await api(page, '/issues?sort=times')).issues
  const cart = issues.find(i => i.title.includes("reading 'price'")) || issues[0]
  await go(page, `/issues/${cart.id}`, 1500)
  assert.ok(await page.locator('.frame').count() >= 2, 'the stack is shown')
  assert.ok((await page.content()).includes('What happened'), 'the AI explanation is shown')
  await shot(page, 'desktop-issue')
  const notes = issues.find(i => i.project_name === 'Pocket Notes' && i.type === 'panic')
  if (notes) {
    await go(page, `/issues/${notes.id}`, 1200)
    assert.ok(await page.locator('.log-lines').count() === 1, 'container log lines')
    await shot(page, 'desktop-panic')
  }
  await go(page, '/projects', 1200)
  assert.ok(await page.locator('.project-card').count() === 5, 'five demo projects')
  await shot(page, 'desktop-projects')
  const projects = (await api(page, '/projects')).projects
  const shop = projects.find(p => p.name === 'Shop website')
  await go(page, `/projects/${shop.id}`, 1000)
  assert.ok((await page.content()).includes('data-dsn'), 'the setup line')
  await shot(page, 'desktop-setup')
  await go(page, '/settings/box', 1000)
  await shot(page, 'desktop-box')
  await ctx.close()
}

// ---- dark ----
{
  const { ctx, page } = await session({ dark: true })
  await go(page, '/')
  await shot(page, 'desktop-dark')
  await ctx.close()
}

// ---- phone ----
{
  const { ctx, page } = await session({ phone: true })
  await go(page, '/')
  await shot(page, 'phone-issues')
  const issues = (await api(page, '/issues?sort=times')).issues
  await go(page, `/issues/${issues[0].id}`, 1200)
  await shot(page, 'phone-issue')
  await page.locator('.frames').first().scrollIntoViewIfNeeded()
  await page.waitForTimeout(300)
  await shot(page, 'phone-stack')
  await go(page, '/projects')
  await shot(page, 'phone-projects')
  await ctx.close()
}

await browser.close().catch(() => {})
if (errors.length) { console.error(errors.join('\n')); process.exit(1) }
console.log('ok')
