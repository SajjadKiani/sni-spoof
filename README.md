# sni-spoof

Go port of [@patterniha](https://github.com/patterniha)'s SNI-Spoofing / DPI-bypass TCP forwarder.

The repo now has two paths:

- Linux/macOS CLI forwarder (`main.go` + raw socket backends)
- Android VPN engine (`engine.go` + `api.go`) exported with gomobile and consumed by `android-app/`

## Linux/macOS CLI usage

Build and run:

```bash
go build -o sni-spoof .
sudo ./sni-spoof config.json
```

`config.json`:

```json
{
  "LISTEN_HOST": "0.0.0.0",
  "LISTEN_PORT": 40443,
  "CONNECT_IP": "104.18.4.130",
  "CONNECT_PORT": 443,
  "FAKE_SNI": "security.vercel.com"
}
```

## Android architecture

- `engine.go` (Android build tag) reads/writes raw IPv4/TCP packets from a TUN fd.
- It tracks SYN ISN, injects fake TLS ClientHello with `seq = ISN + 1 - len(fake)` when outbound third ACK is seen, and waits up to 2s for confirmation `ACK == ISN + 1`.
- Relay starts only after confirmation; timeout aborts flow.
- `api.go` exports gomobile-friendly API:

```go
type VpnEngine interface {
    Start(tunFd int, config string) error
    Stop() error
    Status() string
}
```

Also exported as top-level functions for easy Kotlin calls:

- `Start(tunFd, configJSON)`
- `Stop()`
- `Status()`

## Build Android AAR with gomobile

From module root (`/home/saji/sni-new/sni-spoof`):

```bash
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
gomobile init
gomobile bind -target android -o snispoof.aar .
```

Then copy AAR into Android app libs:

```bash
cp snispoof.aar android-app/app/libs/
```

## Android app project

Files:

- `android-app/app/src/main/java/com/snispoof/app/SniVpnService.kt`
- `android-app/app/src/main/java/com/snispoof/app/MainActivity.kt`
- `android-app/app/src/main/res/layout/activity_main.xml`
- `android-app/app/src/main/AndroidManifest.xml`
- `android-app/app/build.gradle`

Gradle dependency already configured:

```gradle
implementation fileTree(dir: 'libs', include: ['*.aar'])
```

## Open and build in Android Studio

1. Open Android Studio.
2. Choose `Open` and select `/home/saji/sni-new/sni-spoof/android-app`.
3. Let Gradle sync.
4. Confirm `android-app/app/libs/snispoof.aar` exists.
5. Build with `Build > Make Project`.

## Install and test on device (API 26+)

1. Enable developer mode + USB debugging on the Android device.
2. Connect device and run app from Android Studio.
3. In app UI, set:
   - Listen Port
   - Connect IP
   - Connect Port
   - Fake SNI
4. Tap `START`.
5. Accept Android VPN permission dialog.
6. App starts `VpnService`, passes detached TUN fd to Go engine, and status should become `running`.
7. Configure your local client to use `127.0.0.1:<LISTEN_PORT>` inside device context and verify traffic path.
8. Tap `STOP` to tear down VPN + Go engine.
