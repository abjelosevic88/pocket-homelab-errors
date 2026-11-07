package server

import (
	"net/http"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/intake"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// errorsJS is the small browser script for a site without a Sentry SDK:
//
//	<script src="https://errors.example.com/errors.js" data-dsn="https://KEY@errors.example.com/1"
//	        data-release="1.2.0" data-environment="production"></script>
//
// It sends uncaught errors and unhandled promise rejections with the last
// clicks, page changes and console errors before them (20 a page at most,
// the same one once), and offers PocketErrors.capture(error) and
// PocketErrors.message(text, level) to send one yourself.
const errorsJS = `(function () {
  var s = document.currentScript, dsn = s && s.getAttribute('data-dsn');
  var m = dsn && /^(https?):\/\/([^@:]+)(?::[^@]*)?@([^/]+)(.*)\/(\d+)\/?$/.exec(dsn);
  if (!m) { if (window.console) console.warn('Pocket Errors: set data-dsn on the script tag'); return; }
  var url = m[1] + '://' + m[3] + m[4] + '/api/' + m[5] + '/store/?sentry_key=' + m[2] + '&sentry_version=7';
  var release = s.getAttribute('data-release') || '', env = s.getAttribute('data-environment') || '';
  var crumbs = [], sent = 0, seen = {};
  function crumb(c) { c.timestamp = Date.now() / 1000; crumbs.push(c); if (crumbs.length > 30) crumbs.shift(); }
  function describe(t) {
    var d = t.tagName.toLowerCase();
    if (t.id) d += '#' + t.id;
    if (typeof t.className === 'string' && t.className) d += '.' + t.className.trim().split(/\s+/).slice(0, 2).join('.');
    var text = (t.innerText || t.value || '').trim().slice(0, 40);
    return text ? d + ' "' + text + '"' : d;
  }
  document.addEventListener('click', function (e) { if (e.target && e.target.tagName) crumb({ category: 'ui.click', message: describe(e.target) }); }, true);
  var push = history.pushState;
  history.pushState = function () { var from = location.pathname; var r = push.apply(this, arguments); crumb({ category: 'navigation', data: { from: from, to: location.pathname } }); return r; };
  window.addEventListener('popstate', function () { crumb({ category: 'navigation', data: { to: location.pathname } }); });
  if (window.console && console.error) {
    var ce = console.error;
    console.error = function () { try { crumb({ category: 'console', level: 'error', message: [].map.call(arguments, String).join(' ').slice(0, 300) }); } catch (x) {} return ce.apply(console, arguments); };
  }
  function id() { var h = ''; for (var i = 0; i < 32; i++) h += (Math.random() * 16 | 0).toString(16); return h; }
  function send(err, msg, how, level, extra) {
    if (sent >= 20) return;
    var stack = (err && err.stack) || '', key = (msg || (err && err.message) || '') + stack.slice(0, 300);
    if (seen[key]) return; seen[key] = 1; sent++;
    var ev = { event_id: id(), timestamp: Date.now() / 1000, platform: 'javascript', level: level || 'error', release: release, environment: env,
      type: (err && err.name) || '', value: (err && err.message) || '', stack: stack, message: err ? '' : msg,
      request: { url: location.href, headers: { 'User-Agent': navigator.userAgent, 'Referer': document.referrer } },
      breadcrumbs: crumbs.slice(), tags: { mechanism: how, language: navigator.language }, extra: extra || undefined,
      contexts: { browser: { name: navigator.userAgent, viewport: innerWidth + 'x' + innerHeight } } };
    try { fetch(url, { method: 'POST', body: JSON.stringify(ev), keepalive: true, mode: 'cors', headers: { 'Content-Type': 'text/plain;charset=UTF-8' } }); } catch (x) {}
  }
  window.addEventListener('error', function (e) {
    if (e.error) send(e.error, e.message, 'onerror');
    else if (e.message) send(null, e.message + (e.filename ? ' (' + e.filename + ':' + e.lineno + ')' : ''), 'onerror');
  });
  window.addEventListener('unhandledrejection', function (e) {
    var r = e.reason;
    if (r instanceof Error) send(r, '', 'onunhandledrejection');
    else send(null, 'Unhandled promise rejection: ' + (typeof r === 'string' ? r : JSON.stringify(r)), 'onunhandledrejection');
  });
  window.PocketErrors = {
    capture: function (err, extra) { send(err instanceof Error ? err : null, err instanceof Error ? '' : String(err), 'manual', 'error', extra); },
    message: function (text, level, extra) { send(null, String(text), 'manual', level || 'info', extra); }
  };
})();
`

func (s *Server) handleErrorsJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write([]byte(errorsJS))
}

// testEvent is a made-up error in the project's language, to see one arrive.
func testEvent(p *store.Project) *intake.Event {
	stack := map[string]string{
		"go": "panic: this is a test error from Pocket Errors\n\ngoroutine 1 [running]:\nmain.checkout(...)\n\t/app/cart.go:42 +0x1d\nmain.main()\n\t/app/main.go:17 +0x25\n",
		"python": "Traceback (most recent call last):\n  File \"/app/main.py\", line 17, in <module>\n    checkout(cart)\n  File \"/app/cart.py\", line 42, in checkout\n" +
			"    raise ValueError(\"this is a test error from Pocket Errors\")\nValueError: this is a test error from Pocket Errors",
	}[p.Platform]
	if stack == "" {
		stack = "Error: this is a test error from Pocket Errors\n    at checkout (https://example.com/assets/cart.js:42:13)\n    at HTMLButtonElement.onClick (https://example.com/assets/app.js:17:5)"
	}
	m := map[string]any{"stack": stack, "level": "error", "environment": "test", "tags": map[string]any{"test": "yes"},
		"fingerprint": []any{"pocket-errors-test"}}
	ev := intake.FromMap(m)
	ev.At = time.Now()
	return ev
}
