# Release uah on Homebrew and AUR

Pushing a uah tag builds the GitHub release archives. Homebrew and AUR each need a separate packaging commit **after the archives are published**. Use the same release version for both. Commands below use Bash.

## 1. Get the checksums from the release

```bash
version=1.17.1  # replace with the version being released, without v
assets=$(mktemp -d)
gh release download "v$version" --repo viktordanov/uah --dir "$assets" \
  --pattern '*.tar.gz' --pattern '*.sha256'
(cd "$assets" && shasum -a 256 -c *.sha256)
cat "$assets"/*.sha256
```

Expect four successful checks: `aarch64-apple-darwin`, `x86_64-apple-darwin`, `aarch64-unknown-linux-gnu`, and `x86_64-unknown-linux-gnu`. Match hashes to filenames, not their order.

## 2. Update and publish the Homebrew formula

1. Open the maintainer's tap: `cd "$(brew --repository viktordanov/tap)"`. If it is missing, run `brew tap viktordanov/tap`. Check `git status --short --branch` and run `git pull --ff-only` from a clean `main`.
2. In `Formula/uah.rb`, update all four `url` entries (both the tag and filename) and the `sha256` directly below each. Review `git diff -- Formula/uah.rb`.
3. Run `brew audit --strict viktordanov/tap/uah`. If Homebrew requires explicit trust, use `brew trust --formula viktordanov/tap/uah` for this formula alone. To keep that trust temporary, set `export XDG_CONFIG_HOME="$(mktemp -d)"` in a subshell for the trust, audit, install and test commands.
4. Run `HOMEBREW_NO_AUTO_UPDATE=1 brew install viktordanov/tap/uah`, or use `brew upgrade` with the same flag and formula if it was already installed. Run `brew test viktordanov/tap/uah` and `"$(brew --prefix uah)/bin/uah" --version`. If Homebrew was installed **only for this test**, remove the test copy with `HOMEBREW_NO_AUTOREMOVE=1 brew uninstall uah`. Keep any pre-existing installation.
5. Publish from the tap checkout:

   ```bash
   git add Formula/uah.rb
   git commit -m "uah $version"
   git push origin main
   ```

## 3. Build on Arch and hand off the signed AUR push

Connect to the maintainer host and open the existing AUR checkout:

```sh
ssh minima
cd ~/Projects/Collaborative/AUR/aur-uah-bin
git status --short --branch
git pull --ff-only
```

Start from a clean `master`. In `PKGBUILD`, set `pkgver` to the new version without `v`, reset `pkgrel=1` for a new upstream version, and replace `sha256sums_x86_64` and `sha256sums_aarch64` with the matching **Linux** archive hashes from step 1. The source URLs already interpolate `pkgver`.

```sh
makepkg --printsrcinfo > .SRCINFO
makepkg --cleanbuild --clean
git add PKGBUILD .SRCINFO
git diff --cached --check
git diff --cached
```

`makepkg` verifies the host architecture's archive, runs the version and shell-completion checks, and builds `uah-bin`. The established handoff is to leave **only `PKGBUILD` and `.SRCINFO` staged** on `minima`. Report the package as *staged*, not published.

The maintainer finishes from a shell with the signing key available:

```sh
cd ~/Projects/Collaborative/AUR/aur-uah-bin
git commit -m "uah 1.17.1"  # substitute the release version
git push origin master
```

Confirm the public [AUR package](https://aur.archlinux.org/packages/uah-bin) shows the expected version (for example, `1.17.1-1`). If signing is unavailable, leave the changes staged for the maintainer; do not disable signing.
