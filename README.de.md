# BitMerger

**Findet doppelte Einträge in deinem Bitwarden- oder Vaultwarden-Tresor und führt sie zusammen, über persönlichen Tresor, Organisationen und Sammlungen hinweg.**

Bitwarden kann keine Einträge zusammenführen. Nach Jahren voller Importe, Browser-Speicherungen und geteilter Organisationen gibt es denselben Zugang oft drei- oder viermal. BitMerger findet diese Duplikate, du entscheidest Karte für Karte, was zusammengehört, und nichts geht verloren: URLs, Notizen, Zusatzfelder, TOTP, Anhänge und Sammlungen werden vereint, und jedes alte Passwort landet in der Passwort-Historie des Eintrags, der bleibt.

[English](README.md)

- **Eine Datei, nichts installieren.** Herunterladen, starten, die Oberfläche öffnet sich im Browser.
- **Läuft nur auf deinem Rechner.** Kein Server, kein Konto, keine Telemetrie. Dein Master-Passwort geht direkt an die offizielle Bitwarden-CLI auf deinem Rechner.
- **Funktioniert mit** bitwarden.com, bitwarden.eu und selbst gehosteten Servern (Vaultwarden, Bitwarden-Server).
- **Vorsichtig gebaut.** Erst Probelauf; der bleibende Eintrag wird geschrieben und geprüft, bevor Duplikate in den Papierkorb gehen (30 Tage wiederherstellbar). Gruppen über mehrere Organisationen werden nie automatisch zusammengeführt.

## Herunterladen und starten

Die Datei für dein System gibt es im [neuesten Release](https://github.com/MiDniGG/bitmerger/releases/latest):

| System | Datei |
|---|---|
| macOS, Apple Silicon (M1, M2, M3 …) | `bitmerger-macos-apple-silicon` |
| macOS, Intel | `bitmerger-macos-intel` |
| Windows | `bitmerger-windows-x64.exe` |
| Linux | `bitmerger-linux-x64` oder `bitmerger-linux-arm64` |

Die Dateien sind noch nicht signiert, dein System fragt deshalb einmal nach:

- **macOS:** Doppelklick, dann *Systemeinstellungen → Datenschutz & Sicherheit → Trotzdem öffnen*. Oder im Terminal: `xattr -d com.apple.quarantine bitmerger-macos-*; chmod +x bitmerger-macos-*; ./bitmerger-macos-*`
- **Windows:** *Weitere Informationen → Trotzdem ausführen*.
- **Linux:** `chmod +x bitmerger-linux-* && ./bitmerger-linux-*`

Solange BitMerger läuft, bleibt ein kleines Fenster offen, und der Browser zeigt die Oberfläche. Ohne Tresor ausprobieren: mit `--demo` starten.

Beim ersten Start lädt BitMerger die offizielle Bitwarden-CLI (etwa 45 MB) von [Bitwardens GitHub-Releases](https://github.com/bitwarden/clients/releases), prüft sie gegen eine fest hinterlegte SHA-256-Prüfsumme und legt sie im eigenen Cache-Ordner ab. Am System wird nichts installiert, eine vorhandene Bitwarden-CLI bleibt unberührt.

## So funktioniert es

1. **Anmelden** mit E-Mail und Master-Passwort (zweistufige Anmeldung wird unterstützt) oder mit dem persönlichen API-Schlüssel.
2. **Suchen.** Duplikate sind Einträge mit
   - gleicher Seite **und** gleichem Benutzernamen, oder
   - gleicher Seite, ohne Benutzernamen und mit gleichem Passwort, oder
   - ohne URL mit gleichem Namen und Benutzernamen (schwächer, nie vorausgewählt).

   Ein anderer Benutzername ist nie ein Duplikat. *Nach Host* hält `fahrradladen.zendesk.com` und `reisebuero.zendesk.com` auseinander (Hilfeportale zweier Firmen, zwei Konten); *nach Domain* fasst zusätzlich `shop.example.com` und `account.example.com` zu `example.com` zusammen und markiert diese Gruppen. Plattformen auf der Public-Suffix-Liste (z. B. `*.myshopify.com`, `*.github.io`) zählen immer als getrennte Seiten. Gängige gleichwertige Domains zählen als eine (live.com = microsoft.com, gmail.com = google.com, icloud.com = apple.com, amazon.de = amazon.com). Ordner lassen sich von der Suche ausnehmen (z. B. alte Sicherungen); Papierkorb-Ordner aus Importen, etwa der „Papierkorb“ von KeePass, sind von vornherein ausgenommen.
3. **Prüfen.** Jede Gruppe ist eine Karte. Du wählst, welche Einträge mitmachen, welcher bleibt, welches Passwort gilt, und optional einen neuen Namen. Passwörter lassen sich per Klick anzeigen. Änderungen werden automatisch gespeichert.
4. **Probelauf** zeigt genau, was passieren wird.
5. **Zusammenführen** läuft im Hintergrund mit Fortschrittsanzeige. Den Browser-Tab zu schließen hält es nicht an; Beenden wartet die aktuelle Gruppe ab.

Der **Selbsttest** legt zwei Wegwerf-Einträge auf deinem Server an, führt sie zusammen, prüft das Ergebnis und löscht sie. Vor dem ersten Zusammenführen einmal laufen lassen.

## Sicherheit

- Die Oberfläche lauscht nur auf `127.0.0.1`. Zugriff braucht ein Zufalls-Token aus dem Startlink (wird zum Cookie), einen eigenen Header und den exakten Hostnamen. Das sperrt andere Webseiten und DNS-Rebinding aus.
- BitMerger schreibt nie Passwörter auf die Platte. Das Suchergebnis liegt nur im Arbeitsspeicher. Die Bitwarden-CLI hält während der Sitzung eine verschlüsselte Kopie deiner Einträge in einem eigenen Ordner; beim Beenden wird sie gelöscht. Im Cache-Ordner bleiben nur Server, E-Mail (oder API-Client-ID), Geräte-ID und die Ordnerauswahl.
- Die Bitwarden-CLI läuft mit eigenem Wegwerf-Datenordner. Beenden meldet ab und löscht ihn, ebenso 20 Minuten ohne Aktivität im Browser.
- Für das nächste Mal bleiben Server, E-Mail (oder API-Client-ID) und die Geräte-ID der CLI. So ist das Formular ausgefüllt, und der Server meldet nicht bei jedem Start ein neues Gerät. Nie gespeichert werden Master-Passwort, API-Secret und Zwei-Faktor-Codes.
- Für Tempo nutzt BitMerger `bw serve`, gebunden an `127.0.0.1` mit Bitwardens Origin-Schutz. Solange es läuft, könnten andere Programme auf deinem Rechner damit sprechen, genau wie mit einer entsperrten `bw`-Sitzung. BitMerger nicht auf geteilten oder fremden Rechnern nutzen.
- Bitwardens Historie hält höchstens fünf alte Passwörter. Gruppen, deren aktuelle Passwörter nicht hineinpassen, werden übersprungen.

## Hinweis

BitMerger ist ein unabhängiges Projekt und steht in keiner Verbindung zu Bitwarden, Inc. „Bitwarden“ ist eine Marke von Bitwarden, Inc. Nutzung auf eigenes Risiko; einen Export des Tresors aufbewahren.

## Lizenz

MIT
