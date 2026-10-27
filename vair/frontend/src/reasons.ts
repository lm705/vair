// Why a connection ended, in the interface language.
//
// The program writes these as fixed English phrases (core: noteSessionEndReason
// and connectionLost) and they reach two places: the connection log in the AUTO
// window and the error line of the main window. Anything not listed — an
// engine's own error text — is shown as it came.
export function endReasonText(r: string, tt: (s: string) => string): string {
  if (!r) return ''
  // "auto: too slow 661ms" carries the figure that tripped it.
  const slow = /^auto: too slow (\d+)ms$/.exec(r)
  if (slow) return tt('AUTO: too slow') + ', ' + slow[1] + ' ms'
  // "proxy port 10819 (HTTP) is in use by app.exe — choose another port in
  // Settings → Local proxy" carries the port and the program (core:
  // claimProxyPorts).
  const port = /^proxy port (\d+) \((HTTP|SOCKS)\) is in use by (.+?)( — choose another port in Settings → Local proxy|, and no other port is free)$/.exec(r)
  if (port) {
    const owner = port[3] === 'another program' ? tt('another program') : port[3]
    const tail = port[4].startsWith(',') ? tt('and no other port is free') : tt('choose another port in Settings → Local proxy')
    return tt('The proxy port is in use') + ': ' + port[1] + ' (' + port[2] + '), ' + owner + ' — ' + tail
  }
  // "came up and carried nothing", with the kill-switch's half after it when a
  // block holds (core noTrafficMessage). Two halves, each translated.
  for (const head of [
    'the proxy started but no traffic goes through it',
    'the tunnel started but no traffic goes through it',
    'the chain started but no traffic goes through it — test its configs one by one to find the one that does not answer',
  ]) {
    if (r.startsWith(head)) {
      const tail = r.slice(head.length)
      const tails: Record<string, string> = {
        '': '',
        ' — traffic stays blocked for 20 s (kill-switch), then the normal connection returns':
          ' — ' + tt('traffic stays blocked for 20 s (kill-switch), then the normal connection returns'),
        ' — traffic stays blocked (kill-switch); disconnect to release it':
          ' — ' + tt('traffic stays blocked (kill-switch); disconnect to release it'),
      }
      if (tail in tails) return tt(head) + tails[tail]
    }
  }
  // "resolve server: <the resolver's own words>"
  if (r.startsWith('resolve server: ')) return tt('could not resolve the server name') + ': ' + r.slice('resolve server: '.length)
  const known: Record<string, string> = {
    'TUN mode requires administrator/root. Run the program as admin.': tt(
      'TUN mode requires administrator rights. Start Vair as administrator.',
    ),
    'switched by hand': tt('switched by hand'),
    'disconnected by hand': tt('disconnected by hand'),
    'auto: probe failed': tt('AUTO: the server stopped responding'),
    'auto: no longer among the candidates': tt('AUTO: no longer among the candidates'),
    'auto: the candidate tabs changed': tt('AUTO: no longer among the candidates'),
    'the engine stopped': tt('the engine stopped'),
    'the tunnel interface is gone': tt('the tunnel interface is gone'),
    'the tunnel engine stopped unexpectedly': tt('the tunnel engine stopped unexpectedly'),
    'came up but carried no traffic': tt('came up but carried no traffic'),
    'the server stopped responding — connection closed': tt('the server stopped responding — connection closed'),
    'the server stopped responding — traffic stays blocked (kill-switch); disconnect to release it': tt(
      'the server stopped responding — traffic stays blocked (kill-switch); disconnect to release it',
    ),
    'the chain stopped responding — connection closed (auto-connect does not rebuild a chain)': tt(
      'the chain stopped responding — connection closed (auto-connect does not rebuild a chain)',
    ),
    'the chain stopped responding — traffic stays blocked (kill-switch); disconnect to release it': tt(
      'the chain stopped responding — traffic stays blocked (kill-switch); disconnect to release it',
    ),
    'not restored: the configuration is no longer in any tab': tt(
      'not restored: the configuration is no longer in any tab',
    ),
    'not restored: a configuration of the chain is no longer in any tab': tt(
      'not restored: a configuration of the chain is no longer in any tab',
    ),
    'not restored: the configuration is no longer in any tab — traffic stays blocked (kill-switch); disconnect to release it': tt(
      'not restored: the configuration is no longer in any tab — traffic stays blocked (kill-switch); disconnect to release it',
    ),
    'not restored: a configuration of the chain is no longer in any tab — traffic stays blocked (kill-switch); disconnect to release it': tt(
      'not restored: a configuration of the chain is no longer in any tab — traffic stays blocked (kill-switch); disconnect to release it',
    ),
    'xray no longer allows turning off the certificate check (allowInsecure) — pin the certificate instead: pinnedPeerCertSha256 in the configuration': tt(
      'xray no longer allows turning off the certificate check (allowInsecure) — pin the certificate instead: pinnedPeerCertSha256 in the configuration',
    ),
  }
  return known[r] || r
}

