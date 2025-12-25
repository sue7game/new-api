# Repository Guidelines

## Project Structure & Module Organization
- Backend Go code sits at the repo root: `main.go` is the entry; routing flows through `router/`, `controller/`, and `service/`, with shared helpers in `common/`, `constant/`, `types/`, `middleware/`, `logger/`, and `setting/`; data models live in `model/`, request DTOs in `dto/`, and provider relays in `relay/`.
- Frontend is a React/Vite app under `web/` (`src/` for UI, `public/` for static assets). Docs and extra assets live in `docs/` and `web/public/`.
- Ops assets: `docker-compose.yml`, `Dockerfile`, `makefile`, `new-api.service`; runtime artifacts like `logs/`, `one-api.db`, and SQL dumps should not be edited or committed.

## Build, Test, and Development Commands
- Install deps: `go mod download`; `cd web && bun install` (use Bun).
- Local dev: `go run main.go` starts the API on :3000; `cd web && bun run dev -- --host` runs the Vite dev server.
- Bundles: `make build-frontend` builds the UI with `VITE_REACT_APP_VERSION=$(cat VERSION)`; `make start-backend` serves Go with built assets; `docker-compose up -d` brings up the stack.
- Checks: `go vet ./...` and `go test ./...` for backend; `cd web && bun run lint` or `bun run eslint` for UI.

## Coding Style & Naming Conventions
- Go: format with `gofmt`/`goimports`; exported identifiers PascalCase, locals camelCase; keep handlers thin and move logic to `service/`; wrap errors with context.
- Frontend: Prettier uses single quotes; components PascalCase, hooks `useX`, route folders kebab-case; favor functional components and typed props (TS or JSDoc).
- Config: load secrets via env or `.env`; align new vars with `docker-compose.yml`/`setting/`; never commit secrets or local DBs.

## Testing Guidelines
- Add Go unit tests alongside code using table-driven `*_test.go`; stub external calls in `service/` or `relay/`.
- UI tests are optional; if added, use Vitest + React Testing Library in `web/src/__tests__/` or next to components.
- Run `go test -cover ./...` and UI linters before PRs; note manual verification for auth, billing, or provider flows.

## Commit & Pull Request Guidelines
- Use Conventional Commit style from history: `feat(scope): ...`, `fix: ...`, `chore: ...`; keep messages imperative (English or concise Chinese).
- PRs should include a short summary, linked issue, screenshots/GIFs for `web/` changes, required migrations or env vars, and the commands you ran; keep scope focused.
