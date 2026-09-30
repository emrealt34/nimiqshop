#!/usr/bin/env python3
"""Rasterize the REAL @nimiq/identicons face (the exact vendored module the
site ships) for a wallet address, in a real browser, to PNG data URLs at
several sizes — ground truth for the email avatar pipeline + size budget."""
import json
import mimetypes
import pathlib
from playwright.sync_api import sync_playwright

# The probe page and the vendored module are served to the browser from a
# synthetic origin through Playwright request interception — no local HTTP
# server, no port, nothing written into public/.
ORIGIN = "https://identicon.invalid"
PUBLIC = pathlib.Path(__file__).resolve().parent.parent / "public"

PAGE = """<!DOCTYPE html><html><body>
<script type="module">
window.__result = undefined;
new Promise(async (resolve) => {
  try {
    const mod = await import('/vendor/identicons.module.js');
    const I = mod.default;
    I.svgPath = '/vendor/identicons.min.svg';
    self.NIMIQ_IDENTICONS_SVG_PATH = '/vendor/identicons.min.svg';
    I._assetsPromise = null;
    const addr = 'NQ07 XQ4T R7EL A5A6 2L0X SD59 4RNE 3Y4L 0S8M';
    const svg = await I.svg(addr);          // the real composed SVG
    const url = await I.toDataUrl(addr);    // data:image/svg+xml;base64,...
    // Rasterize exactly like the checkout will: Image -> canvas -> PNG.
    const out = { svgLength: svg.length, sizes: {} };
    for (const size of [160, 88, 64, 44, 32, 29]) {
      const img = new Image();
      const png = await new Promise((res, rej) => {
        img.onload = () => {
          const c = document.createElement('canvas');
          c.width = size; c.height = size;
          const ctx = c.getContext('2d');
          ctx.imageSmoothingEnabled = true;
          ctx.drawImage(img, 0, 0, size, size);
          res(c.toDataURL('image/png'));
        };
        img.onerror = rej;
        img.src = url;
      });
      out.sizes[size] = png;
    }
    out.hashSvgHasParts = { face: svg.includes('face_'), top: svg.includes('top_'), side: svg.includes('side_'), bottom: svg.includes('bottom_') };
    // what the module's own hash produced (for the Go port ground truth, if ever needed)
    out.makeHash = (await import('/vendor/hashprobe.js')).makeHash(addr);
    resolve(out);
  } catch (e) {
    resolve({ error: String(e) });
  }
}).then((out) => { window.__result = out; });
</script>
</body></html>"""

HASH_PROBE = """
export function makeHash(t){
  const r = ("" + t.split("").map(t => Number(t.charCodeAt(0))+3).reduce((t,r)=>t*(1-t)*__chaosHash(r), .5)).split("").reduce((t,r)=>r+t, "");
  return _padEnd(r.replace(".", r[5]).substr(4,17), 13, r[5]);
}
function __chaosHash(t){ let r=1/t; for(let t=0;t<100;t++) r=(1-r)*r*3.569956786876; return r }
function _padEnd(t,r,e){ if(String.prototype.padEnd) return t.padEnd(r,e); for(;t.length<r;)t+=e; return t.substring(0,Math.max(t.length,r)) }
"""

def serve(route, request):
    """Fulfil a request for ORIGIN from memory (probe page, hash probe) or from
    the checked-in public/ tree (the vendored identicons module + sprite)."""
    rel = request.url[len(ORIGIN):].split("?", 1)[0]
    if rel in ("/", "/probe.html"):
        return route.fulfill(status=200, content_type="text/html", body=PAGE)
    if rel == "/vendor/hashprobe.js":
        return route.fulfill(status=200, content_type="text/javascript", body=HASH_PROBE)
    target = (PUBLIC / rel.lstrip("/")).resolve()
    if PUBLIC not in target.parents or not target.is_file():
        return route.fulfill(status=404, body="not found")
    ctype = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
    if target.suffix in (".js", ".mjs"):
        ctype = "text/javascript"
    return route.fulfill(status=200, content_type=ctype, body=target.read_bytes())


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch()
        page = browser.new_page(viewport={"width": 400, "height": 300})
        page.route(f"{ORIGIN}/**", serve)
        page.goto(f"{ORIGIN}/probe.html")
        page.wait_for_function("window.__result !== undefined", timeout=15000)
        out = page.evaluate("window.__result")
        browser.close()

    print(json.dumps({
        "svgLength": out.get("svgLength"),
        "parts": out.get("hashSvgHasParts"),
        "makeHash": out.get("makeHash"),
        "sizes": {k: len(v) for k, v in out.get("sizes", {}).items()},
        "error": out.get("error"),
    }, indent=2))
    with open("/var/tmp/identicon-real.json", "w") as f:
        json.dump(out, f)
    print("saved /var/tmp/identicon-real.json")


if __name__ == "__main__":
    main()
