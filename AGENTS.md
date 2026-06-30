# AGENTS.md — AI Agent Instructions

## Build Artifacts Policy (CRITICAL)

- **DO NOT** build or compile `.exe` files. The project uses `go run` or the user runs builds manually when needed.
- **DO NOT** commit any build outputs (`.exe`, `.dll`, `.so`, `.o`, `.a`, `.out`, `.test`). These are gitignored — if you create one, delete it immediately.
- **DO NOT** run `go build`, `go install`, or any command that produces an executable binary unless the user explicitly asks you to.
- If the user asks you to test or verify behavior, prefer code review, static analysis, or `go vet` / `go test` over building and running.

## Project Overview

- **Language:** Go 1.24
- **Module:** `screen_server`
- **Purpose:** Screen capture & streaming server using WebRTC + FFmpeg
- **Frontend:** `frontend/` directory (Node.js / TypeScript)
- **Entry point:** `cmd/` directory

## Development Guidelines

### Go
- Use `go run ./cmd/...` to run the server (only when user explicitly requests).
- Use `go vet ./...` for static analysis.
- Use `go test ./...` for tests (if any exist).
- Go toolchain is at `D:/Software/Go/go/bin` — prepend to PATH when needed:
  ```bash
  export PATH="D:/Software/Go/go/bin:$PATH"
  ```

### Frontend
```bash
cd frontend && npm install && npm run build   # only when user asks
```
- `frontend/node_modules/` and `frontend/dist/` are gitignored.

### Git
- Always commit with clear, conventional commit messages.
- Never commit build artifacts — `.gitignore` covers common patterns but stay vigilant.
- If you accidentally create a build artifact, delete it and do NOT stage it.
