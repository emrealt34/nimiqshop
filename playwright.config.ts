/**
 * End-to-end + responsive test suite (Playwright).
 *
 *   npm run test:e2e                 build like GitHub Pages, then run everything
 *   npm run test:e2e:quick           reuse dist/ (after npm run test:e2e:build)
 *   npx playwright test --project=phone               one device class
 *   npx playwright test --project=chromium crash      one file
 *   npm run test:e2e:report          open the HTML report
 *
 * Projects
 *   desktop / tablet / phone — responsive deep scan (responsive.spec.ts):
 *       every screen × language × every size of the class, every control
 *       pressed; screenshots → screenshots/<class>/…
 *   chromium                 — all functional specs (routing, links, crash,
 *       resilience, header, impact card, PNG, share dialog, identicons, i18n)
 *   firefox / webkit / mobile-safari — the @smoke subset on other engines
 */
import { defineConfig, devices } from '@playwright/test';
import { DEVICES, type DeviceClass } from './tests/e2e/support/data';

const PORT = Number(process.env.E2E_PORT || 4455);
const CI = !!process.env.CI;
const RESPONSIVE = /responsive\.spec\.ts/;
// The static server binds to loopback; the browsers under test only ever
// talk to this machine (reviewed: not debug residue).
const ORIGIN = `http://127.0.0.1:${PORT}`; // DevSkim: ignore DS162092 loopback-only e2e server by design

/** Budget of one responsive deep-scan test: a page load plus a full audit,
 *  screenshot and control sweep at every viewport size of the class. */
const scanBudget = (cls: DeviceClass) => 45_000 + DEVICES[cls].length * 40_000;

export default defineConfig({
  testDir: './tests/e2e',
  outputDir: './test-results',
  timeout: 30_000,
  expect: { timeout: 7_000 },
  fullyParallel: true,
  forbidOnly: CI,
  retries: CI ? 1 : 0,
  // GitHub's 4-vCPU runners: 3 browsers keep every core busy without starving
  // hydration (4+ caused load timeouts in local profiling).
  workers: CI ? 3 : 2,
  reporter: CI
    ? [['blob'], ['github'], ['list']]
    : [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
  use: {
    baseURL: ORIGIN,
    viewport: { width: 1280, height: 800 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    serviceWorkers: 'block',
    reducedMotion: 'reduce',
    timezoneId: 'Europe/Istanbul',
  },
  webServer: {
    command: 'node tests/e2e/support/serve.mjs',
    url: `${ORIGIN}/_health`,
    env: { HOST: new URL(ORIGIN).hostname, PORT: String(PORT), BASE: '/nimiqshop/' },
    reuseExistingServer: !CI,
    timeout: 30_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
  projects: [
    { name: 'desktop', testMatch: RESPONSIVE, metadata: { device: 'desktop' }, timeout: scanBudget('desktop'), use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } } },
    { name: 'tablet', testMatch: RESPONSIVE, metadata: { device: 'tablet' }, timeout: scanBudget('tablet'), use: { ...devices['Desktop Chrome'], viewport: { width: 768, height: 1024 }, hasTouch: true, deviceScaleFactor: 1 } },
    { name: 'phone', testMatch: RESPONSIVE, metadata: { device: 'phone' }, timeout: scanBudget('phone'), use: { ...devices['Pixel 7'], viewport: { width: 390, height: 844 }, deviceScaleFactor: 1 } },
    { name: 'chromium', testIgnore: RESPONSIVE, use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } } },
    { name: 'firefox', testIgnore: RESPONSIVE, grep: /@smoke/, use: { ...devices['Desktop Firefox'], viewport: { width: 1280, height: 800 } } },
    { name: 'webkit', testIgnore: RESPONSIVE, grep: /@smoke/, use: { ...devices['Desktop Safari'], viewport: { width: 1280, height: 800 } } },
    { name: 'mobile-safari', testIgnore: RESPONSIVE, grep: /@smoke/, use: { ...devices['iPhone 13'] } },
  ],
});
