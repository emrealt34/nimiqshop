#!/usr/bin/env node
/* devtools/ensure-env.js
 *
 * Works together with `start.bat`: validates backend/.env file and
 * auto-generates missing / REPLACE_WITH / too short required fields so that backend can
 * start WITHOUT THROWING AN ERROR even if env values were not entered.
 *
 * REQUIRED fields for backend to start (config.go Validate):
 *   - JWT_SECRET                (>=32 byte random)
 *   - CRYPTOREFILLS_WEBHOOK_KEY (>=32 byte random)
 *   - PUBLIC_WEBHOOK_BASE_URL   (absolute https URL)
 *
 * CRYPTOREFILLS_PARTNER_ID can be left empty (backend starts; only live
 * catalog/payment uses it). Empty admin fields = admin disabled.
 *
 * Usage: node devtools/ensure-env.js   (--root <path> or cwd = repo root)
 */
'use strict';
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const root = process.argv.includes('--root')
  ? path.resolve(process.argv[process.argv.indexOf('--root') + 1])
  : path.resolve(__dirname, '..');

const envPath = path.join(root, 'backend', '.env');
const examplePath = path.join(root, 'backend', '.env.example');

function randomSecret(bytes = 48) {
  return crypto.randomBytes(bytes).toString('base64url');
}

// Should a value be filled? (empty / placeholder / too short / example placeholder)
function needsFill(value, minLen, ignoreExample = false) {
  const v = (value || '').trim();
  if (!v) return true;
  if (/REPLACE_WITH/i.test(v)) return true;
  if (ignoreExample && /\.example\.com|example\.com|your-domain/i.test(v)) return true;
  if (minLen && v.length < minLen) return true;
  return false;
}

function main() {
  if (!fs.existsSync(envPath)) {
    const base = fs.existsSync(examplePath) ? fs.readFileSync(examplePath, 'utf8') : '';
    fs.writeFileSync(envPath, base);
    console.log('[env] backend/.env created (from example).');
  }

  let content = fs.readFileSync(envPath, 'utf8');
  let changed = false;

  const fills = {
    JWT_SECRET: () => randomSecret(48),
    CRYPTOREFILLS_WEBHOOK_KEY: () => randomSecret(48),
    PUBLIC_WEBHOOK_BASE_URL: () => 'https://localhost',
  };

  for (const [key, gen] of Object.entries(fills)) {
    const minLen = key === 'PUBLIC_WEBHOOK_BASE_URL' ? 0 : 32;
    const ignoreExample = key === 'PUBLIC_WEBHOOK_BASE_URL';
    // Find existing line (null if not exists).
    const re = new RegExp('^' + key + '(\\s*=\\s*.*)?$', 'm');
    const m = re.exec(content);
    const curVal = m ? m[1] ? m[1].replace(/^\s*=\s*/, '').trim().replace(/^"|"$/g, '') : '' : '';

    if (!m || needsFill(curVal, minLen, ignoreExample)) {
      const val = gen();
      if (m) {
        content = content.substring(0, m.index) + key + '=' + val + content.substring(m.index + m[0].length);
      } else {
        content = content.trimEnd() + '\n' + key + '=' + val + '\n';
      }
      changed = true;
      console.log('[env] ' + key + ' auto-filled.');
    }
  }

  fs.writeFileSync(envPath, content.trimEnd() + '\n');
  console.log(changed
    ? '[env] backend/.env updated — required fields are filled, will start without error.'
    : '[env] backend/.env valid — no changes needed.');
}

main();
