import assert from 'node:assert/strict';
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
const root = path.resolve('dist');
const routes = JSON.parse(readFileSync(path.join(root, '_routes.json'), 'utf8'));
assert.deepEqual(routes, { version: 1, include: ['/api/*'], exclude: [] });
const context = { window: {} };
vm.runInNewContext(readFileSync(path.join(root, 'config.js'), 'utf8'), context);
assert.equal(context.window.APP_CONFIG.API_BASE, '/api');
let pages = 0;
function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const file = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(file);
    else if (entry.name.endsWith('.html')) {
      pages++;
      const html = readFileSync(file, 'utf8');
      assert.ok(!html.includes('__CSP_SCRIPT_HASHES__'), file + ': CSP not built');
      assert.ok(!/API_BASE:\s*buildApiUrl/.test(html), file + ': cross-origin API override');
      for (const match of html.matchAll(/(?:src|href)="(\/_assets\/[^"?#]+)(?:[?#][^"]*)?"/g)) {
        assert.ok(existsSync(path.join(root, match[1])), file + ': missing ' + match[1]);
      }
    }
  }
}
walk(root);
assert.ok(pages > 0);
console.log(`Pages build verified: ${pages} static pages, same-origin config, asset references, CSP and API-only function routes.`);
