# portfolio-site

A photography portfolio built with Go, HTML, CSS, and vanilla JavaScript. A minimal public gallery presents the photographer's name and photographs organized into categories. A separate owner dashboard manages the content without editing source files.

The project is reusable: no photographer's identity is hardcoded. Set your display name in the dashboard and replace the included demonstration images with your own work. The interface currently uses Russian text; this documentation is in English.

## Features

### Public portfolio

- Black background with a full-screen photographic cover.
- Cover slideshow with manual navigation, pause control, and reduced-motion support.
- Category navigation and square category covers.
- Filtered photo galleries and a full-screen image viewer.
- Keyboard navigation and touch swipes in the image viewer.
- Responsive layouts for desktop and mobile.
- Self-hosted Alice and Montserrat fonts.

The public site contains the photographer's name and photographs. It does not include contact or biography sections.

### Owner dashboard

- Password-protected access at `/studio`.
- Upload multiple JPEG or PNG files.
- Rename, reorder, move, and delete photographs.
- Choose the main cover photograph.
- Create, rename, reorder, and delete empty categories.
- Update the photographer's display name.

The dashboard is not linked from the public navigation. Access is enforced by the server; knowing its URL does not grant permission to manage content. A single owner is supported, with no public registration.

## Technology

| Component | Implementation |
| --- | --- |
| Server | Go 1.25+, standard library |
| Pages | Server-rendered `html/template` templates |
| Frontend | HTML, CSS, vanilla JavaScript |
| Portfolio data | JSON file with locking and atomic replacement |
| Uploaded photographs | Local filesystem with generated JPEG previews |
| Deployment | Standalone binary or Docker Compose |

There are no third-party Go dependencies. Templates and static assets are embedded into the binary. Runtime data is stored separately on disk.

## Requirements

