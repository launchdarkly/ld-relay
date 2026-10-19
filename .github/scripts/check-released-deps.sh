#!/usr/bin/env bash
# check-released-deps: verify that go.mod files of this repo's published
# modules reference only final, released versions of LaunchDarkly modules.
#
# Flags:
#   - pseudo-versions:     vX.Y.Z-0.<timestamp>-<sha> (a commit, not a release)
#   - tagged pre-releases: vX.Y.Z-rc1, vX.Y.Z-alpha.pub.N, ...
#   - `replace` of a LaunchDarkly module: any left-hand-side target, versioned
#     or not, whether single-line or in a `replace ( ... )` block, regardless
#     of the replacement; plus a non-final LaunchDarkly pin on a replace's
#     target side.
#
# Non-LaunchDarkly dependencies are not validated: third-party modules
# legitimately use pseudo-versions and +incompatible suffixes.
#
# The `allowed` list below exempts specific, documented module@version pins.
# An entry exempts only that exact version; any other non-final version of the
# same module, and any `replace` of it, is still flagged.
#
# The unpublished cmd/fdv2-integration module is excluded by the caller.
#
# Adapted from go-server-sdk's .github/scripts/check-released-deps.sh; the
# `allowed` list is the only functional difference. Keep the two in sync.
#
# Usage: check-released-deps.sh <go.mod> [...]
# Exit 0 if every LaunchDarkly reference is a released version; 1 otherwise.

set -u

# Exact module@version pins that are exempt from the check. Every entry needs
# a comment that says why it is safe to release with it.
allowed=(
  # api-client-go is the LaunchDarkly REST API client, not an SDK component.
  # Only the integrationtests package imports it, and only behind the
  # `integrationtests` build tag, so it is not compiled into the Relay binary.
  # This pseudo-version is commit f5469391a13e on api-client-go's
  # temp-beta-branch-for-SDK-payload-filter-testing branch (v13.0.0 plus a
  # generated Payload Filters API). It has been pinned since Relay v8.0.0.
  # No release contains that commit: v13.0.0 is the only v13 tag, and v14+ is
  # a new major version with a different module path.
  github.com/launchdarkly/api-client-go/v13@v13.0.1-0.20230420175109-f5469391a13e
)

rc=0
for f in "$@"; do
  findings=$(awk -v file="$f" -v allowed="${allowed[*]}" '
    BEGIN { m = split(allowed, a, /[[:space:]]+/); for (k = 1; k <= m; k++) if (a[k] != "") ok[a[k]] = 1 }
    { sub(/\/\/.*/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, "") }   # strip comments/indent
    $1 == "module" || $1 == "go" || $1 == "toolchain" || $1 == "retract" { next }
    # Track the parenthesized `replace ( ... )` block form: entries inside it
    # start with the module path, not the keyword. Update state before the
    # LaunchDarkly filter, so keyword-only block lines are not dropped first.
    $1 == "replace" && $2 == "(" { inReplace = 1; next }
    $1 == ")" && inReplace { inReplace = 0; next }
    $0 !~ /github\.com\/launchdarkly\// { next }
    {
      n = split($0, tok, /[[:space:]]+/)
      # Any replace of a LaunchDarkly module is a redirect away from the
      # released dependency, whatever the target. The left-hand path sits at
      # tok[1] inside a replace block and at tok[2] on a single-line
      # directive; check it before its optional version, so versioned
      # replaces, grouped or not, cannot slip through.
      left = inReplace ? tok[1] : (tok[1] == "replace" ? tok[2] : "")
      if (left ~ /^github\.com\/launchdarkly\//) {
        print file ": " $0 " (LaunchDarkly modules must not be replaced on release branches)"
        next
      }
      for (i = 1; i <= n; i++) {
        if (tok[i] !~ /^github\.com\/launchdarkly\//) continue
        path = tok[i]; j = i + 1
        if (j > n || tok[j] !~ /^v[0-9]+\.[0-9]+\.[0-9]+$/) {
          # Report exemptions on stderr so they stay visible in CI logs
          # without counting as findings.
          if (j <= n && ((path "@" tok[j]) in ok)) {
            print file ": " path " " tok[j] " (allowed exemption, see check-released-deps.sh)" > "/dev/stderr"
            continue
          }
          print file ": " path " " (j <= n ? tok[j] : "(no version)") " (not a released version)"
        }
      }
    }
  ' "$f")
  if [ -n "$findings" ]; then
    rc=1
    printf '%s\n' "$findings"
  fi
done

if [ "$rc" -ne 0 ]; then
  echo "FAIL: LaunchDarkly component modules listed above are not pinned to released versions." >&2
  echo "Update go.mod to released versions before merging to a release branch." >&2
fi
exit $rc
