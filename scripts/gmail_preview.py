#!/usr/bin/env python3
"""Render the REAL gift-note email inside a Gmail-style reader UI and screenshot it."""
import sys
from playwright.sync_api import sync_playwright

def gmail_page(email_html: str, subject: str, snippet: str) -> str:
    # The email is a standalone HTML document -> render it inside an iframe
    # exactly as Gmail does (isolated document, own styles).
    return f"""<!DOCTYPE html>
<html><head><meta charset="utf-8">
<style>
  * {{ margin:0; padding:0; box-sizing:border-box; }}
  body {{ background:#f8fafd; font-family: 'Google Sans', Roboto, Arial, sans-serif; }}
  .hdr {{ background:#ffffff; border-bottom:1px solid #e0e0e0; padding:10px 20px; display:flex; align-items:center; gap:14px; }}
  .gmail-logo {{ font-size:22px; color:#5f6368; font-weight:400; display:flex; align-items:center; gap:8px; }}
  .search {{ flex:1; max-width:680px; margin:0 auto; background:#eaf1fb; border-radius:24px; padding:9px 18px; font-size:14px; color:#5f6368; display:flex; align-items:center; gap:12px; }}
  .search svg {{ flex:none; }}
  .avatar-me {{ width:32px; height:32px; border-radius:50%; background:#7c4dff; color:#fff; font-size:14px; font-weight:500; display:flex; align-items:center; justify-content:center; }}
  .toolbar {{ background:#ffffff; padding:6px 20px; display:flex; gap:6px; align-items:center; border-bottom:1px solid #f1f3f4; }}
  .tbtn {{ width:36px; height:36px; border-radius:50%; display:flex; align-items:center; justify-content:center; color:#444746; }}
  .content {{ max-width:1200px; margin:0 auto; padding:16px 20px 40px; }}
  .msg {{ background:#ffffff; border-radius:16px; box-shadow:0 1px 3px rgba(60,64,67,.18); overflow:hidden; }}
  .msg-head {{ padding:16px 20px 4px; }}
  .msg-row {{ display:flex; align-items:center; gap:14px; }}
  .sender-av {{ width:40px; height:40px; border-radius:50%; background:linear-gradient(135deg,#b98a2e,#e9c46a); color:#fff; font-weight:700; font-size:18px; display:flex; align-items:center; justify-content:center; flex:none; }}
  .sender {{ font-size:14px; color:#202124; line-height:1.35; flex:1; min-width:0; }}
  .sender b {{ font-size:14px; font-weight:700; }}
  .sender .to {{ color:#5f6368; font-size:12px; background:#f1f3f4; border-radius:10px; padding:1px 8px; margin-left:6px; }}
  .sender .addr {{ color:#5f6368; font-size:12px; }}
  .msg-actions {{ display:flex; align-items:center; gap:4px; color:#5f6368; flex:none; }}
  .msg-actions .time {{ font-size:12px; color:#5f6368; margin-right:10px; }}
  .subject {{ font-size:22px; font-weight:500; color:#202124; padding:14px 20px 6px; display:flex; align-items:center; gap:10px; }}
  .subject .inbox-chip {{ font-size:11px; color:#5f6368; border:1px solid #dadce0; border-radius:6px; padding:2px 8px; font-weight:500; letter-spacing:.3px; flex:none; }}
  .snippet {{ padding:0 20px 10px; font-size:13px; color:#5f6368; }}
  iframe {{ width:100%; border:0; display:block; }}
  .replyrow {{ display:flex; gap:10px; padding:14px 20px 18px; }}
  .rbtn {{ border:1px solid #dadce0; border-radius:16px; padding:6px 16px; font-size:13px; color:#3c4043; display:flex; gap:6px; align-items:center; font-weight:500; }}
</style></head>
<body>
  <div class="hdr">
    <div class="gmail-logo">
      <svg width="30" height="22" viewBox="0 0 30 22"><path fill="#4285f4" d="M3 22h6V8L0 1v18a3 3 0 0 0 3 3z"/><path fill="#34a853" d="M21 22h6a3 3 0 0 0 3-3V1l-9 7z"/><path fill="#fbbc04" d="M21 0v8l-6 5-6-5V0z" transform="translate(-3 0) translate(6 0)"/><path fill="#ea4335" d="M9 0h12v8l-6 5L3 3z"/><path fill="#fbbc04" d="M9 0v8l6 5 6-5V0z"/></svg>
      Gmail
    </div>
    <div class="search">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="#444746"><path d="M15.5 14h-.79l-.28-.27a6.5 6.5 0 1 0-.7.7l.27.28v.79l5 4.99L20.49 19zm-6 0A4.5 4.5 0 1 1 14 9.5 4.5 4.5 0 0 1 9.5 14z"/></svg>
      Search in mail
    </div>
    <div style="display:flex;gap:14px;color:#444746;align-items:center;">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M12 22c1.1 0 2-.9 2-2h-4a2 2 0 0 0 2 2zm6-6v-5a6 6 0 0 0-5-5.91V4a1 1 0 1 0-2 0v1.09A6 6 0 0 0 6 11v5l-2 2v1h16v-1z"/></svg>
      <div class="avatar-me">F</div>
    </div>
  </div>
  <div class="toolbar">
    <div class="tbtn"><svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20z"/></svg></div>
    <div class="tbtn"><svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M6 2v6l.5 1L6 9.5V22h12V9.5l-.5-.5.5-1V2z" fill="none" stroke="currentColor" stroke-width="2"/></svg></div>
    <div class="tbtn"><svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6zm2.46-7.12 1.41-1.41L12 10.59l2.12 2.12 1.41-1.41L13.41 9l2.13-2.12-1.41-1.41L12 7.59 9.88 5.46 8.46 6.88 10.59 9z"/></svg></div>
    <div class="tbtn"><svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M12 22c1.1 0 2-.9 2-2h-4a2 2 0 0 0 2 2zm6-6v-5a6 6 0 0 0-5-5.91V4a1 1 0 1 0-2 0v1.09A6 6 0 0 0 6 11v5l-2 2v1h16v-1z"/></svg></div>
    <div style="flex:1"></div>
    <div class="tbtn"><svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M18 2H6c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-3 8h-3v3h-2v-3H7V8h3V5h2v3h3z" transform="translate(-2 -1) scale(.9)"/></svg></div>
    <div class="tbtn"><svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M10 4H4a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-8z"/></svg></div>
  </div>
  <div class="content">
    <div class="msg">
      <div class="msg-head">
        <div class="msg-row">
          <div class="sender-av">n</div>
          <div class="sender">
            <b>nim.shop</b> <span class="addr">&lt;hello@shop.nimiqbase.com&gt;</span><span class="to">to me</span><br>
          </div>
          <div class="msg-actions">
            <span class="time">11 Sep, 09:24</span>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="#5f6368"><path d="m22 9.24-7.19-.62L12 2 9.19 8.62 2 9.24l5.46 4.73L5.82 21 12 17.27 18.18 21l-1.63-7.03z"/></svg>
          </div>
        </div>
      </div>
      <div class="subject"><span class="inbox-chip">INBOX</span>{subject}</div>
      <div class="snippet">{snippet}</div>
      <iframe id="mail" srcdoc="{email_html}"></iframe>
      <div class="replyrow">
        <div class="rbtn"><svg width="16" height="16" viewBox="0 0 24 24" fill="#3c4043"><path d="M10 9V5l-7 7 7 7v-4.1c5 0 8.5 1.6 11 5.1-1-5-4-10-11-11z"/></svg> Reply</div>
        <div class="rbtn"><svg width="16" height="16" viewBox="0 0 24 24" fill="#3c4043"><path d="M10 9V5l-7 7 7 7v-4.1c5 0 8.5 1.6 11 5.1-1-5-4-10-11-11z" transform="scale(-1,1) translate(-24,0)"/></svg> Forward</div>
      </div>
    </div>
  </div>
</body></html>"""


