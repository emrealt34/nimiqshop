#!/usr/bin/env node
/**
 * Builds screenshots/index.html — a browsable gallery of every screenshot the
 * responsive scan took, grouped Desktop / Tablet / Phone → screen → language —
 * and appends a summary table to the GitHub job summary.
 */
import { readdirSync, statSync, writeFileSync, appendFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

const ROOT = join(process.cwd(), 'screenshots');
if (!existsSync(ROOT)) { console.log('no screenshots/'); process.exit(0); }
const ls = (p) => readdirSync(p).filter((n) => !n.startsWith('.')).sort();
const isDir = (p) => statSync(p).isDirectory();
const TITLES = { desktop: '🖥️ Desktop (PC)', tablet: '📱 Tablet', phone: '📱 Phone' };
const order = ['desktop', 'tablet', 'phone'].filter((d) => existsSync(join(ROOT, d)));

let html = `<!doctype html><meta charset="utf-8"><title>nimiqshop · responsive screenshots</title>
<style>body{font:14px system-ui;margin:0;background:#f4f4f6;color:#1f2348}header{position:sticky;top:0;background:#1f2348;color:#fff;padding:12px 20px;z-index:2}
header a{color:#e9b213;margin-right:16px;font-weight:700}h2{margin:28px 20px 8px}h3{margin:18px 20px 6px;font-size:15px}details{margin:0 20px 8px;background:#fff;border-radius:10px;padding:8px 12px}
summary{cursor:pointer;font-weight:600}.grid{display:flex;flex-wrap:wrap;gap:10px;margin-top:8px}figure{margin:0;width:220px}figure img{width:100%;border:1px solid #ddd;border-radius:6px;background:#fff}
figcaption{font-size:11px;color:#666;word-break:break-all}</style>
<header><b>nimiqshop responsive scan</b> · ${new Date().toISOString().slice(0, 16).replace('T', ' ')} UTC · ${order.map((d) => `<a href="#${d}">${TITLES[d]}</a>`).join('')}</header>`;
const summary = ['| Device | Screens | Languages | Screenshots |', '|---|---|---|---|'];

for (const dev of order) {
  const dDir = join(ROOT, dev);
  let count = 0; const langs = new Set();
  html += `<h2 id="${dev}">${TITLES[dev]}</h2>`;
  for (const screen of ls(dDir).filter((s) => isDir(join(dDir, s)))) {
    html += `<h3>${screen}</h3>`;
    for (const lang of ls(join(dDir, screen))) {
      langs.add(lang);
      const shots = ls(join(dDir, screen, lang)).filter((f) => f.endsWith('.jpg'));
      count += shots.length;
      html += `<details><summary>${lang.toUpperCase()} · ${shots.length} shots</summary><div class="grid">${shots.map((f) => `<figure><a href="${dev}/${screen}/${lang}/${f}" target="_blank"><img loading="lazy" src="${dev}/${screen}/${lang}/${f}"></a><figcaption>${f.replace('.jpg', '')}</figcaption></figure>`).join('')}</div></details>`;
    }
  }
  summary.push(`| ${TITLES[dev]} | ${ls(dDir).filter((s) => isDir(join(dDir, s))).length} | ${langs.size} | ${count} |`);
}
writeFileSync(join(ROOT, 'index.html'), html);
console.log(summary.join('\n'));
if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `## 📸 Responsive screenshots\n\n${summary.join('\n')}\n\nDownload the **responsive-screenshots** artifact and open \`index.html\`.\n`);
