# Nanit HA Integration
# Show available commands
default:
    @just --list
# Create a GitHub release by bumping from a base version.
# Usage: just release <patch|minor|major> <base-version>
# Example: just release patch 0.2.0  →  0.2.1
release bump base:
    #!/usr/bin/env bash
    set -euo pipefail
    IFS='.' read -r major minor patch <<< "{{base}}"
    if [[ -z "${major:-}" || -z "${minor:-}" || -z "${patch:-}" ]]; then
        echo "Error: Invalid version '{{base}}'. Expected format: X.Y.Z"
        exit 1
    fi
    case "{{bump}}" in
        patch) patch=$((patch + 1)) ;;
        minor) minor=$((minor + 1)); patch=0 ;;
        major) major=$((major + 1)); minor=0; patch=0 ;;
        *) echo "Error: Invalid bump type '{{bump}}'. Use patch, minor, or major."; exit 1 ;;
    esac
    new="${major}.${minor}.${patch}"
    tag="v${new}"
    echo "Bumping: {{base}} → ${new}"
    # Update manifest.json
    sed -i '' 's/"version": "[0-9]*\.[0-9]*\.[0-9]*"/"version": "'"${new}"'"/' custom_components/nanit/manifest.json
    # Update addon config.yaml
    sed -i '' 's/^version: [0-9]*\.[0-9]*\.[0-9]*/version: '"${new}"'/' nanitd/config.yaml
    git add custom_components/nanit/manifest.json nanitd/config.yaml
    git commit -m "Bump version to ${new}"
    git push
    gh release create "${tag}" --title "${tag}" --generate-notes
    echo "Released ${tag}"