// Why a chain was not connected (core ConnectChain, splitChain), in the
// interface language. The engine's name for a protocol stays as it came.
export function chainErrText(r: string, tt: (s: string) => string): string {
  if (!r) return ''
  const cant = /^(.+) can't be chained — no engine here can dial it$/.exec(r)
  if (cant) return cant[1] + ': ' + tt('cannot be in a chain — no engine here can connect to it')
  const known: Record<string, string> = {
    'a chain needs at least 2 configs': tt('A chain needs at least 2 configs'),
    'chain: unparseable config in selection': tt('A config in the selection could not be read'),
    'unparseable config in selection': tt('A config in the selection could not be read'),
    'bad idx': tt('A config of the selection is no longer in the tab'),
    'chain: a selected config is no longer in the tab': tt('A config of the selection is no longer in the tab'),
    'this chain changes engine more than once — reorder it so all the WireGuard/Hysteria2/TUIC hops sit together': tt(
      'This chain changes engine more than once — reorder it so all the WireGuard/Hysteria2/TUIC configs sit together',
    ),
  }
  return known[r] || r
}

export function srcErrText(r: string, tt: (s: string) => string): string {
  if (!r) return ''
  if (r.startsWith('GitHub: ')) return 'GitHub: ' + srcErrText(r.slice('GitHub: '.length), tt)
  const named = /^(.+?): (file not found|no permission to read the file|no configs found)$/.exec(r)
  if (named) return named[1] + ': ' + tt(named[2])
  const unread = /^(?:(.+?): )?could not read the file: (.*)$/.exec(r)
  if (unread) return (unread[1] ? unread[1] + ': ' : '') + tt('could not read the file') + ' (' + unread[2] + ')'
  const gh = /^GitHub API: (\d+)/.exec(r)
  if (gh) {
    if (gh[1] === '401' || gh[1] === '403') return tt('the token was not accepted') + ' (HTTP ' + gh[1] + ')'
    if (gh[1] === '404') return tt('no such repository or file, or the token cannot read it') + ' (HTTP 404)'
    return tt('the server answered with an error') + ' (HTTP ' + gh[1] + ')'
  }
  const http = /^HTTP (\d+)$/.exec(r)
  if (http) return tt('the server answered with an error') + ' (HTTP ' + http[1] + ')'
  const known: Record<string, string> = {
    'not a link — a source address starts with https://': tt('not a link — a source address starts with https://'),
    'no configs found': tt('no configs found'),
    'file not found': tt('file not found'),
    'no permission to read the file': tt('no permission to read the file'),
    'incomplete GitHub config': tt('the GitHub fields are not all filled in'),
  }
  if (known[r]) return known[r]
  if (/no such host/i.test(r)) return tt('the server name was not found') + ' (' + r + ')'
  if (/timeout|deadline exceeded/i.test(r)) return tt('the server did not answer in time')
  if (/connection refused/i.test(r)) return tt('the server refused the connection')
  if (/x509|certificate/i.test(r)) return tt("the server's certificate did not pass the check") + ' (' + r + ')'
  return r
}

// Why an update check or an update failed (core CheckForUpdate, RunUpdate,
// updateApplyBlocked), in the interface language. The text after a known
// prefix — an HTTP status, the system's own words — stays as it came.
export function updErrText(r: string, tt: (s: string) => string): string {
  if (!r) return ''
  // 404 is a missing list, not a network fault: say it in words.
  if (r === 'HTTP 404') return tt('the server has no version information (HTTP 404)')
  const known: Record<string, string> = {
    'could not reach the update server': tt('could not reach the update server'),
    'update information is not available yet — try again in a few minutes': tt(
      'update information is not available yet — try again in a few minutes',
    ),
    'update manifest is missing the download URL or checksum': tt(
      'the version information names no download or no checksum for this copy',
    ),
    'checksum mismatch — download rejected': tt('the download does not match its checksum and was rejected'),
    'installed by the setup program — download the new setup program and run it': tt(
      'installed by the setup program — download the new setup program and run it',
    ),
    'installed from a package — update it with your package manager (apt/dnf/pacman)': tt(
      'installed from a package — update it with your package manager (apt/dnf/pacman)',
    ),
    'running as an AppImage — download the new AppImage and replace this one': tt(
      'running as an AppImage — download the new AppImage and replace this one',
    ),
    'in-app updates are not available on this build — download the new release manually': tt(
      'in-app updates are not available on this build — download the new release manually',
    ),
  }
  if (known[r]) return known[r]
  for (const [head, label] of [
    ['download failed: ', 'the download failed'],
    ['download: ', 'the download failed'],
    ['write: ', 'could not save the download'],
    ['could not apply update: ', 'could not put the new version in place'],
  ]) {
    if (r.startsWith(head)) return tt(label) + ' (' + r.slice(head.length) + ')'
  }
  const http = /^download HTTP (\d+)$/.exec(r)
  if (http) return tt('the download failed') + ' (HTTP ' + http[1] + ')'
  return r
}

// Why the configuration form will not build or save a configuration (core
// formToURL, BuildConfigURL, AddManualConfig, UpdateConfig), in the interface
// language. What the parser said after "didn't validate" is its own detail and
// stays as it came; an address quoted back stays as typed.
export function formErrText(r: string, tt: (s: string) => string): string {
  if (!r) return ''
  const dv = /^the config didn't validate: (.*)$/.exec(r)
  if (dv) return tt("The configuration did not pass the check") + ': ' + dv[1]
  const proto = /^unknown protocol "(.*)"$/.exec(r)
  if (proto) return tt('Unknown protocol') + ': ' + proto[1]
  const empty = /^the address has an empty part: "(.*)"$/.exec(r)
  if (empty) return tt('The address has an empty part') + ': ' + empty[1]
  const ip = /^"(.*)" looks like an incomplete IP address$/.exec(r)
  if (ip) return ip[1] + ': ' + tt('looks like an incomplete IP address')
  return tt(r)
}
