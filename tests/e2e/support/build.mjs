/** Build dist/ exactly like the GitHub Pages workflow (base /nimiqshop/, API
 *  on shopapi.nimiqbase.com). Cross-platform — no shell env syntax. */
import { spawnSync } from 'node:child_process';
const env = { ...process.env, PUBLIC_BASE: process.env.PUBLIC_BASE || '/nimiqshop/', API_URL: process.env.API_URL || 'https://shopapi.nimiqbase.com' };
const win = process.platform === 'win32';
const r = spawnSync(win ? 'npm.cmd' : 'npm', ['run', 'build'], { stdio: 'inherit', env, shell: win });
process.exit(r.status ?? 1);
