// fennec.runta.com: the static site (ASSETS) plus release downloads from the
// fennec-releases R2 bucket (RELEASES), which scripts/sync-release-r2.sh fills.
//
//   /download/<product>/latest[/<kind>]       302 to the newest version's file
//   /download/<product>/v<version>/<file>     the file itself (ranges supported)
//
// Products and the files their latest.json names:
//   fennec        dmg (default), zip, checksums
//   computer-use  dmg (default), full, core, checksums

const PRODUCTS = {
  fennec: { '': 'dmg', dmg: 'dmg', zip: 'zip', checksums: 'checksums' },
  'computer-use': { '': 'dmg', dmg: 'dmg', full: 'full', core: 'core', checksums: 'checksums' },
};
const VERSION = /^v[0-9]+\.[0-9]+\.[0-9]+$/;
const FILE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

function notFound() {
  return new Response('Not found\n', { status: 404, headers: { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' } });
}

async function latest(env, url, product, kind) {
  const name = PRODUCTS[product][kind ?? ''];
  if (!name) return notFound();
  const obj = await env.RELEASES.get(`${product}/latest.json`);
  if (!obj) return notFound();
  const info = await obj.json();
  // 0.1.0 of computer-use has no disk image: its default is the full archive.
  const file = info && info.files && (info.files[name] || (!kind && info.files.full));
  if (!file || !VERSION.test('v' + info.version) || !FILE.test(file)) return notFound();
  const to = new URL(`/download/${product}/v${info.version}/${file}`, url);
  return new Response(null, { status: 302, headers: { location: to.toString(), 'cache-control': 'no-store' } });
}

async function file(env, request, key, name) {
  const head = request.method === 'HEAD';
  const obj = head
    ? await env.RELEASES.head(key)
    : await env.RELEASES.get(key, { range: request.headers, onlyIf: request.headers });
  if (!obj) return notFound();
  const headers = new Headers();
  obj.writeHttpMetadata(headers);
  headers.set('etag', obj.httpEtag);
  headers.set('accept-ranges', 'bytes');
  headers.set('cache-control', 'public, max-age=31536000, immutable');
  headers.set('content-disposition', `attachment; filename="${name}"`);
  headers.set('x-content-type-options', 'nosniff');
  if (head) {
    headers.set('content-length', String(obj.size));
    return new Response(null, { status: 200, headers });
  }
  if (!('body' in obj) || obj.body === undefined) {
    // An If-None-Match or If-Modified-Since precondition held: no body.
    return new Response(null, { status: 304, headers });
  }
  let status = 200;
  if (obj.range && request.headers.has('range')) {
    const offset = obj.range.offset ?? (obj.range.suffix !== undefined ? obj.size - obj.range.suffix : 0);
    const length = obj.range.length ?? obj.size - offset;
    headers.set('content-range', `bytes ${offset}-${offset + length - 1}/${obj.size}`);
    headers.set('content-length', String(length));
    status = 206;
  } else {
    headers.set('content-length', String(obj.size));
  }
  return new Response(obj.body, { status, headers });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (!url.pathname.startsWith('/download/')) return env.ASSETS.fetch(request);
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return new Response('Method not allowed\n', { status: 405, headers: { allow: 'GET, HEAD' } });
    }
    const parts = url.pathname.slice('/download/'.length).split('/').filter(Boolean);
    const product = parts[0];
    if (!Object.hasOwn(PRODUCTS, product)) return notFound();
    if (parts[1] === 'latest' && parts.length <= 3) return latest(env, url, product, parts[2]);
    if (parts.length === 3 && VERSION.test(parts[1]) && FILE.test(parts[2])) {
      return file(env, request, parts.join('/'), parts[2]);
    }
    return notFound();
  },
};
