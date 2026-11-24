# Releasing germ

1. Tag and push: `git tag v0.2.0 && git push origin v0.2.0`.
2. `.github/workflows/release.yml` then:
   - tests and builds `germ` + `germ-relay` natively on Ubuntu 22.04 runners
     for x86_64 and aarch64 (cgo can't easily cross-compile GLFW);
   - publishes a GitHub release with `germ-linux-<arch>.tar.gz` and
     `SHA256SUMS` (stable asset names, so `install.sh` can use
     `releases/latest/download/...`);
   - renders `packaging/homebrew/germ.rb` from its `.in` template
     (`packaging/render.sh`);
   - pushes the formula to the `zach-monroe/homebrew-germ` tap if the
     `HOMEBREW_TAP_TOKEN` secret is set (a fine-grained token with
     contents: write on that repo);

Re-run for an existing tag from the Actions tab (workflow_dispatch).

## One-time setup

- **Homebrew**: the tap repo `zach-monroe/homebrew-germ` holds
  `Formula/germ.rb`; add the `HOMEBREW_TAP_TOKEN` secret so releases update
  it automatically.
