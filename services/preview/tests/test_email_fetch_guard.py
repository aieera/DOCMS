"""M-21 — the email HTML renderer never fetches remote resources.

WeasyPrint's default url_fetcher follows every <img src>, stylesheet and
@import in uploaded email HTML: a blind GET SSRF from the preview worker
(10 s per fetch) reachable by uploading an .eml. Only inline data: URIs
(and cid: references, which resolve to nothing) are allowed now.
"""
from __future__ import annotations

import base64
import http.server
import io
import os
import threading

import pytest

pytest.importorskip("weasyprint")

from app.processors import email as email_proc  # noqa: E402


@pytest.fixture()
def canary():
    hits: list[str] = []

    class H(http.server.BaseHTTPRequestHandler):
        def do_GET(self):  # noqa: N802
            hits.append(self.path)
            self.send_response(200)
            self.send_header("Content-Type", "text/css")
            self.end_headers()
            self.wfile.write(b"body{color:red}")

        def log_message(self, *a):
            pass

    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    yield f"http://127.0.0.1:{srv.server_address[1]}", hits
    srv.shutdown()


def _png_data_uri() -> str:
    from PIL import Image
    buf = io.BytesIO()
    Image.new("RGB", (4, 4), (255, 0, 0)).save(buf, "PNG")
    return "data:image/png;base64," + base64.b64encode(buf.getvalue()).decode()


def test_email_render_blocks_remote_fetch(canary, workdir):
    base, hits = canary
    html = f"""<html><head>
      <link rel="stylesheet" href="{base}/sheet.css">
      <style>@import url("{base}/import.css");
             body {{ background: url("{base}/bg.png"); }}</style>
    </head><body>
      <h1>Quarterly</h1>
      <img src="{base}/pixel.png">
      <img src="file:///etc/passwd">
      <img src="cid:logo@example.com">
      <img src="{_png_data_uri()}">
    </body></html>"""
    dest = os.path.join(workdir, "email.pdf")

    rendered = email_proc._render_html_pdf(html, "Quarterly", dest)
    assert hits == [], f"renderer fetched remote resources: {hits}"
    if _weasyprint_renders(workdir):
        # Blocked resources are skipped, not fatal: the body still renders.
        assert rendered is True
        assert os.path.getsize(dest) > 0


def _weasyprint_renders(workdir) -> bool:
    """weasyprint 61 + pydyf >= 0.11 cannot write any PDF ("PDF.__init__()
    takes 1 positional argument"); M-25 moves to weasyprint >= 62. Only
    assert a successful render where the library itself works."""
    from weasyprint import HTML
    try:
        HTML(string="<p>x</p>").write_pdf(os.path.join(workdir, "probe.pdf"))
        return True
    except TypeError:
        return False


def test_email_fetcher_allows_only_data_and_cid():
    fetch = email_proc._safe_url_fetcher()
    assert fetch(_png_data_uri()).read()
    assert fetch("cid:part1@example.com").read() == b""
    for url in ("http://169.254.169.254/latest/meta-data/", "https://example.com/x.css",
                "file:///etc/passwd", "ftp://example.com/a"):
        with pytest.raises(ValueError):
            fetch(url)
