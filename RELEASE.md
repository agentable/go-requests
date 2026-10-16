# Coordinated Release Handoff

This repository publishes the root, browser, fingerprint, and HTTP/3 modules as
one coordinated release set.

## Release Rule

Select one next patch version from the aggregate tag history of every
publishable module. Pin each extension to the root at that version, commit the
complete release set, and create every annotated tag at that commit. Never move,
delete, recreate, or force-update an existing tag.

## Required Order

1. Upgrade and tidy the currently resolvable module graph, then run the
   complete pre-pin gate while every extension still requires the latest
   published root version:

   ```bash
   task deps:update
   GOTOOLCHAIN=go1.27.2 task verify:all
   ```

2. Commit the verified implementation, freezing every file included in the root
   module archive. Use the workspace's shared
   `scripts/release/module-checksums` tool against that canonical Git commit to
   calculate the future root version's `Sum` and `GoModSum`. The tool uses
   official `golang.org/x/mod/zip.CreateFromVCS` archive filtering and
   `dirhash.Hash1`, with no temporary source checkout or module proxy. First
   verify its calculation against a directly downloaded published root release.
   It normalizes Git's `info/attributes` with the same export-ignore and
   export-subst overrides as `cmd/go`, preserving existing metadata. This is
   necessary because this repository's distribution attributes otherwise omit
   files that Go includes in its published module archives.

   Pin every extension to the common next version and replace its old root
   checksum entries with these content-derived future checksums. Commit the
   final release metadata:

   ```bash
   for dir in browser fingerprint http3; do
     (cd "$dir" && go mod edit -require=github.com/agentable/go-requests@vX.Y.Z)
   done
   git commit
   ```

   Recalculate the root checksums against the final commit and require both
   values to equal the frozen implementation's values. Only excluded nested
   module metadata may change after calculating them. Run the workspace's
   behavior, vet, lint, race, and vulnerability checks on the final tree; leave
   tidy and standalone resolution for the published boundary below.

   Do not run `go mod tidy` after writing the unpublished common pin. Go checks
   required versions during tidy even with `go.work` active, so that boundary is
   verified on the final commit only after the atomic push makes all four tags
   resolvable.

3. Confirm every target tag is absent, then create all annotated tags at the
   final commit:

   ```bash
   git tag -a vX.Y.Z -m vX.Y.Z
   git tag -a browser/vX.Y.Z -m browser/vX.Y.Z
   git tag -a fingerprint/vX.Y.Z -m fingerprint/vX.Y.Z
   git tag -a http3/vX.Y.Z -m http3/vX.Y.Z
   ```

4. Push `main` and only the explicit new tag refspecs atomically:

   ```bash
   git push --atomic origin \
     refs/heads/main:refs/heads/main \
     refs/tags/vX.Y.Z:refs/tags/vX.Y.Z \
     refs/tags/browser/vX.Y.Z:refs/tags/browser/vX.Y.Z \
     refs/tags/fingerprint/vX.Y.Z:refs/tags/fingerprint/vX.Y.Z \
     refs/tags/http3/vX.Y.Z:refs/tags/http3/vX.Y.Z
   ```

5. Verify all published modules and rerun the complete gate on the final
   commit:

   ```bash
   GOPROXY=direct go list -m github.com/agentable/go-requests@vX.Y.Z
   GOPROXY=direct go list -m github.com/agentable/go-requests/browser@vX.Y.Z
   GOPROXY=direct go list -m github.com/agentable/go-requests/fingerprint@vX.Y.Z
   GOPROXY=direct go list -m github.com/agentable/go-requests/http3@vX.Y.Z
   GOTOOLCHAIN=go1.27.2 task verify:all
   GOTOOLCHAIN=go1.27.2 task test:published
   ```

   Download the published root with `GOWORK=off GOPROXY=direct go mod download
   -json github.com/agentable/go-requests@vX.Y.Z` and require its real `Sum` and
   `GoModSum` to match the precomputed values exactly. Require a clean worktree
   after all gates, including extension module checksums. Do not repair a
   published release by committing checksum changes after its tags.

   If this post-publication gate finds a release defect, fix it in a new common
   patch release. Published tags remain immutable.

## Verification

- `task verify:all` before writing the unpublished common pins
- all four annotated tags peel to the same commit
- all extension root requirements equal the common tag version
- `task verify:all` and `task test:published` on the final commit after the
  atomic push
