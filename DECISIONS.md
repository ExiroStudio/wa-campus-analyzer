# Catatan Keputusan Desain & Riset (DECISIONS.md)

Dokumen ini mencatat temuan dokumentasi GOWA, keputusan teknis, asumsi, dan strategi pemenuhan spesifikasi proyek **WA Campus Analyzer**.

---

## 1. Temuan Riset Dokumentasi GOWA (v9)

Berdasarkan inspeksi langsung pada repositori [aldinokemal/go-whatsapp-web-multidevice](https://github.com/aldinokemal/go-whatsapp-web-multidevice):

### 1.1 Mode dan CLI Flags
- **Mode Eksekusi**: Dimulai dari versi 6/7/9, GOWA dijalankan dengan sub-command `rest` (contoh: `./whatsapp rest`).
- **MCP Server**: Mulai v9, MCP disatukan dalam perintah `rest` dan aktif secara default di endpoint `/mcp`. Untuk mematuhi aturan mutlak *read-only* (mencegah AI memiliki akses tools pengiriman WhatsApp), MCP **wajib dinonaktifkan** via environment variable `MCP_ENABLED=false` atau flag `--mcp-enabled=false`.
- **Presence Pulse**: GOWA secara default memiliki fitur harian `WHATSAPP_PRESENCE_PULSE_ENABLED=true` yang mengubah status akun menjadi *available* selama 5 menit setiap 24 jam. Ini melanggar aturan mutlak no-presence. Kita **wajib menonaktifkan** ini dengan `WHATSAPP_PRESENCE_PULSE_ENABLED=false`.
- **Presence on Connect**: Default adalah `unavailable`. Kita tetapkan eksplisit `WHATSAPP_PRESENCE_ON_CONNECT=unavailable` agar tidak memancarkan status online saat startup.
- **Fitur Otomatis Pengiriman/Status**:
  - `WHATSAPP_AUTO_MARK_READ=false` (pastikan tidak ada auto-read receipt).
  - `WHATSAPP_AUTO_REPLY=""` (pastikan tidak ada autoreply).
  - `WHATSAPP_AUTO_REJECT_CALL=false`.
- **Webhook Flags & Envs**:
  - URL Webhook: `WHATSAPP_WEBHOOK=http://analyzer:8080/webhook/gowa` (flag: `-w` / `--webhook`).
  - Webhook Secret: `WHATSAPP_WEBHOOK_SECRET=<GOWA_WEBHOOK_SECRET>` (flag: `--webhook-secret`).
  - Webhook Events: `WHATSAPP_WEBHOOK_EVENTS=message` (flag: `--webhook-events`).
  - Insecure TLS: `WHATSAPP_WEBHOOK_INSECURE_SKIP_VERIFY=false`.
- **Autentikasi GOWA**:
  - Basic auth bawaan: `APP_BASIC_AUTH=admin:campussecurepassword` (flag: `-b`).

### 1.2 Format Tanda Tangan Webhook (HMAC-SHA256)
- **Header HTTP**: `X-Hub-Signature-256`.
- **Format Header**: `sha256=<hex_digest>` (contoh: `sha256=a1b2c3d4...`).
- **Komputasi HMAC**: Raw body di-hash dengan HMAC-SHA256 menggunakan kunci rahasia webhook (`GOWA_WEBHOOK_SECRET`), lalu di-encode menjadi string hexadecimal huruf kecil (lowercase).
- **Verifikasi**: Membandingkan string digest hasil komputasi dengan nilai setelah prefiks `sha256=` menggunakan `crypto/subtle.ConstantTimeCompare` untuk mencegah *timing attacks*.

### 1.3 Struktur Payload Webhook GOWA
Format JSON yang dikirimkan GOWA pada event pesan:
```json
{
  "event": "message",
  "device_id": "628123456789@s.whatsapp.net",
  "session_id": "org_2",
  "payload": {
    "id": "3EB0C127D7BACC83D6A1",
    "chat_id": "628987654321@s.whatsapp.net",
    "from": "628123456789@s.whatsapp.net",
    "from_lid": "251556368777322@lid",
    "sender_display_name": "Pak Budi Dosen",
    "from_name": "Budi",
    "timestamp": "2026-10-05T03:00:00Z",
    "is_from_me": false,
    "body": "kumpulkan laporan besok jam 10 pagi",
    "replied_to_id": "...",
    "quoted_body": "...",
    "image": "...",
    "video": "...",
    "audio": "...",
    "document": "...",
    "sticker": "..."
  }
}
```

Pemetaan ke tabel `messages`:
- `wa_message_id` ← `payload.id`
- `chat_jid` ← `payload.chat_id`
- `chat_name` ← `payload.sender_display_name` (jika 1-on-1) atau `payload.chat_id` (fallback)
- `is_group` ← `1` jika `strings.HasSuffix(payload.chat_id, "@g.us")`, selain itu `0`
- `sender_jid` ← `payload.from`
- `sender_name` ← `payload.sender_display_name` jika ada, fallback `payload.from_name`
- `body` ← `payload.body` (pada media ber-caption, GOWA otomatis mengisi `body` dengan teks caption)
- `msg_type` ← `"text"` jika tidak ada media, atau `"image"|"video"|"audio"|"document"|"sticker"` tergantung objek media yang terisi
- `has_media` ← `1` jika salah satu dari media di atas ada, selain itu `0`
- `sent_at` ← `time.Parse(time.RFC3339, payload.timestamp)` dikonversi ke UTC
- `received_at` ← `time.Now().UTC()`
- `raw_payload` ← raw string JSON asli yang diterima webhook

---

## 2. Keputusan Arsitektur & Aturan Read-Only

1. **Komunikasi Satu Arah**:
   - Analyzer sama sekali tidak mengimpor atau menginisialisasi HTTP client yang mengarah ke GOWA. Tidak ada kode `sendMessage`, `sendReaction`, `markRead`, `setPresence`, dll.
   - Analyzer hanya bertindak sebagai HTTP server penerima (endpoint `POST /webhook/gowa`).
2. **Ketiadaan Tools/Functions pada LLM**:
   - Request ke OmniRoute OpenAI-compatible endpoint hanya mengirim `model`, `messages`, `temperature`, `max_tokens`, dan `response_format: {"type": "json_object"}`. Field `tools`, `functions`, dan `tool_choice` secara eksplisit dilarang.
3. **Status Lokal**:
   - Tombol-tombol di dashboard seperti "Selesai" dan "Abaikan" hanya memutasi tabel lokal `user_state` dan `ignored_chats`. Tidak ada panggilan jaringan ke luar.
4. **Database SQLite Pure Go**:
   - Menggunakan `modernc.org/sqlite` (tanpa CGO).
   - Mode WAL aktif (`PRAGMA journal_mode = WAL;`) dan timeout busy (`PRAGMA busy_timeout = 5000;`). Foreign keys diaktifkan (`PRAGMA foreign_keys = ON;`).
5. **Deduplikasi Webhook**:
   - `INSERT INTO messages (...) ON CONFLICT(wa_message_id) DO NOTHING` menjamin pengiriman ulang webhook dari GOWA bersifat idempoten dan tidak menimbulkan duplikasi pesan maupun job antrian.