def shoot(email_path: str, subject: str, snippet: str, out_png: str):
    with sync_playwright() as p:
        browser = p.chromium.launch()
        page = browser.new_page(viewport={"width": 1180, "height": 900}, device_scale_factor=2)
        html = open(email_path, encoding="utf-8").read()
        # Escape for the srcdoc attribute
        escaped = html.replace("&", "&amp;").replace('"', "&quot;")
        page.set_content(gmail_page(escaped, subject, snippet), wait_until="load")
        page.wait_for_timeout(400)
        # Fit the iframe to the email's real height
        page.evaluate(
            """() => {
              const f = document.getElementById('mail');
              f.style.height = f.contentDocument.documentElement.scrollHeight + 'px';
            }"""
        )
        page.wait_for_timeout(400)
        page.screenshot(path=out_png, full_page=True)
        browser.close()
        print("wrote", out_png)


if __name__ == "__main__":
    shoot(
        "/var/tmp/giftmail-preview/named.html",
        "A gift arrived for you 🎁 — and planted a tree 🌳",
        "Someone sent you a gift via nim.shop — Steam · 50 USD, and a tree is being planted. Here is what arrived and where to find it.",
        "/home/user/nimshop-extract/nimshop/ekran-goruntuleri/gmail-hediye-notu-isimli.png",
    )
    shoot(
        "/var/tmp/giftmail-preview/anonymous.html",
        "Someone sent you a gift 🎁 — and planted a tree 🌳",
        "Someone (anonymous) sent you a gift via nim.shop — Steam · 50 USD, and a tree is being planted. Here is what arrived and where to find it.",
        "/home/user/nimshop-extract/nimshop/ekran-goruntuleri/gmail-hediye-notu-anonim.png",
    )
