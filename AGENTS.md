# ha-nanit — AGENTS.md

This file is a quick lookup for agents working in this repo. Read it before making changes.

## Repo overview

Home Assistant custom integration for Nanit baby cameras.
Two main parts:

- **Python integration**: `custom_components/nanit/`
- **Go add-on (daemon)**: `nanitd/`

## Architecture (high level)

```
Home Assistant (Python)  ⇄  nanitd (Go add-on)  ⇄  Nanit camera
custom_components/nanit      nanitd/src/           (local/cloud)
             │
             └── HTTPS → api.nanit.com (auth/MFA/baby metadata)
```

## Key paths

- `custom_components/nanit/manifest.json` — integration metadata + version
- `custom_components/nanit/__init__.py` — `async_setup_entry` / `async_unload_entry`
- `custom_components/nanit/config_flow.py` — UI setup + reauth/reconfigure
- `custom_components/nanit/coordinator.py` — polling via `DataUpdateCoordinator`
- `custom_components/nanit/*.py` — entities (sensor, switch, camera, event, etc.)
- `custom_components/nanit/diagnostics.py` — redaction list for credentials
- `custom_components/nanit/strings.json` + `translations/en.json` — user strings
- `nanitd/src/` — Go daemon source
- `nanitd/config.yaml` — add-on metadata + version
- `.github/workflows/build-addon.yaml` — add-on build pipeline
- `justfile` — release helper (bumps versions + creates tag)
- `README.md` — user-facing docs

## Development guidelines (Home Assistant)

- **Always use the latest Home Assistant version, API, and developer docs**
  from https://developers.home-assistant.io/.
- Minimum supported HA version: **2025.12+** (per README).
- Keep integration **fully async**; no blocking I/O in the event loop.
- Use `ConfigEntry.runtime_data` for runtime objects (clients/coordinators).
- Use the `DataUpdateCoordinator` pattern for polling.
- Avoid hardcoded English strings; use `strings.json`/translations.
- **Do not change entity unique IDs or class names** without a migration plan.
- Never log or store credentials/tokens unredacted (see `diagnostics.py`).

## Development guidelines (Go add-on)

- Keep the HTTP API contract stable (`README.md` endpoint table).
- If the API contract changes, update **both** the add-on and integration
  (and update `README.md`).
- Preserve add-on schema/ports (`nanitd/config.yaml`) unless explicitly changing
  the Supervisor contract.
- Use `gofmt` on all Go code.

## Commits & releases

- **One task/feature per commit**, with a compact description.
- If behavior or user-facing functionality changes, **update `README.md`**.
- **Release when impact is significant** (new features, breaking changes,
  substantial behavior changes). Minor internal changes can remain unreleased.

### Versioning (two files)

Version is stored in **two places** and must stay in sync:

- `custom_components/nanit/manifest.json` → `"version"`
- `nanitd/config.yaml` → `version:`

Use the helper in `justfile` (do not edit manually):

```
just release patch 0.2.2
just release minor 0.2.2
just release major 0.2.2
```

## Verification (required)

- **Verify features work** in a Home Assistant instance.
- Go daemon validation:

```
cd nanitd/src
go test ./...
go build ./...
```

- If new tests or linters are added, run them as part of the change.

## CI / automation

- Add-on builds are handled by `.github/workflows/build-addon.yaml`.
  It triggers on changes under `nanitd/**` and on GitHub releases.

## Questions before changes

- If anything is unclear, **address questions before work begins**. Do not guess.

## Optional additions to consider

- `quality_scale.yaml` (Home Assistant Integration Quality Scale tracking)
- `CONTRIBUTING.md` with dev environment + style rules
- Dev container configuration for HA + Go toolchain
