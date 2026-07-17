# Web UX Design: bazelmop Report Viewer

This document specifies the design for the offline `bazelmop` web dashboard. The objective is to provide a clean, self-contained, GitHub-style Markdown report viewer served directly by the `bazelmop daemon` process, with zero external network dependencies.

---

## 1. Look & Feel (GitHub Aesthetics)
To align with the user's preference, the web dashboard will mimic the clean, minimal aesthetic of a GitHub repository Markdown file view:
* **Color Palette**: GitHub Dark theme:
  * Background: `#0d1117`
  * Card Container Background: `#161b22`
  * Text Color: `#c9d1d9`
  * Border Colors: `#30363d`
  * Table Header Background: `#1f242c`
* **Typography**: Clean sans-serif stack:
  * Font Family: `-apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif`
* **Layout**:
  * A centered single-column layout (max-width `1012px`) resembling a GitHub README file container.
  * A file header bar showing `report.md`, last updated timestamp, and status.
  * Markdown tables rendered with borders, alignment, and zebra striping.

---

## 2. Technical Stack (100% Offline)
* **Frontend**: Vanilla HTML/CSS/JS.
* **Markdown Parser**: Pre-bundled minified `marked.min.js` file (~15KB) loaded locally.
* **Go Embed**: Both the HTML shell and the JS library are embedded into the Go binary using `go:embed`:
  ```go
  //go:embed assets/index.html assets/marked.min.js
  var assetsFS embed.FS
  ```

---

## 3. Licensing & Third-Party Code
Since `marked.min.js` is checked into our repository, we comply with its license terms:
* **Marked License**: MIT License.
* **Compliance**: We include the full license text in `pkg/web/assets/marked.LICENSE` and keep the copyright header inside `marked.min.js`.
* **License Notice Section**: Added to the main project `LICENSE` file under a "Third-Party Component Licenses" appendix.

---

## 4. API Endpoints
The embedded web server (running on `localhost:8080` by default) exposes:
1. `GET /`: Serves the HTML container shell.
2. `GET /assets/marked.min.js`: Serves the minified JS library with `application/javascript` content type.
3. `GET /api/report`: Returns the JSON payload:
   ```json
   {
     "report": "# Bazel Cache...",
     "updated_at": "2026-07-16T20:00:00Z"
   }
   ```
