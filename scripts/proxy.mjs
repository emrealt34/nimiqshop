/** Private origin boundary: Cloudflare -> cloudflared -> this proxy -> Go.
 * CF-Ray is metadata, not authentication. Only configured TCP peers can supply
 * CF identity. Incoming forwarding/private-hop headers are always replaced.
 * Nothing in this module is included in the browser bundle. */
import { BlockList, isIP } from 'node:net';
import { request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';

export class ProxyIdentityError extends Error {
  constructor(message, status = 403) { super(message); this.status = status; }
}
export function canonicalIP(raw) {
  const value = String(raw || '').trim();
  if (value.includes('%') || value === '0.0.0.0' || value === '::' || !isIP(value)) throw new ProxyIdentityError('Invalid visitor IP');
  if (isIP(value) === 4) {
    const first = Number(value.split('.')[0]);
    if (first >= 224 && first < 240) throw new ProxyIdentityError('Invalid visitor IP');
    return value;
  }
  let v6 = new URL('http://[' + value + ']/').hostname.slice(1, -1).toLowerCase();
  if (/^ff/i.test(v6) || v6 === '::') throw new ProxyIdentityError('Invalid visitor IP');
  const mapped = /^::ffff:([a-f0-9]+):([a-f0-9]+)$/.exec(v6);
  if (mapped) {
    const n = (BigInt('0x' + mapped[1]) << 16n) + BigInt('0x' + mapped[2]);
    return canonicalIP([24n,16n,8n,0n].map((shift) => Number((n >> shift) & 255n)).join('.'));
  }
  return v6;
}
export function trustedPeers(cidrs) {
  const blocks = new BlockList();
  for (const raw of cidrs) {
    const [address, bitsText, extra] = String(raw).trim().split('/');
    const kind = isIP(address); const bits = Number(bitsText);
    if (!kind || extra !== undefined || !/^\d+$/.test(bitsText || '') || bits < 1 || bits > (kind === 4 ? 32 : 128)) throw new Error('Invalid/unsafe TUNNEL_PROXY_CIDRS');
    blocks.addSubnet(address, bits, kind === 4 ? 'ipv4' : 'ipv6');
  }
  return (raw) => {
    try { const ip = canonicalIP(raw); return blocks.check(ip,isIP(ip) === 4 ? 'ipv4' : 'ipv6'); } catch { return false; }
  };
}
function oneHeader(req, name, required = true) {
  const lower = name.toLowerCase();
  const value = req.headers?.[lower];
  let count = Array.isArray(value) ? value.length : value === undefined ? 0 : 1;
  if (Array.isArray(req.rawHeaders)) {
    count = 0;
    for (let i=0;i<req.rawHeaders.length;i+=2) if (req.rawHeaders[i].toLowerCase() === lower) count++;
  }
  if (count > 1 || Array.isArray(value) || (required && (count !== 1 || !String(value || '').trim()))) throw new ProxyIdentityError('Missing or repeated ' + name);
  return String(value || '');
}
function country(value) { const v = String(value || '').trim().toUpperCase(); return /^[A-Z]{2}$/.test(v) && v !== 'XX' ? v : ''; }

export function proxyOptionsFromEnv(env = process.env) {
  const mode = env.EDGE_MODE || 'direct';
  if (!['direct','cloudflare-tunnel'].includes(mode)) throw new Error('EDGE_MODE must be direct or cloudflare-tunnel');
  const cidrs = (env.TUNNEL_PROXY_CIDRS || '127.0.0.1/32,::1/128').split(',').map((s)=>s.trim()).filter(Boolean);
  const sharedSecret = env.FORWARDED_HEADER_SECRET || '';
  if (sharedSecret && (sharedSecret.length < 32 || sharedSecret.length > 512 || /\s/.test(sharedSecret))) throw new Error('FORWARDED_HEADER_SECRET must be 32-512 non-whitespace characters');
  const backend = new URL(env.BACKEND || env.BACKEND_URL || 'http://127.0.0.1:8084');
  if (!['http:','https:'].includes(backend.protocol) || backend.username || backend.password) throw new Error('Invalid BACKEND URL');
  return { mode, cidrs, trusts:trustedPeers(cidrs), sharedSecret, backend, timeoutMs:95_000 };
}
export function resolveVisitor(req, options) {
  const peer = canonicalIP(req.socket?.remoteAddress);
  const agent = oneHeader(req,'user-agent');
  if (agent.length > 4096 || /[\r\n\0]/.test(agent)) throw new ProxyIdentityError('Invalid User-Agent',400);
  if (options.mode === 'direct') {
    // An accidentally attached tunnel must not silently forward loopback as
    // the customer. This is a configuration rejection, NOT trust in CF-Ray.
    if (options.trusts(peer) && req.headers?.['cf-connecting-ip']) throw new ProxyIdentityError('Use EDGE_MODE=cloudflare-tunnel for tunnel traffic',503);
    return { ip:peer,agent,cloudflare:false,country:'',ray:'' };
  }
  if (!options.trusts(peer)) throw new ProxyIdentityError('Untrusted tunnel peer');
  const cfConnecting = oneHeader(req,'cf-connecting-ip',false);
  const hasRay = !!oneHeader(req,'cf-ray',false);
  if (!cfConnecting && !hasRay) {
    // No edge attribution at all: a LOCAL request — the operator's browser
    // hitting the origin directly while the stack runs in tunnel mode (the
    // quick tunnel died, or has not registered yet, or is simply not being
    // used right now). Serve it as a direct visitor instead of 403: a dead
    // tunnel must not take the local site's API down with it. Loopback is
    // already a trusted peer here, and half-claims (a cf-ray WITHOUT a
    // cf-connecting-ip) still fail closed below.
    return { ip:peer,agent,cloudflare:false,country:'',ray:'' };
  }
  let ip = canonicalIP(oneHeader(req,'cf-connecting-ip'));
  const ray = oneHeader(req,'cf-ray');
  if (ray.length > 128 || /[\r\n\0,]/.test(ray)) throw new ProxyIdentityError('Invalid edge trace');
  if (isIP(ip) === 4 && Number(ip.split('.')[0]) >= 240) {
    ip = canonicalIP(oneHeader(req,'cf-connecting-ipv6'));
    if (isIP(ip) !== 6) throw new ProxyIdentityError('Real IPv6 required for Pseudo IPv4');
  }
  if (ip === '2a06:98c0:3600::103') throw new ProxyIdentityError('Cross-zone Worker does not provide the original visitor IP',503);
  return { ip,agent,cloudflare:true,country:country(oneHeader(req,'cf-ipcountry',false)),ray };
}
const HOP_HEADERS = new Set(['connection','keep-alive','proxy-authenticate','proxy-authorization','te','trailer','transfer-encoding','upgrade']);
const IDENTITY_HEADERS = new Set(['forwarded','x-forwarded-for','x-real-ip','true-client-ip','cf-connecting-ip','cf-connecting-ipv6','cf-pseudo-ipv4','cf-ipcountry','cf-ray','x-nimshop-proxy-secret','x-nimshop-client-ip','x-nimshop-client-country']);

export function forwardedHeaders(req, options) {
  const visitor = resolveVisitor(req,options);
  const connectionTokens = String(req.headers?.connection || '').split(',').map((s)=>s.trim().toLowerCase());
  const headers = {};
  for (const [key,value] of Object.entries(req.headers || {})) {
    const lower = key.toLowerCase();
    if (!HOP_HEADERS.has(lower) && !IDENTITY_HEADERS.has(lower) && !connectionTokens.includes(lower)) headers[lower] = value;
  }
  headers.host = options.backend.host;
  headers['user-agent'] = visitor.agent; // exact inbound agent; never the proxy's agent
  headers['x-forwarded-for'] = visitor.ip; // ONE verified IP, not the user's XFF chain
  if (visitor.cloudflare) {
    headers['cf-connecting-ip'] = visitor.ip;
    headers['cf-ray'] = visitor.ray;
    if (visitor.country) headers['cf-ipcountry'] = visitor.country;
  }
  if (options.sharedSecret) headers['x-nimshop-proxy-secret'] = options.sharedSecret;
  return { headers, visitor };
}
export function proxyApi(req,res,options) {
  let identity;
  try { identity=forwardedHeaders(req,options); }
  catch (err) {
    if (process.env.PROXY_DIAGNOSTICS === 'true') console.error('[proxy] identity rejected:', err.message);
    res.writeHead(err.status || 403,{'Content-Type':'application/json','Cache-Control':'no-store'});
    res.end(JSON.stringify({error:'Request origin could not be verified. Use the secure shop URL or contact support.',code:'UNVERIFIED_PROXY'}));
    return;
  }
  const send = options.backend.protocol === 'https:' ? httpsRequest : httpRequest;
  let upstream;
  const fail = () => {
    if (res.destroyed || res.writableEnded) return;
    if (res.headersSent) { res.destroy(); return; }
    res.writeHead(502,{'Content-Type':'application/json','Cache-Control':'no-store'});
    res.end(JSON.stringify({error:'The shop is temporarily unavailable. Check your existing order before another payment.'}));
  };
  upstream = send(options.backend.origin + req.url,{method:req.method,headers:identity.headers},(reply)=>{
    const headers = Object.fromEntries(Object.entries(reply.headers).filter(([key])=>!HOP_HEADERS.has(key.toLowerCase())));
    // CORS policy belongs to Go; do not override it with '*'.
    res.writeHead(reply.statusCode || 502,headers);
    reply.on('error',()=>res.destroy()); reply.pipe(res);
  });
  upstream.setTimeout(options.timeoutMs,()=>upstream.destroy(new Error('upstream timeout')));
  upstream.on('error',fail);
  req.on('aborted',()=>upstream.destroy());
  req.pipe(upstream);
}
