I have a Go-based SNI spoofing / DPI bypass TCP forwarder that currently works only on Linux
because it uses AF_PACKET raw sockets and CAP_NET_RAW. I want to port it to Android as a
proper app with a UI, using Android's VpnService API (no root required) and gomobile to
expose Go logic as an Android library.

---

## Current architecture (Linux-only)

The tool works as follows:
1. Listens on a local TCP port (LISTEN_HOST:LISTEN_PORT)
2. For each incoming connection, dials the upstream (CONNECT_IP:CONNECT_PORT)
3. Opens an AF_PACKET raw socket sniffer to watch the TCP handshake
4. Records the outbound SYN's ISN (Initial Sequence Number)
5. The moment the outbound 3rd-handshake ACK is seen, it injects a crafted TLS
   ClientHello packet carrying a FAKE_SNI (e.g. "security.vercel.com") with sequence
   number set to ISN+1-len(fake) — i.e. BEFORE the server's receive window
6. DPI appliances parse this fake packet and whitelist the flow; the server drops it
   as out-of-window
7. The sniffer waits for the server's ACK with ack==ISN+1, confirming the server
   ignored the fake
8. Only then does the forwarder start relaying real client<->server data
9. The real TLS ClientHello is now invisible to DPI

Config (config.json):
{
  "LISTEN_HOST": "0.0.0.0",
  "LISTEN_PORT": 40443,
  "CONNECT_IP": "104.18.4.130",
  "CONNECT_PORT": 443,
  "FAKE_SNI": "security.vercel.com"
}

---

## Goal

Port this to Android without root, replacing the AF_PACKET raw socket mechanism with a
TUN-based approach using Android's VpnService API.

---

## Required architecture changes

### 1. Replace AF_PACKET with a TUN interface (via VpnService)

On Android, use VpnService to create a TUN device. All device traffic (or selected
traffic) is routed through this TUN interface. The Go layer reads/writes raw IP packets
from the TUN file descriptor. This gives us the same visibility into TCP segments that
AF_PACKET provided on Linux — without root.

### 2. Go layer (compiled with gomobile as an .aar library)

Refactor the Go code into two parts:

#### a) Packet interception engine (replaces AF_PACKET sniffer)
- Accept a TUN file descriptor (int) from Android via JNI/gomobile
- Read raw IP/TCP packets from the TUN fd
- Implement the same ISN-tracking and fake ClientHello injection logic
- Instead of injecting via raw sockets, write the crafted packet back into the TUN fd
  (it will be routed to the upstream as if it came from the kernel)
- Confirm the server dropped the fake by watching for ACK==ISN+1
- After confirmation, switch to relay mode: forward real data between client and
  upstream using normal net.Conn

#### b) Exported gomobile interface
Export a clean Go interface for Android to call:

  type VpnEngine interface {
      Start(tunFd int, config string) error   // config is JSON string
      Stop() error
      Status() string  // "running" | "stopped" | "error: ..."
  }

Use `gomobile bind` to produce an .aar that Android can import directly.

### 3. Android app (Kotlin)

Build a simple Android app with the following:

#### VpnService subclass
- Subclass android.net.VpnService
- On start: call builder.establish() to get the TUN ParcelFileDescriptor
- Pass its fd (pfd.detachFd()) to the Go VpnEngine.Start()
- On stop: call VpnEngine.Stop() and close the VPN session

#### UI (single Activity or Compose screen)
- Config fields: Listen Port, Connect IP, Connect Port, Fake SNI
- START / STOP toggle button
- Status text view showing current state (bound to VpnEngine.Status())
- On first START: trigger VPN permission dialog (startActivityForResult with
  VpnService.prepare())

#### Permissions in AndroidManifest.xml
- BIND_VPN_SERVICE
- INTERNET
- FOREGROUND_SERVICE

---

## Constraints and notes

- The Go code must compile with CGO_ENABLED=0 where possible; use gomobile bind for
  the Android AAR
- The TUN fd passed from Android to Go is a raw int (detached from the
  ParcelFileDescriptor to avoid lifecycle issues)
- The fake ClientHello packet construction logic should stay identical to the original:
  same sequence number arithmetic (seq = ISN+1 - len(fake)), same TLS record structure
- After the fake injection window (2s timeout), if no confirmation ACK is received,
  abort the connection — keep this behavior
- Target Android API level 26+ (Android 8.0+)
- Use gomobile v0.0.0 latest; run `gomobile bind -target android -o snispoof.aar .`
  from the Go module root
- The .aar output should be placed in the Android project's app/libs/ and referenced
  in build.gradle as `implementation fileTree(dir: 'libs', include: ['*.aar'])`

---

## Deliverables

1. Refactored Go module with:
   - engine.go  — TUN-based packet interception + fake injection logic
   - api.go     — gomobile-exported VpnEngine interface + Start/Stop/Status
   - main.go    — kept for optional Linux CLI usage (AF_PACKET path unchanged)

2. Android project with:
   - SniVpnService.kt        — VpnService subclass
   - MainActivity.kt         — UI (config fields + start/stop + status)
   - activity_main.xml       — layout
   - AndroidManifest.xml     — with correct permissions and service declaration
   - build.gradle (app)      — with .aar dependency and gomobile glue

3. Step-by-step build instructions:
   - How to run `gomobile bind` to produce the .aar
   - How to open and build the Android project in Android Studio
   - How to install and test on a device

Please generate all files completely, with no placeholders or TODOs.