#!/usr/bin/env node
/**
 * Render the Lighthouse CI reports as a markdown table in the job summary.
 *
 * `lhci autorun` prints a wall of text; a table next to the run is what
 * actually gets read ("did the number move since last week?"). Reads every
 * `lhr-*.json` the filesystem upload wrote into `.lighthouseci/`.
 *
 *   node scripts/lighthouse-summary.mjs
 *
 * Set GITHUB_STEP_SUMMARY to append; without it the table goes to stdout.
 */
import { readdir, readFile, appendFile } from 'node:fs/promises';
import { join } from 'node:path';

const DIR = '.lighthouseci';
const CATEGORIES = ['performance', 'accessibility', 'best-practices', 'seo'];

async function main() {
  let files;
  try {
    files = (await readdir(DIR)).filter((f) => f.startsWith('lhr-') && f.endsWith('.json'));
  } catch {
    console.log(`No ${DIR} directory — nothing to summarize.`);
    return;
  }
  if (files.length === 0) {
    console.log(`${DIR} is empty — nothing to summarize.`);
    return;
  }

  const rows = [
    `| Page | ${CATEGORIES.join(' | ')} |`,
    `|---|${CATEGORIES.map(() => '---').join('|')}|`,
  ];
  for (const file of files.sort()) {
    let report;
    try {
      report = JSON.parse(await readFile(join(DIR, file), 'utf8'));
    } catch {
      continue;
    }
    const score = (key) => {
      const value = report.categories?.[key]?.score;
      return typeof value === 'number' ? Math.round(value * 100) : '–';
    };
    const url = String(report.finalDisplayedUrl || report.finalUrl || file)
      .replace(/^https?:\/\/[^/]+/, '') || '/';
    rows.push(`| \`${url}\` | ${CATEGORIES.map(score).join(' | ')} |`);
  }

  const table = ['### Lighthouse (desktop preset)', '', ...rows, ''].join('\n');
  const summary = process.env.GITHUB_STEP_SUMMARY;
  if (summary) await appendFile(summary, `${table}\n`);
  else console.log(table);
}

main().catch((err) => {
  console.error(`lighthouse-summary: ${err.message}`);
  process.exitCode = 1;
});