- [Go 1.25 or newer](https://go.dev/dl/) for local development.
- A writable, persistent data directory.
- Docker and Docker Compose if using the container deployment.

Run commands from the project root, where `go.mod` is located.

## Quick start

### macOS and Linux

Build the server:

```bash
go build -buildvcs=false -o portfolio-site .
```

Create the owner's password using **Bash**. If your terminal uses zsh, run `bash` first. The password is read without displaying it or passing it as a command-line argument.

```bash
read -r -s -p 'Owner password: ' portfolio_password
printf '\n'
printf '%s' "$portfolio_password" | ./portfolio-site set-password
unset portfolio_password
```

Use a password of at least 12 characters. There is no default password: owner login remains unavailable until a password has been configured.

Start the server:

```bash
./portfolio-site
```

Open:

- Portfolio: <http://localhost:8080>
- Owner dashboard: <http://localhost:8080/studio>

Stop the server with `Ctrl+C`. Content persists across restarts.

### Windows PowerShell

```powershell
go build -buildvcs=false -o portfolio-site.exe .

$portfolioSecret = Read-Host 'Owner password' -AsSecureString
$portfolioPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($portfolioSecret)
try {
    [Runtime.InteropServices.Marshal]::PtrToStringBSTR($portfolioPointer) |
        .\portfolio-site.exe set-password
} finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($portfolioPointer)
    Remove-Variable portfolioSecret,portfolioPointer
}

.\portfolio-site.exe
```

Use the same local URLs listed above. A native executable contains the embedded site assets, so the `web` directory is not required beside the built binary at runtime.

### GoLand

Open the project root in GoLand and configure Go 1.25 or newer. Set the owner password using the terminal instructions above, then run `main.go`.

Set the run configuration's working directory to the project root so that local development uses the same `data` directory. Environment variables can be supplied in the run configuration.

## Access from another device

The server binds to `127.0.0.1:8080` by default and is accessible only from the computer running it. To access it on your local network:

```bash
ADDR=0.0.0.0:8080 ./portfolio-site
```

Find your computer's local IP address. On a Mac connected through `en0`:

```bash
ipconfig getifaddr en0
```

On another device connected to the same network, open `http://<local-ip>:8080`. Use `/studio` for the dashboard. Allow the server's incoming connections in your firewall if prompted.

`localhost` on a phone refers to the phone itself, not your development computer. Local HTTP access is intended for testing; use HTTPS for internet deployment.

If `PUBLIC_URL` is set, it must match the origin used to access the dashboard. An origin mismatch causes owner form submissions to be rejected.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `127.0.0.1:8080` | Server bind address |
| `DATA_DIR` | `data` | Directory for portfolio data, password hash, and uploads |
| `PUBLIC_URL` | Unset | External origin used for request validation and secure cookies |

Example production origin:

```dotenv
PUBLIC_URL=https://portfolio.example.com
```

Use a scheme and host, including a port if needed, without a path. Set the exact HTTPS origin in production. With HTTPS configured, session cookies use the `Secure` flag.

When `PUBLIC_URL` is unset, request validation uses the request's scheme and host. Set it explicitly when running behind an HTTPS reverse proxy.

## Docker deployment

The included Compose configuration publishes port 8080 only on the host's loopback interface. Use a reverse proxy on the host for public access.

Create a `.env` file with your external origin, then build:

```bash
docker compose build
```

Set the owner password using Bash:

```bash
read -r -s -p 'Owner password: ' portfolio_password
printf '\n'
printf '%s' "$portfolio_password" |
  docker compose run --rm -T portfolio-site set-password
unset portfolio_password
```

Start the service:

```bash
docker compose up -d
```

For a local Docker check, the default origin is `http://localhost:8080`. For a public deployment, point your domain to the server and configure HTTPS. A Caddy example is included in `Caddyfile.example`:

```caddy
portfolio.example.com {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080
}
```

Replace the example domain with your own. Caddy runs on the host in this setup.

Uploaded files and settings are stored in a persistent Docker volume. Updating the application does not require deleting that volume:

```bash
git pull --ff-only
docker compose up -d --build
```

Do not run `docker compose down -v` unless you intend to delete the stored portfolio data.

## Content and storage

On first startup, the application initializes sample categories, nature photographs, and a generic display name. Open `/studio` to replace them with your own content.

Existing data takes precedence over the starter content. Updating the code does not reset a previously configured photographer's name or photographs.

| Path | Contents |
| --- | --- |
| `data/portfolio.json` | Categories, photo metadata, and settings |
| `data/password.json` | Password salt and derived hash |
| `data/uploads/` | Processed photographs and thumbnails |

The paths above use the default `DATA_DIR`. In Docker, the data directory is `/data` inside the container.

Use one server process per data directory. File locking is local to the process; this version is not designed for multiple replicas sharing the same files.

To back up a native deployment, stop the server and copy the entire data directory. For Docker, stop the service and back up its data volume. Keep backups of your original photographs separately.

### Upload limits

- JPEG and PNG input.
- Up to 12 files per request.
- Up to 15 MiB per file and 64 MiB per request, including multipart overhead.
- Up to 30 million pixels per image.
- Generated full-view JPEGs up to 2400 pixels on their longest side.
- Generated thumbnails up to 720 pixels on their longest side.

Images are decoded and re-encoded. Embedded metadata is not retained, and the source files are not stored as originals. Convert HEIC or WebP files to JPEG or PNG before uploading. Export images with the correct pixel orientation: EXIF rotation is not applied.

Published photographs are publicly viewable. The dashboard protects editing operations, not access to portfolio images.

## Authentication and security

- Salted PBKDF2-SHA256 password hashing with 600,000 iterations.
- Random server-side sessions with a 12-hour expiry.
- `HttpOnly` and `SameSite=Strict` cookies, plus `Secure` when HTTPS is configured.
- CSRF tokens and origin validation for login and content changes.
- Rate limiting of failed login attempts.
- Template escaping, Content Security Policy, and frame restrictions.
- Image validation, re-encoding, and upload size limits.
- Owner pages excluded from caching.

Sessions are held in memory and do not survive server restarts. To change the password, run `set-password` again and restart the server. In Docker, restart with:

```bash
docker compose restart portfolio-site
```

The `.gitignore` and `.dockerignore` files exclude runtime data, local binaries, and `.env`. Keep credentials and uploaded private source material out of version control.

## Static design preview

Export the public design without running a server:

```bash
go run . export-preview dist
```

This creates `dist/index.html` and static assets. It exports the bundled starter gallery, not the current content from `DATA_DIR`. It does not include a working owner dashboard or synchronize uploaded photographs.

Serve the export from the root of a static host. Asset URLs start with `/static/`; hosting under a repository subpath requires adjusting those paths in the HTML and CSS.

GitHub Pages can host this static preview, but cannot run the Go server. The full application needs a host that runs Go or Docker and provides persistent storage. Deploying the current application on an ephemeral filesystem will lose content after a restart or redeployment. External storage integration is not included in this version.

## Development checks

```bash
go test -race ./...
go vet ./...
```

The integration tests cover authorization, CSRF and origin checks, category persistence, upload and deletion, cover selection, login throttling, session expiry, logout, template escaping, and invalid image rejection.

## Project structure

| Path | Purpose |
| --- | --- |
| `main.go` | HTTP server, authentication, persistence, and owner operations |
| `main_test.go` | Integration tests |
| `web/templates/` | Public portfolio, login, and dashboard templates |
| `web/static/` | CSS, JavaScript, fonts, icon, and demonstration photographs |
| `Dockerfile` | Container build |
| `compose.yaml` | Local or VPS container deployment |
| `Caddyfile.example` | HTTPS reverse proxy example |
| `ASSETS.txt` | Third-party asset sources |

## Assets

The bundled nature photographs are demonstration images from Unsplash, not the site owner's work. Replace them before publishing a personal portfolio. Sources are listed in `ASSETS.txt`.

Alice and Montserrat are distributed with their SIL Open Font License files in `web/static/fonts/`. Their asset licenses do not grant a license to this project's application source code.
