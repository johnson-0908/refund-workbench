"""Static contract checks only; does not build images or contact business services."""
import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class WebContainerContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dockerfile = (ROOT / "Dockerfile.web").read_text(encoding="utf-8-sig")
        cls.nginx = (ROOT / "deploy/nginx.conf").read_text(encoding="utf-8-sig")
        cls.ignore = (ROOT / ".dockerignore").read_text(encoding="utf-8-sig")
        cls.lock = json.loads((ROOT / "deploy/web-image-lock.json").read_text(encoding="utf-8-sig"))

    def test_official_bases_are_tag_and_digest_pinned(self):
        for name in ("node", "nginx"):
            image = self.lock[name]
            self.assertRegex(image["manifest_digest"], r"^sha256:[a-f0-9]{64}$")
            self.assertIn(f'FROM {image["image"]}@{image["manifest_digest"]}', self.dockerfile)
        self.assertTrue(self.lock["node"]["image"].startswith("node:24."))
        self.assertEqual("stable", self.lock["nginx"]["channel"])

    def test_locked_frontend_build_and_minimal_nonroot_runtime(self):
        self.assertIn("COPY web/package.json web/package-lock.json ./", self.dockerfile)
        self.assertIn("RUN npm ci --no-audit --no-fund", self.dockerfile)
        self.assertIn("RUN npm run build", self.dockerfile)
        runtime = self.dockerfile.split(" AS runtime", 1)[1]
        self.assertIn("COPY --from=build /app/dist/ /usr/share/nginx/html/", runtime)
        self.assertNotRegex(runtime, r"(?m)^COPY\s+web/(?:src|node_modules)")
        self.assertIn("USER 101:101", runtime)
        self.assertIn("EXPOSE 8080", runtime)
        self.assertIn('ENTRYPOINT ["nginx"]', runtime)
        self.assertIn("http://127.0.0.1:8080/healthz", runtime)

    def test_readonly_compatible_paths_and_healthcheck(self):
        self.assertIn("pid /tmp/nginx.pid;", self.nginx)
        for name in ("client_body", "proxy", "fastcgi", "uwsgi", "scgi"):
            self.assertRegex(self.nginx, rf"{name}_temp_path /tmp/[^;]+;")
        self.assertIn("listen 8080 default_server;", self.nginx)
        self.assertIn("location = /healthz", self.nginx)
        self.assertIn('return 200 "ok\\n";', self.nginx)

    def test_proxy_preserves_paths_and_never_retries_or_reflects_origin(self):
        self.assertIn("location ^~ /api/", self.nginx)
        self.assertIn("set $backend_service http://backend:18080;", self.nginx)
        self.assertIn("proxy_pass $backend_service$request_uri;", self.nginx)
        self.assertIn("proxy_set_header Origin $http_origin;", self.nginx)
        self.assertIn("proxy_next_upstream off;", self.nginx)
        self.assertIn("proxy_intercept_errors off;", self.nginx)
        self.assertIn("proxy_cache off;", self.nginx)
        self.assertNotIn("Access-Control-Allow-Origin", self.nginx)
        self.assertNotIn("proxy_pass $http_", self.nginx)

    def test_security_headers_and_selective_asset_caching(self):
        self.assertIn("server_tokens off;", self.nginx)
        for header in ("X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy"):
            self.assertRegex(self.nginx, rf"add_header {header} [^\n]+ always;")
        for directive in ("default-src 'self'", "script-src 'self'", "object-src 'none'", "base-uri 'self'", "form-action 'self'", "frame-ancestors 'none'"):
            self.assertIn(directive, self.nginx)
        self.assertNotIn("'unsafe-eval'", self.nginx)
        self.assertEqual(1, len(re.findall(r"add_header Cache-Control", self.nginx)))
        self.assertIn('default "no-store";', self.nginx)
        self.assertIn("max-age=31536000, immutable", self.nginx)
        self.assertIn(":(?:200|304)$", self.nginx)
        self.assertIn("try_files $uri =404;", self.nginx)
        self.assertIn("try_files $uri $uri/ /index.html;", self.nginx)

    def test_compression_applies_only_to_public_static_assets(self):
        locations = re.findall(r"location ([^{}]+)\{([^{}]+)\}", self.nginx)
        compressed = [(name.strip(), body) for name, body in locations if "gzip on;" in body]
        self.assertEqual(["/assets/"], [name for name, _ in compressed])
        self.assertIn("gzip_vary on;", compressed[0][1])
        self.assertIn("application/javascript", compressed[0][1])
        self.assertIn("text/css", compressed[0][1])

    def test_context_is_a_closed_allowlist_with_sensitive_paths_excluded(self):
        rules = [line.strip() for line in self.ignore.splitlines() if line.strip() and not line.startswith("#")]
        self.assertEqual("**", rules[0])
        allowed = [line for line in rules if line.startswith("!")]
        self.assertIn("!web/package-lock.json", allowed)
        self.assertIn("!backend/internal/**/*.go", allowed)
        self.assertIn("!deploy/nginx.conf", allowed)
        self.assertNotIn("!web/**", allowed)
        self.assertNotIn("!backend/**", allowed)
        self.assertNotIn("!deploy/**", allowed)
        last_allow = max(index for index, rule in enumerate(rules) if rule.startswith("!"))
        for rule in ("**/.env", "**/.env.*", "**/.git/**", "**/.runtime/**", "**/evidence/**", "**/private/**", "**/node_modules/**", "**/archives/**", "**/bin/**", "**/*.sql"):
            self.assertGreater(rules.index(rule), last_allow)


if __name__ == "__main__":
    unittest.main(verbosity=2)
