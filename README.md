# Ear

Minimal background HLS audio player for iPhone.

The app writes observed playback progress to `EarPlayback.log` in its Documents directory. With file sharing enabled, the log is visible after unlocking in Files → On My iPhone → Ear.

## Build, sign, install

The free Apple ID must already be present in Xcode's account store. `xcodebuild` can update a Personal Team profile, but it cannot add a Personal Apple ID from flags.

```sh
cd /path/to/this/directory

xcrun devicectl list devices
security find-identity -v -p codesigning

export EAR_DEVICE_ID='DEVICE-IDENTIFIER-FROM-DEVICETCL'
export EAR_TEAM_ID='10-CHARACTER-TEAM-ID'
export EAR_BUNDLE_ID='com.asifahmed.Ear'
export EAR_DERIVED_DATA="$PWD/build/DerivedData"

xcodebuild \
  -project Ear.xcodeproj \
  -scheme Ear \
  -configuration Release \
  -destination "id=$EAR_DEVICE_ID" \
  -derivedDataPath "$EAR_DERIVED_DATA" \
  DEVELOPMENT_TEAM="$EAR_TEAM_ID" \
  PRODUCT_BUNDLE_IDENTIFIER="$EAR_BUNDLE_ID" \
  CODE_SIGN_STYLE=Automatic \
  -allowProvisioningUpdates \
  -allowProvisioningDeviceRegistration \
  clean build

export EAR_APP_PATH="$EAR_DERIVED_DATA/Build/Products/Release-iphoneos/Ear.app"
codesign --verify --deep --strict --verbose=2 "$EAR_APP_PATH"
xcrun devicectl device install app --device "$EAR_DEVICE_ID" "$EAR_APP_PATH"
xcrun devicectl device process launch --device "$EAR_DEVICE_ID" "$EAR_BUNDLE_ID"
```

If `security find-identity` shows no `Apple Development` identity, automatic provisioning may create one only when the Apple ID is already stored in Xcode Accounts. There is no supported `xcodebuild` Apple-ID/password option. A Personal Team profile expires after seven days; rerun the same build and install commands.

## Read the evidence log from the Mac

```sh
xcrun devicectl device copy from \
  --device "$EAR_DEVICE_ID" \
  --domain-type appDataContainer \
  --domain-identifier "$EAR_BUNDLE_ID" \
  --source Documents/EarPlayback.log \
  --destination ./EarPlayback.log

tail -100 EarPlayback.log
```

`PLAYBACK_PROGRESS` is written at most once per minute, and only after the observed media time advances while `AVPlayer.timeControlStatus` is `.playing`. It is evidence of player progress, not proof that acoustic output reached the car speakers.
