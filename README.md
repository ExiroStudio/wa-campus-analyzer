# WA Campus Analyzer

Sistem cerdas penerima pesan WhatsApp masuk di nomor khusus kampus milik mahasiswa Teknik Informatika. Pesan dianalisis secara otomatis menggunakan AI (OmniRoute / OpenAI-compatible API) untuk mengekstrak informasi penting, tugas, jadwal, ujian, dan workshop, serta menampilkannya di Dashboard Web yang responsif.

---

## 🔒 Jaminan Mutlak: Sistem Read-Only & Zero-Presence

Sistem ini dirancang dengan prinsip **strictly read-only**:
1. **Tidak Ada HTTP Client ke GOWA**: Kode aplikasi analyzer sama sekali tidak menginisialisasi atau memanggil endpoint pengiriman pesan, reaksi, status, tanda baca, atau presence ke GOWA. Komunikasi 100% satu arah dari GOWA ke analyzer via webhook.
2. **Tidak Ada Tools / Function Calling pada LLM**: Request ke OmniRoute tidak menyertakan parameter `tools`, `functions`, atau `tool_choice`.
3. **Pesan Tetap Berstatus Belum Dibaca**: GOWA dikonfigurasi dengan `--auto-mark-read=false`. Tidak ada read receipt (centang biru) yang dikirim. Pesan tetap belum dibaca sampai pemilik membukanya di HP.
4. **Zero-Presence**: GOWA dikonfigurasi dengan `--presence-on-connect=unavailable` dan `--presence-pulse-enabled=false` untuk mencegah indikator online / mengetik / merekam.
5. **Aksi Dashboard Hanya Lokal**: Tombol "Selesai", "Abaikan", serta catatan di dashboard hanya mengubah status di database SQLite lokal (`user_state`), tanpa berdampak ke WhatsApp.

---

## 🛠️ Persyaratan Sistem & Arsitektur

- **Bahasa**: Go 1.25+
- **Database**: SQLite (pure-Go `modernc.org/sqlite`, tanpa CGO), mode WAL & busy timeout
- **WhatsApp Gateway**: GOWA v9 (`aldinokemal/go-whatsapp-web-multidevice`) dalam mode `rest`
- **AI Gateway**: OmniRoute (OpenAI-compatible `/chat/completions`)
- **Web UI**: Server-rendered `html/template` + vanilla CSS ter-embed dalam binary

---

## 🚀 Panduan Setup & Menjalankan

### 1. Salin dan Sesuaikan Konfigurasi Lingkungan
```bash
cp .env.example .env
```
Isi konfigurasi berikut di file `.env`:
- `GOWA_WEBHOOK_SECRET`: Secret acak untuk validasi HMAC-SHA256 webhook.
- `OMNIROUTE_BASE_URL`: URL API OmniRoute (misal: `https://api.omniroute.ai/v1`).
- `OMNIROUTE_API_KEY`: API Key Anda.
- `OMNIROUTE_MODEL`: Model yang digunakan (contoh: `gpt-4o-mini`).
- `DASHBOARD_PASSWORD`: Password tunggal untuk login ke dashboard web.
- `SESSION_SECRET`: String rahasia minimal 32 karakter untuk penanda tangan cookie sesi.

### 2. Login & Scan QR Code WhatsApp
Secara default, port GOWA (3000) **tidak dipublish ke host** demi keamanan jaringan.

Untuk scan QR pertama kali:
1. Buka file `docker-compose.yml`, uncomment bagian port sementara GOWA:
   ```yaml
   ports:
     - "127.0.0.1:3000:3000"
   ```
2. Jalankan docker compose:
   ```bash
   docker compose up -d
   ```
3. Buka browser di `http://127.0.0.1:3000`. Masukkan kredensial basic auth (default: `admin` / `gowaSecureAdmin123`), lalu scan QR Code menggunakan aplikasi WhatsApp di HP nomor kampus.
4. Setelah WhatsApp terhubung, **tutup kembali port GOWA** untuk mengisolasi gateway:
   - Comment kembali baris ports di `docker-compose.yml`.
   - Jalankan ulang:
     ```bash
     docker compose up -d
     ```
   - Port 3000 kini sepenuhnya tertutup dari luar dan hanya bisa dihubungi oleh analyzer melalui network internal Docker.

### 3. Akses Dashboard Web
Buka browser di:
```text
http://127.0.0.1:8080
```
Login menggunakan `DASHBOARD_PASSWORD` yang telah diatur di `.env`.

---

## 🧪 Verifikasi & Audit Keamanan

### 1. Menjalankan Seluruh Pengujian Otomatis
```bash
go test -v -count=1 ./...
```
Pengujian mencakup:
- Verifikasi tanda tangan HMAC-SHA256 (valid, invalid, body di-tamper).
- Deduplikasi pesan webhook ganda.
- Pre-filter pesan singkat, kata filler, dan emoji murni.
- Parser JSON toleran dan validasi skema keluaran AI.
- Ketahanan terhadap prompt injection adversarial.
- Audit ketiadaan client pengiriman WhatsApp / GOWA di seluruh kode.
- Audit ketiadaan tools/functions pada body request OmniRoute.

### 2. Uji Isolasi Port GOWA
Jalankan perintah berikut dari host setelah scan QR:
```bash
curl -I http://127.0.0.1:3000
```
Hasil yang diharapkan: `Connection refused` (membuktikan port GOWA tidak dapat diakses dari luar network Docker).

### 3. Pemeriksaan Status Kesehatan Webhook
Buka endpoint health check:
```bash
curl http://127.0.0.1:8080/healthz
```
Respon JSON menampilkan status koneksi database dan umur pesan terakhir yang diterima.

---

## 💾 Backup Data
Seluruh data pesan, hasil analisis, catatan, antrian, dan status pengguna tersimpan di direktori `./data`.
Untuk melakukan backup:
```bash
# Backup database SQLite saat WAL aktif menggunakan SQLite CLI atau copy aman
sqlite3 ./data/analyzer.db ".backup ./data/backup-$(date +%Y%m%d).db"
```
Atau cukup backup folder `./data/` saat analyzer sedang dimatikan.
Sesi login WhatsApp GOWA tersimpan di Docker volume `gowa-storages`.
