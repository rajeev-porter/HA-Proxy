# HAProxy Ingress — annotation test rig

A small test app + web console to verify that **HAProxy Ingress annotations** actually do
what they claim, one annotation at a time, on a real cluster (Porter or any Kubernetes).

- **Backend + Console** (`main.go` + `index.html`, one binary): a dependency-free Go server
  exposing crafted endpoints (slow responses, body echo, header echo, status passthrough,
  path echo, …) and serving a **20-module console** that fires requests **through your ingress**
  and reports pass/fail.
- **Two run modes from the same image** (no second build needed):
  - `SERVE_UI=true ENABLE_CORS=false` *(default)* → **combined**: console + API on one origin.
    Simplest; recommended for Porter.
  - `SERVE_UI=false ENABLE_CORS=true` → **headless backend**: API only, the dedicated test
    *target* you annotate freely while the console runs elsewhere and reaches it cross-origin.
- **CLI** (`scripts/test.sh`): curl-based checks for the annotations a browser can't drive
  (slow-client timeouts, large headers, CORS preflight, source-IP allow/deny).

Targets the **community HAProxy Ingress controller** (`haproxy-ingress.github.io/...` keys),
which is the one the migration doc selects.

---

## 1. ingress-nginx → HAProxy annotation map

`<prefix>` = `haproxy-ingress.github.io` (the controller also accepts `ingress.kubernetes.io`).
Keys can sit on the **Ingress**, on the **Service** (backend-scope, which is how Gateway API
applies them), or in the global **ConfigMap** — the "Scope" column says where.

### Timeouts & connections
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `proxy-connect-timeout` | `timeout-connect` | Backend | TCP connect to backend (default 5s) |
| `proxy-read-timeout` | `timeout-server` | Backend | Wait for backend response (default 50s) |
| `proxy-send-timeout` | `timeout-client` | Global | Client inactivity (default 50s) |
| `keep-alive` / keepalive timeout | `timeout-keep-alive` | Backend | Idle keep-alive (default 1m) |
| `proxy-request-buffering` window | `timeout-http-request` | Backend | Time to receive full request (default 5s) |
| (queue wait) | `timeout-queue` | Backend | Time queued waiting for a slot (default 5s) |
| websocket/long-lived | `timeout-tunnel` | Backend | Tunnel/WebSocket (default 1h) |
| `upstream-keepalive-connections` | `maxconn-server` | Backend | Max conns per backend server |
| `limit-connections` | `limit-connections` | Backend | Max concurrent conns |
| global worker conns | `max-connections` | Global | HAProxy `maxconn` (default 2000) |

### Body, buffers, payloads
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `proxy-body-size` | `proxy-body-size` | Path | Same name. Accepts `k`/`m`/`g`. The doc's `maxbody-size` is wrong. |
| `proxy-buffer-size` | *(no direct key)* | — | Header buffer is `tune.bufsize`; set via `config-global` snippet, not a per-route key |
| `client-body-buffer-size` | *(no direct key)* | — | Same — global HAProxy tuning, not an annotation |

### Rate limiting & access control
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `limit-rps` | `limit-rps` | Backend | Requests per second |
| `limit-rpm` | *(use `limit-rps`)* | Backend | No per-minute key; convert to rps |
| `limit-connections` | `limit-connections` | Backend | Concurrent connections |
| `limit-whitelist` | `limit-whitelist` | Backend | CIDRs exempt from the limit |
| `whitelist-source-range` | `allowlist-source-range` | Path | `whitelist-source-range` kept as alias |
| (deny list) | `denylist-source-range` | Path | Allow-by-default, deny these |
| (trust a header for src IP) | `allowlist-source-header` | Path | Read client IP from a named header |

### TLS / SSL / HSTS
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `ssl-redirect` | `ssl-redirect` | Path | http→https redirect (default true) |
| `force-ssl-redirect` | `ssl-redirect: "true"` (+ `ssl-always-add-https`) | Path/Host | Redirect even without TLS host config |
| `ssl-passthrough` | `ssl-passthrough` | Host | TCP-mode passthrough |
| `ssl-ciphers` | `ssl-ciphers` / `ssl-cipher-suites` | Host | TLS 1.2 / 1.3 cipher lists |
| `hsts` | `hsts` | Path | default true |
| `hsts-max-age` | `hsts-max-age` | Path | default 15768000 |
| `hsts-include-subdomains` | `hsts-include-subdomains` | Path | |
| `hsts-preload` | `hsts-preload` | Path | |

