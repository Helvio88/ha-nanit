# Nanit HA Integration
# Show available commands
default:
    @just --list

# Deploy integration to HA via SSH
deploy:
    tar cf - -C custom_components nanit | ssh homeassistant "tar xf - -C /config/custom_components/"

# Deploy + reload integration (no HA restart needed)
reload: deploy
    #!/usr/bin/env bash
    set -euo pipefail
    entry_id=$(ssh homeassistant 'curl -sf http://supervisor/core/api/config/config_entries/entry -H "Authorization: Bearer ${SUPERVISOR_TOKEN}"' \
      | python3 -c "import sys,json; entries=json.load(sys.stdin); ids=[e[\"entry_id\"] for e in entries if e[\"domain\"]==\"nanit\"]; print(ids[0]) if ids else exit(1)")
    if [[ -z "$entry_id" ]]; then
        echo "Error: Nanit integration not found in HA"
        exit 1
    fi
    ssh homeassistant "curl -sf -X POST http://supervisor/core/api/config/config_entries/entry/${entry_id}/reload -H \"Authorization: Bearer \${SUPERVISOR_TOKEN}\"" > /dev/null
    echo "Reloaded nanit integration"

# Create a GitHub release by bumping the latest version.
# Usage: just release <patch|minor|major>
# Example: just release patch  (0.2.1 → 0.2.2)
release bump:
    #!/usr/bin/env bash
    set -euo pipefail
    # Get latest version tag, strip 'v' prefix
    latest=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    latest="${latest#v}"
    IFS='.' read -r major minor patch <<< "${latest}"
    case "{{bump}}" in
        patch) patch=$((patch + 1)) ;;
        minor) minor=$((minor + 1)); patch=0 ;;
        major) major=$((major + 1)); minor=0; patch=0 ;;
        *) echo "Error: Invalid bump type '{{bump}}'. Use patch, minor, or major."; exit 1 ;;
    esac
    new="${major}.${minor}.${patch}"
    tag="v${new}"
    echo "Bumping: ${latest} → ${new}"
    # Update manifest.json
    sed -i '' 's/"version": "[0-9]*\.[0-9]*\.[0-9]*"/"version": "'"${new}"'"/' custom_components/nanit/manifest.json
    # Update addon config.yaml
    sed -i '' 's/^version: [0-9]*\.[0-9]*\.[0-9]*/version: '"${new}"'/' nanitd/config.yaml
    git add custom_components/nanit/manifest.json nanitd/config.yaml
    git commit -m "Bump version to ${new}"
    git tag "${tag}"
    git push && git push --tags
    gh release create "${tag}" --title "${tag}" --generate-notes
    echo "Released ${tag}"
