# Ella MCPTT for Android

The Android client of Ella MCPTT: Kotlin over the Go session of `../mcptt/mobile`, which the build binds to an AAR
(`:app:bindMobile`, gomobile of `../go.mod`).

## Build

Needs Go (of `../go.mod`), JDK 21, and the Android SDK with NDK 28.2.13676358 (`local.properties` or `ANDROID_HOME`).

```
./gradlew :app:spotlessCheck :app:lint :app:verifyRoborazziDebug :app:assembleDebug
```

As CI does:

- `spotlessCheck`: the Kotlin's format and the Compose rules (ktlint); `spotlessApply` fixes the format.
- `verifyRoborazziDebug`: the unit tests (JVM, Robolectric for the UI's), the UI's screenshots compared with
  `app/src/test/screenshots`. A UI change made on purpose: `recordRoborazziDebug` records them anew, the new images
  reviewed in the diff and committed. A failure's differences are in `app/build/outputs/roborazzi`.

In Android Studio, run `:app:bindMobile` once before the first sync, so that the classes of the Go session resolve.

## Testing on phones

The JVM tests cover the app's logic; the Go session's are in `../mcptt/mobile` and `../e2e`. What only a phone shows,
checked before a release on each phone model of the fleet:

- PTT both ways in a group call, the speech heard: `scripts/ptt-timing.sh` for the timings, the call media line
  (`heard` equal to the talker's `sent`) in logcat.
- PTT from the hardware key with the app in the background, after the service started in the background (a reboot,
  or an update): refused with the notice until the app is opened once, then heard.
- The dedicated bearer of the PTT's speech (QCI 65 in 4G, 5QI 65 in 5G) set up by the radio.

No instrumented tests: under instrumentation the app's process runs in the foreground with the microphone's
capability, which hides the while-in-use rule of the microphone; the emulator's ARM translation cannot run the Go
runtime (`mrs midr_el1`).

## Signing

The app runs AKA on the ISIM, which only an app signed by a certificate of the SIM's ARA-M rules may.

- Release: the Ella release key, whose SHA-1 is `release-cert.sha1`; CI signs each build of main with it, of the
  secrets `MCPTT_KEYSTORE_BASE64`, `MCPTT_KEYSTORE_PASSWORD`, `MCPTT_KEY_ALIAS` and `MCPTT_KEY_PASSWORD`. Its SHA-1 is
  in every SIM's rules.
- Debug: the developers' key, in the lab SIMs' rules only, never a customer's. In `~/.gradle/gradle.properties`:

  ```
  mcptt.devKeystore=/path/to/ella-mcptt-dev.jks
  mcptt.devKeystorePassword=...
  mcptt.devKeyAlias=...
  mcptt.devKeyPassword=...
  ```

  Without it a debug build has the machine's debug key: it installs, but gets no carrier privileges.

## Hardware PTT keys

Crosscall Core-Z5: Settings → Buttons → Programmable button 1 → Long press → Push to Talk application → Ella MCPTT.

## Versions

The version name is `../version/VERSION`, and `+N` of CI's run number `N`, the version code: each build of main updates
the last.
