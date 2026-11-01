#!/bin/sh
# Signs the latest unsigned CI build with this Mac's development certificate and installs it on the connected iPhone.
# Build the app in Xcode once first, so Xcode has created the provisioning profile for the bundle ID.
set -eu

bundle=com.triadstudios.owlrelay
profiles="$HOME/Library/Developer/Xcode/UserData/Provisioning Profiles"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

run=${1:-$(gh run list --workflow ios.yml --status success --limit 1 --json databaseId --jq '.[0].databaseId')}
gh run download "$run" --name OWLRelay --dir "$work"
ditto -x -k "$work/OWLRelay.zip" "$work"
app="$work/OWLRelay.app"

profile=
for p in "$profiles"/*.mobileprovision; do
    id=$(security cms -D -i "$p" 2>/dev/null | plutil -extract Entitlements.application-identifier raw - 2>/dev/null || true)
    case "$id" in *."$bundle") profile=$p ;; esac
done
[ -n "$profile" ] || { echo "no provisioning profile for $bundle; build once in Xcode"; exit 1; }
team=$(security cms -D -i "$profile" | plutil -extract TeamIdentifier.0 raw -)
identity=$(security find-identity -v -p codesigning | awk -F'"' '/Apple Development/ { print $2; exit }')

pb=/usr/libexec/PlistBuddy
$pb -c "Add :application-identifier string $team.$bundle" "$work/entitlements.plist" >/dev/null
$pb -c "Add :com.apple.developer.team-identifier string $team" "$work/entitlements.plist"
$pb -c "Add :get-task-allow bool true" "$work/entitlements.plist"

cp "$profile" "$app/embedded.mobileprovision"
$pb -c "Set :MWDAT:TeamID $team" "$app/Info.plist" 2>/dev/null || true
for nested in "$app"/*.dylib "$app"/Frameworks/*; do
    [ -e "$nested" ] && codesign --force --sign "$identity" "$nested"
done
codesign --force --sign "$identity" --entitlements "$work/entitlements.plist" "$app"

xcrun devicectl list devices --json-output "$work/devices.json" >/dev/null
device=$(plutil -extract result.devices.0.identifier raw "$work/devices.json")
xcrun devicectl device install app --device "$device" "$app"
xcrun devicectl device process launch --terminate-existing --device "$device" "$bundle"