### Auth
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `auth-type: basic` + `auth-secret` | `auth-secret` | Path | Secret with an `auth` (htpasswd) key |
| `auth-realm` | `auth-realm` | Path | |
| `auth-url` (external auth) | `auth-url` | Path | Supports `svc://` targets |
| `auth-signin` | `auth-signin` | Path | Redirect on auth failure |
| `auth-tls-secret` | `auth-tls-secret` | Host | mTLS client CA bundle |
| `auth-tls-verify-client` | `auth-tls-verify-client` | Host | `off`/`optional`/`on` |
| `auth-tls-error-page` | `auth-tls-error-page` | Host | |

### CORS
| ingress-nginx | HAProxy key | Scope |
|---|---|---|
| `enable-cors` | `cors-enable` | Path |
| `cors-allow-origin` | `cors-allow-origin` (+ `cors-allow-origin-regex`) | Path |
| `cors-allow-methods` | `cors-allow-methods` | Path |
| `cors-allow-headers` | `cors-allow-headers` | Path |
| `cors-allow-credentials` | `cors-allow-credentials` | Path |
| `cors-expose-headers` | `cors-expose-headers` | Path |
| `cors-max-age` | `cors-max-age` | Path |

### Routing, rewrite, redirect, headers
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `rewrite-target` | `rewrite-target` | Path | |
| `app-root` | `app-root` | Host | Redirect `/` to a subpath |
| `permanent-redirect` | `redirect-to` (+ `redirect-to-code: "301"`) | Path | |
| `temporal-redirect` | `redirect-to` (default code 302) | Path | |
| `from-to-www-redirect` | `redirect-from` / `server-alias` | Host | |
| `use-regex` / `pathType` | `path-type` (`begin`/`exact`/`prefix`/`regex`) | Path | + global `path-type-order` |
| custom request/response headers | `headers` | Backend | multiline `Name: value` pairs |
| `configuration-snippet` | `config-backend` | Backend | raw HAProxy backend lines |
| `server-snippet` | `config-frontend` | Global | raw HAProxy frontend lines |

### Load balancing & sticky sessions
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `load-balance` | `balance-algorithm` | Backend | `roundrobin`, `leastconn`, `random`, … |
| `upstream-hash-by` | `balance-algorithm: uri`/`source` (+ `assign-backend-server-id`) | Backend | |
| `affinity: cookie` | `affinity: cookie` | Backend | |
| `session-cookie-name` | `session-cookie-name` | Backend | |
| `session-cookie-expires`/`max-age` | `session-cookie-keywords` | Backend | |
| `session-cookie-path`/domain | `session-cookie-domain` | Backend | |

### Backend protocol & health
| ingress-nginx | HAProxy key | Scope | Notes |
|---|---|---|---|
| `backend-protocol: HTTP/HTTPS/GRPC/GRPCS` | `backend-protocol: h1/h1-ssl/h2/h2-ssl` | Backend | also `fcgi` since v0.16 |
| `proxy-ssl-secret` | `secure-crt-secret` (+ `secure-backends: "true"`) | Backend | |
| `proxy-ssl-verify` | `secure-verify-ca-secret` | Backend | |
| health check path | `health-check-uri` | Backend | + `health-check-interval`, `…-port` |

### WAF / ModSecurity
| ingress-nginx | HAProxy key | Scope |
|---|---|---|
| `enable-modsecurity` | `waf: "modsecurity"` (+ `modsecurity-endpoints`) | Path / Global |
| `modsecurity-snippet` | `config-backend` snippet | Backend |

> Full reference: <https://haproxy-ingress.github.io/docs/configuration/keys/>

---

## 2. Build & push the image

```bash
docker build -t <your-registry>/haproxy-tester:latest .
docker push <your-registry>/haproxy-tester:latest
```

One image, two roles (chosen at runtime by env vars — no rebuild):

| Role | Env | What it is |
|---|---|---|
| Combined *(default)* | `SERVE_UI=true` `ENABLE_CORS=false` | Console + API on one origin. Use this. |
| Headless backend | `SERVE_UI=false` `ENABLE_CORS=true` | API-only test target; console runs elsewhere. |

## 3. Deploy on Porter (no manifests)

Porter creates the Deployment, Service, Ingress and Gateway for you. You only deploy the
image and paste annotations into the dashboard. **Ignore the `k8s/` folder on Porter** — it's
there only for plain (non-Porter) clusters.

**Simplest — one combined app (recommended):**
1. Create a **Web service** from your image. Port **8080**. Scale to **2+ replicas** (so the
   load-balancing and affinity modules have something to show).
