---
description: Step-by-step instructions to install the Ella MCPTT app on Android phones.
---

# Install the Android MCPTT App

This guide installs Ella MCPTT, the push-to-talk app for Ella IMS, on Android phones.

## Prerequisites

- **Phones**: Android 14 or later.
- **SIMs**: access rules (ARA-M) that allow the app's signing certificate, SHA-1 `692EDA27666062D3982DF420E1FEC26FE806335B`.

## 1. Download the app

Download the app and its checksum, and check them:

```shell
curl -LO https://github.com/ellanetworks/ims/releases/download/mcptt-edge/ella-mcptt.apk
curl -LO https://github.com/ellanetworks/ims/releases/download/mcptt-edge/ella-mcptt.apk.sha256
sha256sum -c ella-mcptt.apk.sha256
```

## 2. Install the app

=== "MDM"

    In your MDM, add `ella-mcptt.apk` as a private or in-house app, and assign it to the phones.

=== "adb"

    Connect the phone over USB, with USB debugging on, and run:

    ```shell
    adb install -r ella-mcptt.apk
    ```

=== "On the phone"

    Open the APK's address in the phone's browser, allow the browser to install unknown apps, and install it.

## 3. Grant the permissions

Open Ella MCPTT and go to **Settings → Diagnostics → Permissions**. Tap **Allow** on each item, and allow it in the prompt. It should show **The app has everything it needs.**

To update the app, install a newer APK the same way. Its settings are kept.
