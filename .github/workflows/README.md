# GitHub Actions Setup

This repository uses GitHub Actions for CI/CD. Here's what you need to configure:

## Required Secrets

Add these secrets to your GitHub repository settings:

### Quay.io Container Registry
- `QUAY_USERNAME` - Your Quay.io username
- `QUAY_PASSWORD` - Your Quay.io password or app token


## Workflows

### `ci.yml` - Continuous Integration
Runs on every push and pull request:
- **Test**: Runs Go tests with race detection and coverage
- **Build**: Builds multi-arch container images (amd64, arm64)
- **Security**: Scans images with Trivy for vulnerabilities

### `release.yml` - Release Automation
Runs when you create a git tag:
- Builds binaries for multiple platforms
- Creates container images with semantic version tags
- Generates GitHub releases with release notes
- Uploads compiled binaries as release artifacts

## Creating a Release

1. Tag your commit with a semantic version:
   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```

2. The release workflow will automatically:
   - Build binaries for Linux, macOS, Windows
   - Create container images tagged with the version
   - Generate a GitHub release with release notes

## Container Image Tags

Images are pushed to `quay.io/philipgough/kube-resource-sync` with these tags:
- `latest` - Latest main branch build
- `v1.0.0` - Specific version tag
- `1.0` - Major.minor version
- `1` - Major version
- `main-abc123` - Branch name with commit SHA

## Security

- Trivy scans all built images for vulnerabilities
- Results are uploaded to GitHub Security tab
- Multi-arch builds ensure compatibility across platforms
- Distroless base images minimize attack surface