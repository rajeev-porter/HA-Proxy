// haproxy-annotation-tester
//
// A tiny, dependency-free HTTP server whose only job is to let you verify that
// HAProxy Ingress annotations actually do what the doc says they do.
//
// It exposes crafted endpoints (slow responses, body echo, header echo, status
// passthrough, path echo, ...) and — unless disabled — serves a single-page
// console at "/" that fires requests at those endpoints THROUGH your ingress
// and reports pass/fail.
//
// Behaviour is controlled by two env vars so the SAME image can run two ways:
//
//   SERVE_UI=true   ENABLE_CORS=false   (defaults) -> combined app: console + API,
//                                        same origin. Best for most tests.
//   SERVE_UI=false  ENABLE_CORS=true                -> API-only backend you can
//                                        annotate freely (even auth) while the
//                                        console runs elsewhere and reaches it
//                                        cross-origin.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed index.html
var ui embed.FS

const (
	maxDelayMs   = 120_000
	maxSizeBytes = 64 << 20
	appHeader    = "haproxy-annotation-tester"
)

var enableCORS bool

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-App", appHeader)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func reqID(r *http.Request) string {
	for _, k := range []string{"X-Request-ID", "X-Request-Id", "X-Id"} {
		if v := r.Header.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "host": hostname(), "time": time.Now().UTC().Format(time.RFC3339Nano),
		"method": r.Method, "path": r.URL.Path, "request_id": reqID(r),
	})
}

func delay(w http.ResponseWriter, r *http.Request) {
	ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
	if ms < 0 {
		ms = 0
	}
	if ms > maxDelayMs {
		ms = maxDelayMs
	}
	start := time.Now()
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
	case <-r.Context().Done():
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "host": hostname(), "slept_ms": ms, "actual_ms": time.Since(start).Milliseconds(),
	})
}

func echo(w http.ResponseWriter, r *http.Request) {
	n, err := io.Copy(io.Discard, io.LimitReader(r.Body, maxSizeBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "host": hostname(), "received_bytes": n})
}

func headers(w http.ResponseWriter, r *http.Request) {
	flat := map[string]string{}
	for k, vals := range r.Header {
		flat[k] = strings.Join(vals, ", ")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "host": hostname(), "headers": flat})
}

func whoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "host": hostname(), "method": r.Method, "path": r.URL.Path,
		"query": r.URL.RawQuery, "remote_addr": r.RemoteAddr,
		"x_forwarded_for": r.Header.Get("X-Forwarded-For"), "x_real_ip": r.Header.Get("X-Real-IP"),
		"x_forwarded_proto": r.Header.Get("X-Forwarded-Proto"), "x_forwarded_host": r.Header.Get("X-Forwarded-Host"),
		"request_id": reqID(r),
	})
}

// pathEcho is a subtree handler (/api/path/) that reports the full path the
// backend received — used for rewrite-target and path-type checks.
func pathEcho(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "host": hostname(), "path": r.URL.Path, "query": r.URL.RawQuery,
	})
}

func status(w http.ResponseWriter, r *http.Request) {
	code, _ := strconv.Atoi(r.URL.Query().Get("code"))
	if code < 100 || code > 599 {
		code = 200
	}
	writeJSON(w, code, map[string]any{"ok": code < 400, "host": hostname(), "status": code})
}

func size(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
	if n < 0 {
		n = 0
	}
	if n > maxSizeBytes {
		n = maxSizeBytes
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-App", appHeader)
	w.Header().Set("Content-Length", strconv.Itoa(n))
	w.WriteHeader(http.StatusOK)
	buf := make([]byte, 32*1024)
	for i := range buf {
		buf[i] = 'x'
	}
	for n > 0 {
		c := len(buf)
		if n < c {
			c = n
		}
		if _, err := w.Write(buf[:c]); err != nil {
			return
		}
		n -= c
	}
}

func auth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "host": hostname(), "authorization_seen": r.Header.Get("Authorization") != "",
	})
}

// cors wraps a handler. When ENABLE_CORS=true it reflects the Origin, allows
// credentials, and exposes all response headers so a console served from a
// different origin can still read HAProxy-added headers and statuses.
// NOTE: with CORS on, this backend emits its own CORS headers, so don't use it
// to test the cors-enable annotation (use the combined app for that).
func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if enableCORS {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = "*"
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			reqH := r.Header.Get("Access-Control-Request-Headers")
			if reqH == "" {
				reqH = "*"
			}
			h.Set("Access-Control-Allow-Headers", reqH)
			h.Set("Access-Control-Expose-Headers", "*")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next(w, r)
	}
}

func logged(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next(w, r)
		fmt.Printf("%s %s %s %dms\n", r.Method, r.URL.Path, r.URL.RawQuery, time.Since(start).Milliseconds())
	}
}

func handle(mux *http.ServeMux, pat string, h http.HandlerFunc) {
	mux.HandleFunc(pat, logged(cors(h)))
}

func main() {
	port := env("PORT", "8080")
	serveUI := env("SERVE_UI", "true") != "false"
	enableCORS = env("ENABLE_CORS", "false") == "true"

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-App", appHeader)
		_, _ = io.WriteString(w, "ok")
	})
	handle(mux, "/api/ping", ping)
	handle(mux, "/api/delay", delay)
	handle(mux, "/api/echo", echo)
	handle(mux, "/api/headers", headers)
	handle(mux, "/api/whoami", whoami)
	handle(mux, "/api/path/", pathEcho) // subtree
	handle(mux, "/api/status", status)
	handle(mux, "/api/size", size)
	handle(mux, "/api/auth", auth)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if !serveUI {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": true, "service": appHeader, "ui": "disabled (SERVE_UI=false)",
					"endpoints": []string{"/api/ping", "/api/delay?ms=", "/api/echo", "/api/headers",
						"/api/whoami", "/api/path/...", "/api/status?code=", "/api/size?bytes=", "/api/auth"},
				})
				return
			}
			b, _ := ui.ReadFile("index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("X-App", appHeader)
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	})

	fmt.Printf("haproxy-annotation-tester :%s ui=%v cors=%v host=%s\n", port, serveUI, enableCORS, hostname())
	if err := (&http.Server{Addr: ":" + port, Handler: mux}).ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
