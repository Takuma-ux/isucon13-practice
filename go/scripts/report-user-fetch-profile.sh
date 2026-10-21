#!/usr/bin/env bash
# Summarize user-fetch-related CPU in pprof files (production baseline: selectIn[...User...]).
set -euo pipefail

for f in "$@"; do
	echo "======== $(basename "$f") ========"
	go tool pprof -top "$f" 2>/dev/null | head -8
	echo "--- user-fetch path (focus) ---"
	go tool pprof -text -nodecount=25 \
		-focus='selectIn|fillUsersByIDs|coalescedLoadUserProfile|loadUserProfileFromDB|getUserProfileFromProcessCache|userFillModel' \
		"$f" 2>/dev/null | head -35
	echo "--- fillUsersByIDs cumulative ---"
	go tool pprof -top -cum "$f" 2>/dev/null | grep -E 'fillUsersByIDs|coalescedLoadUserProfile|loadUserProfileFromDB|selectIn|themes WHERE|icons WHERE' | head -15
	echo
done