2. Deploy. Open the app's URL — that's the console.
3. Add **one** annotation at a time in Porter:
   - **Ingress path:** app → *Networking / Advanced* → **Custom ingress annotations** → add e.g.
     `haproxy-ingress.github.io/timeout-server: 5s`.
   - **Gateway API path:** put the same key under the **Service annotations** field instead
     (the doc's `service.annotations`). The keys are identical either way.
4. Redeploy/save, wait a couple seconds for HAProxy to reload, run the matching module.

Caveat of the combined app: an annotation you're testing also applies to the console's own
page loads (e.g. basic-auth will prompt you once; a tiny `limit-rps` will throttle the page).
Fine for most tests — load the page first, then fire from the buttons.

**Cleaner isolation — two apps (optional):**
1. **Backend app** from the same image with env `SERVE_UI=false`, `ENABLE_CORS=true`. This is
   the *target* — put all annotations under test here. Scale to 2+ replicas.
2. **Console app** from the same image (defaults). Open it, and in the **target** field enter
   the backend app's URL.
3. Because the two are different origins, the backend's `ENABLE_CORS=true` lets the console
   reach it. This also lets you slap `auth-secret` on the backend without locking yourself out
   of the console. (CORS testing — module 15 — *requires* this two-origin setup.)

## 4. Use the console

Open the app URL. Put the HAProxy-routed host in **target** (or leave blank to use the page's
own origin). The wire indicator top-right pings `/api/ping` to confirm the target is valid.

Per annotation: add it in the Porter dashboard → save → run the matching module. Each module
prints the request, the response, and a **PASS / FAIL / INFO** verdict. **INFO** is not failure
— it means the request reached the backend (limit not hit / annotation not applied / input
under the threshold). Only PASS and FAIL are verdicts.

### The 20 modules
| # | Module | Annotation under test |
|---|---|---|
| 01 | Connectivity & request ID | `config-frontend: unique-id-header` |
| 02 | Read / server timeout | `timeout-server` |
| 03 | Request body size | `proxy-body-size` |
| 04 | Rate limit (rps) | `limit-rps` |
| 05 | Concurrent connection limit | `limit-connections` |
| 06 | Basic auth | `auth-secret` |
| 07 | Custom **request** header → backend | `headers` |
| 08 | Custom **response** header → client | `config-backend: http-response set-header` |
| 09 | HSTS | `hsts` |
| 10 | SSL redirect | `ssl-redirect` |
| 11 | App-root redirect | `app-root` |
| 12 | Permanent / temporary redirect | `redirect-to` (+ `redirect-to-code`) |
| 13 | Rewrite target | `rewrite-target` |
| 14 | Source IP allowlist | `allowlist-source-range` / `allowlist-source-header` |
| 15 | CORS | `cors-enable` (+ `cors-allow-origin`) — needs the two-app setup |
| 16 | Load balancing across pods | `balance-algorithm` — needs 2+ replicas |
| 17 | Sticky sessions (affinity) | `affinity: cookie` — needs 2+ replicas |
| 18 | WAF / ModSecurity | `waf: modsecurity` |
| 19 | Forwarded headers | `forwardfor` / `use-forwarded-proto` |
| 20 | Not browser-testable → CLI | `timeout-connect`, `timeout-client`, buffer-size, ssl-ciphers, mTLS |

## 5. CLI tests (the browser-awkward ones)

```bash
./scripts/test.sh https://your-app.host            # all sections
./scripts/test.sh https://your-app.host bodysize   # one section
```
Sections: `ping timeout_server timeout_client bodysize ratelimit headers cors allowlist bigheader`.
Use these for `timeout-client`/`timeout-connect` (needs a slow client), `bigheader`
(buffer sizing), `cors` (real preflight), and `allowlist` from your real source IP.

## Notes & honest limits
- For basic auth, create the secret first:
  `htpasswd -nbB admin secret > auth && kubectl create secret generic app-basic-auth --from-file=auth`,
  then reference it with `auth-secret: app-basic-auth`.
- Module 08 reads custom **response** headers; browsers only expose them **same-origin**, so run
  it on the combined app (or via the CLI), not the two-app split.
- `allowlist-source-range` is judged on your real IP — test it from the CLI on an allowed/blocked
  network, or use `allowlist-source-header` (module 14) which a browser can set.
- Modules 16/17 need **2+ replicas** to mean anything — scale the app in Porter.
- `waf: modsecurity` needs a ModSecurity sidecar/service reachable via `modsecurity-endpoints`;
  without it the WAF module just shows INFO.
- The backend sets **no** CORS, auth, or rate-limit behaviour of its own (except the optional
  `ENABLE_CORS` reflector), so anything else you observe is HAProxy's doing.
