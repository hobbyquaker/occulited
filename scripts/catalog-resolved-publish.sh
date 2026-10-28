#!/bin/sh
# Publishes a freshly resolved catalogue on the branch catalog-resolved (occulited task 124), called by
# .github/workflows/catalog-resolve.yml:
#
#     scripts/catalog-resolved-publish.sh <catalog-resolved.json> <remote URL>
#
# The branch is appended to, never replaced: the repository's ruleset forbids force pushes, and it
# stays that way. A run commits only when the answers changed - the file without its generated_at -
# or when the published file is older than REFRESH_SECONDS (3 h): systems take the file for six hours
# (internal/resolved MaxAge), so an unchanged catalogue is re-stamped well before it goes stale
# rather than every hour. When the branch does not exist yet, its first commit is created. A push
# that loses a race is refused by the remote (no -f) and the next run tries again.
set -eu

new=$1
remote=$2
branch=${BRANCH:-catalog-resolved}
refresh=${REFRESH_SECONDS:-10800}
file=catalog-resolved.json

[ -s "$new" ] || { echo "no resolved file at $new" >&2; exit 1; }
new=$(cd "$(dirname "$new")" && pwd)/$(basename "$new")

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
cd "$dir"
git init -q
git remote add origin "$remote"

# --exit-code: 2 = the branch is not there; anything else non-zero is an error and stops the run,
# so a network failure never turns into a new orphan branch.
set +e
git ls-remote --exit-code --heads origin "$branch" >/dev/null
rc=$?
set -e
case $rc in
0)
	git fetch -q --depth=1 origin "$branch"
	git checkout -q -b "$branch" FETCH_HEAD
	if [ -f "$file" ]; then
		verdict=$(python3 - "$file" "$new" "$refresh" <<'PY'
import datetime, json, sys
old, new = (json.load(open(p)) for p in sys.argv[1:3])
stamp = old.get("generated_at", "")
old.pop("generated_at", None)
new.pop("generated_at", None)
if old != new:
    print("changed")
    sys.exit()
try:
    t = datetime.datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    age = (datetime.datetime.now(datetime.timezone.utc) - t).total_seconds()
except ValueError:
    age = float("inf")
print("refresh" if age >= float(sys.argv[3]) else "unchanged")
PY
)
	else
		verdict=changed
	fi
	;;
2)
	git checkout -q --orphan "$branch"
	verdict=first
	;;
*)
	echo "cannot read the remote's branches (git ls-remote: $rc)" >&2
	exit 1
	;;
esac

if [ "$verdict" = unchanged ]; then
	echo "catalog-resolved: unchanged, nothing committed"
	exit 0
fi

cp "$new" "$file"
git add "$file"
case $verdict in
first) msg="catalog-resolved.json: the first file" ;;
refresh) msg="catalog-resolved.json: unchanged answers, re-stamped before it goes stale" ;;
*) msg="catalog-resolved.json: changed answers" ;;
esac
[ -n "${SOURCE_SHA:-}" ] && msg="$msg (from $SOURCE_SHA)"
git -c user.name="${COMMIT_NAME:-github-actions[bot]}" \
	-c user.email="${COMMIT_EMAIL:-41898282+github-actions[bot]@users.noreply.github.com}" \
	commit -q -m "$msg"
git push -q origin "$branch:$branch"
echo "catalog-resolved: $verdict, pushed"
