# Reverse proxy

Set `BASE_URL` to the public address and `SECURE_COOKIES=true` behind HTTPS. See [deploy/reverse-proxy/Caddyfile](../deploy/reverse-proxy/Caddyfile). Sentry SDKs post to `/api/{project}/store/` and `/api/{project}/envelope/`: those paths must reach the app unauthenticated (the project key is the credential) and, for websites, from the visitors' browsers — CORS is answered by the app. `/api/v1/*` is reachable for Homepage widgets, scripts and linked apps; they send their own bearer token.
