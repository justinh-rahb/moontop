#!/bin/sh
# Builds a release's notes from CHANGELOG.md and git:
#
#   scripts/release-notes.sh 0.2.0 [previous-tag] > notes.md
#
# - the version's CHANGELOG section, as written (its opening paragraph is
#   the summary; keep it good)
# - contributors since the previous tag: commit authors and
#   Co-authored-by trailers, minus the maintainer and bots
# - how to install, and a link to the full diff
#
# The release workflow runs it; run it locally to preview the notes.
set -eu

version=${1:?usage: release-notes.sh VERSION [PREVIOUS_TAG]}
version=${version#v}
prev=${2:-}
repo=${GITHUB_REPOSITORY:-justinh-rahb/moontop}
changelog=${CHANGELOG:-CHANGELOG.md}

section=$(awk -v v="$version" '
	$0 ~ "^## \\[" v "\\]" { on = 1; next }
	on && /^## \[/ { exit }
	on && /^\[[^]]+\]: / { next }
	on { print }
' "$changelog")
if [ -z "$(printf '%s' "$section" | tr -d '[:space:]')" ]; then
	echo "release-notes: no '## [$version]' section in $changelog" >&2
	exit 1
fi

# The release's commits end at its tag once it exists (rebuilding old
# notes), else at HEAD (cutting it now). The previous release is the
# highest tag below this version.
end=HEAD
git rev-parse -q --verify "refs/tags/v$version" >/dev/null && end="v$version"
if [ -z "$prev" ]; then
	prev=$(git tag --list 'v*' | grep -vx "v$version" | { cat; echo "v$version"; } |
		sort -V | awk -v t="v$version" '$0 == t { print p; exit } { p = $0 }')
fi
range=$end
[ -n "$prev" ] && range="$prev..$end"

# Everyone who wrote code for this release, except the maintainer,
# bots and AI assistants (they're tooling, not contributors).
contributors=$(
	{
		git log --format='%an' "$range"
		git log --format='%(trailers:key=Co-authored-by,valueonly)' "$range" | sed 's/ <.*//'
	} | sed '/^[[:space:]]*$/d' |
		grep -viE '^(justin hayes|justinh-rahb|claude|github-actions|dependabot)|\[bot\]' |
		sort -fu | paste -sd, - | sed 's/,/, /g'
)

printf '%s\n' "$section" | sed -e '/./,$!d'
if [ -n "$contributors" ]; then
	printf '\n### Thanks\n\nCode in this release from %s.\n' "$contributors"
fi
cat <<NOTES

### Install

\`\`\`sh
curl -fsSL https://justinh-rahb.github.io/moontop/install.sh | sh
\`\`\`

or \`go install github.com/$repo/cmd/moontop@v$version\`. On Windows, unzip \`moontop_${version}_windows_amd64.zip\` (or \`arm64\`) below and put \`moontop.exe\` on your \`PATH\`. Check a download against \`checksums.txt\`; \`moontop version\` shows what you have.
NOTES
if [ -n "$prev" ]; then
	printf '\n**Full changelog:** https://github.com/%s/compare/%s...v%s\n' "$repo" "$prev" "$version"
fi
