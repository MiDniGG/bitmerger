# BitMerger

**Find and merge duplicate entries in your Bitwarden or Vaultwarden vault, across your personal vault, organizations and collections.**

Bitwarden has no merge function. After years of imports, browser saves and shared organizations, the same login often exists three or four times. BitMerger finds those duplicates, lets you decide card by card what belongs together, and merges them without losing anything: URLs, notes, custom fields, TOTP, attachments and collections are combined, and every old password ends up in the password history of the entry that stays.

[Deutsche Anleitung](README.de.md)

- **One file, nothing to install.** Download, start, the interface opens in your browser.
- **Runs only on your computer.** No server, no account, no telemetry. Your master password goes straight to the official Bitwarden CLI on your machine.
- **Works with** bitwarden.com, bitwarden.eu and self-hosted servers (Vaultwarden, Bitwarden server).
- **Safe by design.** Dry run first; the entry that stays is written and verified before duplicates are moved to the trash (restorable for 30 days). Groups across several organizations are never merged automatically.

## Download and start

Get the file for your system from the [latest release](https://github.com/MiDniGG/bitmerger/releases/latest):

| System | File |
|---|---|
| macOS, Apple Silicon (M1, M2, M3 …) | `bitmerger-macos-apple-silicon` |
| macOS, Intel | `bitmerger-macos-intel` |
| Windows | `bitmerger-windows-x64.exe` |
| Linux | `bitmerger-linux-x64` or `bitmerger-linux-arm64` |

The files are not code-signed yet, so your system asks once:

- **macOS:** double-click, then *System Settings → Privacy & Security → Open Anyway*. Or in Terminal: `xattr -d com.apple.quarantine bitmerger-macos-*; chmod +x bitmerger-macos-*; ./bitmerger-macos-*`
- **Windows:** *More info → Run anyway*.
- **Linux:** `chmod +x bitmerger-linux-* && ./bitmerger-linux-*`

A small window stays open while BitMerger runs, and your browser opens the interface. Try it without a vault: start with `--demo`.

On first start BitMerger downloads the official Bitwarden CLI (about 45 MB) from [Bitwarden's GitHub releases](https://github.com/bitwarden/clients/releases), checks it against a pinned SHA-256 checksum and keeps it in BitMerger's own cache folder. Nothing is installed system-wide, and an existing Bitwarden CLI setup is not touched.

## How it works

1. **Log in** with email and master password (two-step login supported) or with your personal API key.
2. **Scan.** Entries count as duplicates when they have
   - the same site **and** the same username, or
   - the same site, no username and the same password, or
   - no URL and the same name and username (weaker, never preselected).

   A different username is never a duplicate. *By host* treats `audio.example.com` and `tools.example.com` as different sites; *by domain* merges them into `example.com` and marks those groups. Common equivalent domains count as one (live.com = microsoft.com, gmail.com = google.com, icloud.com = apple.com, amazon.de = amazon.com).
3. **Review.** Every group is a card. Choose which entries take part, which one stays, which password stays, and optionally a new name. You can reveal passwords on click. Changes are saved automatically.
4. **Dry run** shows exactly what will happen.
5. **Merge** runs in the background with progress. Closing the browser tab does not stop it; quitting waits for the current group.

The **selftest** creates two throwaway entries on your server, merges them, checks the result and deletes them. Run it once before your first merge.

### What a merge does

| | |
|---|---|
| URLs, custom fields | combined, duplicates dropped |
| Notes | appended with the source entry's name |
| Password | the one you chose; all others go to the password history |
| TOTP, passkey | taken over if the remaining entry has none |
| Attachments | copied to the remaining entry |
| Collections | combined (same organization) |
| Duplicates | moved to the trash, restorable for 30 days |

Groups spanning more than one organization are skipped until you untick entries so only one organization is left.

## Security

- The web interface listens on `127.0.0.1` only. Access needs a random token from the start link (turned into a cookie), a custom header and the exact host name, which blocks other websites and DNS rebinding.
- Passwords are never written to disk by BitMerger. The plan file in the cache folder contains names, hosts and password *labels* (P1, P2), no secrets.
- The Bitwarden CLI runs with its own throwaway data folder. Quitting logs out and deletes it; so does 20 minutes without any browser activity.
- Kept for next time: server, email (or API client ID) and the CLI's device ID, so the login form is filled in and the server does not report a new device on every start. Never the master password, the API secret or two-step codes.
- For speed BitMerger uses `bw serve`, bound to `127.0.0.1` with Bitwarden's origin protection. While it runs, other programs on your computer could talk to it, just as they could use an unlocked `bw` session. Do not run BitMerger on a shared or untrusted machine.
- Bitwarden's history keeps at most five old passwords. Groups whose current passwords would not fit are skipped.

## Building

```bash
go test ./...
go run . --demo         # try the UI with made-up data
./scripts/build.sh      # all platforms into dist/
```

Set `BITMERGER_BW=/path/to/bw` to use an existing Bitwarden CLI during development.

## Disclaimer

BitMerger is an independent project, not affiliated with or endorsed by Bitwarden, Inc. "Bitwarden" is a trademark of Bitwarden, Inc. Use at your own risk and keep an export of your vault.

## License

MIT
