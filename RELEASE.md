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
   task verify:all
   ```

2. Pin every extension to the common next version and commit the final tree:

   ```bash
   for dir in browser fingerprint http3; do
     (cd "$dir" && go mod edit -require=github.com/agentable/go-requests@vX.Y.Z)
   done
   git commit
   ```

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
   task verify:all
   task test:published
   ```

   If this post-publication gate finds a release defect, fix it in a new common
   patch release. Published tags remain immutable.

## Verification

- `task verify:all` before writing the unpublished common pins
- all four annotated tags peel to the same commit
- all extension root requirements equal the common tag version
- `task verify:all` and `task test:published` on the final commit after the
  atomic push
