#!/usr/bin/env node
// unpin-hosts.mjs — remove the nimshop-tunnel-pin line from the hosts file
// (the launcher replaces it on every start; this is the manual clean-up).
import { unpinHostsEntry, hostsPath } from './tunnel-health.mjs';
const r = unpinHostsEntry();
console.log(r.ok ? `removed the tunnel pin from ${hostsPath()}` : `could not remove automatically (${r.how}) — delete the 'nimshop-tunnel-pin' line from ${hostsPath()} manually`);
