package dashboard

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wa-campus-analyzer/internal/config"
	"wa-campus-analyzer/internal/db"
	"wa-campus-analyzer/internal/gowaclient"
	"wa-campus-analyzer/internal/worker"

	"github.com/go-chi/chi/v5"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Dashboard struct {
	cfg         *config.Config
	db          *db.DB
	auth        *AuthManager
	gowaClient  *gowaclient.Client
	logger      *slog.Logger
	templates   map[string]*template.Template
}

func NewDashboard(cfg *config.Config, database *db.DB, gowaClient *gowaclient.Client, logger *slog.Logger) (*Dashboard, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if gowaClient == nil && cfg.GOWABaseURL != "" {
		gowaClient = gowaclient.NewClient(cfg.GOWABaseURL, "admin", cfg.GOWABasicAuthPassword)
	}

	d := &Dashboard{
		cfg:        cfg,
		db:         database,
		auth:       NewAuthManager(cfg.DashboardPassword, cfg.SessionSecret),
		gowaClient: gowaClient,
		logger:     logger,
	}

	if err := d.initTemplates(); err != nil {
		return nil, fmt.Errorf("failed to initialize templates: %w", err)
	}

	return d, nil
}

func (d *Dashboard) initTemplates() error {
	tmplFuncs := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"mul": func(a, b float64) float64 { return a * b },
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict requires even number of arguments")
			}
			dict := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				dict[key] = values[i+1]
			}
			return dict, nil
		},
	}

	d.templates = make(map[string]*template.Template)

	pages := []string{"inbox.html", "agenda.html", "detail.html", "review.html", "status.html", "categories.html"}
	for _, page := range pages {
		tmpl, err := template.New(page).Funcs(tmplFuncs).ParseFS(templateFS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return fmt.Errorf("error parsing template %s: %w", page, err)
		}
		d.templates[page] = tmpl
	}

	// Login page is standalone
	loginTmpl, err := template.New("login.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/login.html")
	if err != nil {
		return fmt.Errorf("error parsing login template: %w", err)
	}
	d.templates["login.html"] = loginTmpl

	return nil
}

func (d *Dashboard) RegisterRoutes(r chi.Router) {
	// Static files
	staticSub, err := fs.Sub(staticFS, "static")
	if err == nil {
		r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	}

	// Login / Logout
	r.Get("/login", d.handleLoginView)
	r.Post("/login", d.handleLoginSubmit)
	r.Post("/logout", d.handleLogout)

	// Protected routes
	r.Group(func(pr chi.Router) {
		pr.Use(d.auth.RequireAuth)

		pr.Get("/", d.handleInbox)
		pr.Get("/categories", d.handleCategories)
		pr.Get("/agenda", d.handleAgenda)
		pr.Get("/messages/{id}", d.handleMessageDetail)
		pr.Post("/messages/{id}/feedback", d.handleMessageFeedback)
		pr.Post("/messages/{id}/status", d.handleMessageStatus)
		pr.Post("/messages/{id}/reanalyze", d.handleMessageReanalyze)
		pr.Post("/chats/ignore", d.handleIgnoreChat)
		pr.Get("/review", d.handleReviewQueue)
		pr.Post("/jobs/{id}/retry", d.handleJobRetry)
		pr.Get("/status", d.handleStatus)
		pr.Get("/api/whatsapp/status", d.handleWhatsAppStatus)
		pr.Get("/api/whatsapp/qr", d.handleWhatsAppQR)
		pr.Post("/api/whatsapp/reset", d.handleWhatsAppReset)
	})
}

// -------------------------------------------------------------
// Auth Handlers
// -------------------------------------------------------------

func (d *Dashboard) handleLoginView(w http.ResponseWriter, r *http.Request) {
	token := d.auth.GenerateCSRFToken(w, r)
	data := map[string]any{
		"CSRFToken": token,
	}
	_ = d.templates["login.html"].Execute(w, data)
}

func (d *Dashboard) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if colon := strings.LastIndex(ip, ":"); colon != -1 {
		ip = ip[:colon]
	}

	if !d.auth.rateLimiter.Allow(ip, 5, 5*time.Minute) {
		w.WriteHeader(http.StatusTooManyRequests)
		data := map[string]any{
			"Error":     "Terlalu banyak percobaan login gagal. Silakan tunggu 5 menit.",
			"CSRFToken": d.auth.GenerateCSRFToken(w, r),
		}
		_ = d.templates["login.html"].Execute(w, data)
		return
	}

	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	password := r.FormValue("password")
	if !d.auth.VerifyPassword(password) {
		d.logger.Warn("Failed dashboard login attempt", "ip", ip)
		w.WriteHeader(http.StatusUnauthorized)
		data := map[string]any{
			"Error":     "Password salah.",
			"CSRFToken": d.auth.GenerateCSRFToken(w, r),
		}
		_ = d.templates["login.html"].Execute(w, data)
		return
	}

	cookie := d.auth.CreateSessionCookie(r)
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (d *Dashboard) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	cookie := d.auth.ClearSessionCookie(r)
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// -------------------------------------------------------------
// Inbox Handler
// -------------------------------------------------------------

type InboxFilter struct {
	Q             string
	Category      string
	MinImportance string
	Status        string
	Flag          string
}

type MessageItem struct {
	ID                int64
	SenderName        string
	ChatName          string
	Category          string
	Importance        int
	StarsFull         []int
	StarsEmpty        []int
	Summary           string
	Body              string
	TruncatedBody     string
	HasMedia          bool
	MsgType           string
	DisplayTime       string
	DisplayDeadline   string
	DisplayEventStart string
	UserStateStatus   string
	NeedsReview       int
	JobStatus         string
	LastError         string
}

func (d *Dashboard) handleInbox(w http.ResponseWriter, r *http.Request) {
	filters := InboxFilter{
		Q:             strings.TrimSpace(r.URL.Query().Get("q")),
		Category:      strings.TrimSpace(r.URL.Query().Get("category")),
		MinImportance: strings.TrimSpace(r.URL.Query().Get("min_importance")),
		Status:        strings.TrimSpace(r.URL.Query().Get("status")),
		Flag:          strings.TrimSpace(r.URL.Query().Get("flag")),
	}

	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	limit := 20
	offset := (page - 1) * limit

	whereClauses := []string{"1=1"}
	var args []any

	if filters.Q != "" {
		whereClauses = append(whereClauses, "(m.body LIKE ? OR a.summary LIKE ? OR m.sender_name LIKE ?)")
		searchTerm := "%" + filters.Q + "%"
		args = append(args, searchTerm, searchTerm, searchTerm)
	}

	if filters.Category != "" {
		whereClauses = append(whereClauses, "COALESCE(us.category_override, a.category) = ?")
		args = append(args, filters.Category)
	}

	if filters.MinImportance != "" {
		if minImp, err := strconv.Atoi(filters.MinImportance); err == nil {
			whereClauses = append(whereClauses, "COALESCE(us.importance_override, a.importance) >= ?")
			args = append(args, minImp)
		}
	}

	if filters.Status != "" {
		whereClauses = append(whereClauses, "COALESCE(us.status, 'open') = ?")
		args = append(args, filters.Status)
	}

	if filters.Flag == "needs_review" {
		whereClauses = append(whereClauses, "a.needs_review = 1")
	} else if filters.Flag == "failed" {
		whereClauses = append(whereClauses, "j.status = 'failed'")
	} else if filters.Flag == "skipped" {
		whereClauses = append(whereClauses, "j.status = 'skipped'")
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	countQuery := fmt.Sprintf(`
		SELECT COUNT(1)
		FROM messages m
		LEFT JOIN jobs j ON j.message_id = m.id
		LEFT JOIN analyses a ON a.message_id = m.id AND a.is_current = 1
		LEFT JOIN user_state us ON us.message_id = m.id
		WHERE %s
	`, whereSQL)

	var totalCount int
	_ = d.db.QueryRow(countQuery, args...).Scan(&totalCount)

	query := fmt.Sprintf(`
		SELECT
			m.id, m.sender_name, m.chat_name, m.body, m.has_media, m.msg_type, m.sent_at,
			COALESCE(us.category_override, a.category, ''),
			COALESCE(us.importance_override, a.importance, 0),
			COALESCE(a.summary, ''),
			COALESCE(a.deadline, ''),
			COALESCE(a.event_start, ''),
			COALESCE(us.status, 'open'),
			COALESCE(a.needs_review, 0),
			COALESCE(j.status, 'pending'),
			COALESCE(j.last_error, '')
		FROM messages m
		LEFT JOIN jobs j ON j.message_id = m.id
		LEFT JOIN analyses a ON a.message_id = m.id AND a.is_current = 1
		LEFT JOIN user_state us ON us.message_id = m.id
		WHERE %s
		ORDER BY m.sent_at DESC, m.id DESC
		LIMIT ? OFFSET ?
	`, whereSQL)

	fetchArgs := append(args, limit, offset)
	rows, err := d.db.Query(query, fetchArgs...)
	if err != nil {
		d.logger.Error("Failed to fetch inbox messages", "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var messages []MessageItem
	for rows.Next() {
		var (
			item                                MessageItem
			hasMediaInt                         int
			senderName, chatName, body, msgType sql.NullString
			sentAtStr                           string
			deadlineStr, eventStartStr          string
		)
		err := rows.Scan(
			&item.ID, &senderName, &chatName, &body, &hasMediaInt, &msgType, &sentAtStr,
			&item.Category, &item.Importance, &item.Summary,
			&deadlineStr, &eventStartStr, &item.UserStateStatus,
			&item.NeedsReview, &item.JobStatus, &item.LastError,
		)
		if err != nil {
			d.logger.Error("Failed to scan message item", "error", err.Error())
			continue
		}

		item.SenderName = senderName.String
		item.ChatName = chatName.String
		item.Body = body.String
		item.HasMedia = hasMediaInt == 1
		item.MsgType = msgType.String

		if len([]rune(item.Body)) > 120 {
			item.TruncatedBody = string([]rune(item.Body)[:120]) + "..."
		} else {
			item.TruncatedBody = item.Body
		}

		t, _ := time.Parse(time.RFC3339, sentAtStr)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", sentAtStr)
		}
		item.DisplayTime = t.In(d.cfg.Location).Format("02 Jan 15:04")

		if deadlineStr != "" {
			dt, _ := time.Parse(time.RFC3339, deadlineStr)
			if dt.IsZero() {
				dt, _ = time.Parse("2006-01-02 15:04:05", deadlineStr)
			}
			item.DisplayDeadline = dt.In(d.cfg.Location).Format("02 Jan 15:04")
		}

		if eventStartStr != "" {
			et, _ := time.Parse(time.RFC3339, eventStartStr)
			if et.IsZero() {
				et, _ = time.Parse("2006-01-02 15:04:05", eventStartStr)
			}
			item.DisplayEventStart = et.In(d.cfg.Location).Format("02 Jan 15:04")
		}

		if item.Importance > 0 {
			item.StarsFull = make([]int, item.Importance)
			item.StarsEmpty = make([]int, 5-item.Importance)
		}

		messages = append(messages, item)
	}

	totalPages := int(math.Ceil(float64(totalCount) / float64(limit)))
	if totalPages < 1 {
		totalPages = 1
	}

	data := map[string]any{
		"Title":       "Inbox",
		"ActiveNav":   "inbox",
		"CSRFToken":   d.auth.GenerateCSRFToken(w, r),
		"Messages":    messages,
		"Filters":     filters,
		"TotalCount":  totalCount,
		"CurrentPage": page,
		"TotalPages":  totalPages,
	}

	_ = d.templates["inbox.html"].ExecuteTemplate(w, "layout", data)
}

// -------------------------------------------------------------
// Categories Handler
// -------------------------------------------------------------

type CategoryColumn struct {
	Key         string
	Title       string
	Icon        string
	HeaderClass string
	BadgeClass  string
	Messages    []MessageItem
	TotalCount  int
}

func (d *Dashboard) handleCategories(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	hideDone := r.URL.Query().Get("hide_done") == "1"

	whereClauses := []string{"1=1"}
	var args []any

	if q != "" {
		whereClauses = append(whereClauses, "(m.body LIKE ? OR a.summary LIKE ? OR m.sender_name LIKE ?)")
		searchTerm := "%" + q + "%"
		args = append(args, searchTerm, searchTerm, searchTerm)
	}

	if hideDone {
		whereClauses = append(whereClauses, "COALESCE(us.status, 'open') != 'done'")
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	query := fmt.Sprintf(`
		SELECT
			m.id, m.sender_name, m.chat_name, m.body, m.has_media, m.msg_type, m.sent_at,
			COALESCE(us.category_override, a.category, ''),
			COALESCE(us.importance_override, a.importance, 0),
			COALESCE(a.summary, ''),
			COALESCE(a.deadline, ''),
			COALESCE(a.event_start, ''),
			COALESCE(us.status, 'open'),
			COALESCE(a.needs_review, 0),
			COALESCE(j.status, 'pending'),
			COALESCE(j.last_error, '')
		FROM messages m
		LEFT JOIN jobs j ON j.message_id = m.id
		LEFT JOIN analyses a ON a.message_id = m.id AND a.is_current = 1
		LEFT JOIN user_state us ON us.message_id = m.id
		WHERE %s
		ORDER BY m.sent_at DESC, m.id DESC
	`, whereSQL)

	rows, err := d.db.Query(query, args...)
	if err != nil {
		d.logger.Error("Failed to fetch category messages", "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	columns := []*CategoryColumn{
		{Key: "tugas", Title: "Tugas & PR", Icon: "📝", HeaderClass: "cat-header-tugas", BadgeClass: "badge-primary"},
		{Key: "ujian_kuis", Title: "Ujian & Kuis", Icon: "📋", HeaderClass: "cat-header-ujian", BadgeClass: "badge-danger"},
		{Key: "jadwal", Title: "Jadwal & Kuliah", Icon: "📅", HeaderClass: "cat-header-jadwal", BadgeClass: "badge-info"},
		{Key: "workshop_event", Title: "Workshop & Event", Icon: "🎯", HeaderClass: "cat-header-workshop", BadgeClass: "badge-warning"},
		{Key: "pengumuman", Title: "Pengumuman", Icon: "📢", HeaderClass: "cat-header-pengumuman", BadgeClass: "badge-primary"},
		{Key: "info_umum", Title: "Info Umum", Icon: "💡", HeaderClass: "cat-header-info", BadgeClass: "badge-secondary"},
		{Key: "obrolan", Title: "Obrolan & Lainnya", Icon: "💬", HeaderClass: "cat-header-obrolan", BadgeClass: "badge-secondary"},
	}

	colMap := make(map[string]*CategoryColumn)
	for _, col := range columns {
		col.Messages = make([]MessageItem, 0)
		colMap[col.Key] = col
	}

	for rows.Next() {
		var (
			item                                MessageItem
			hasMediaInt                         int
			senderName, chatName, body, msgType sql.NullString
			sentAtStr                           string
			deadlineStr, eventStartStr          string
		)
		err := rows.Scan(
			&item.ID, &senderName, &chatName, &body, &hasMediaInt, &msgType, &sentAtStr,
			&item.Category, &item.Importance, &item.Summary,
			&deadlineStr, &eventStartStr, &item.UserStateStatus,
			&item.NeedsReview, &item.JobStatus, &item.LastError,
		)
		if err != nil {
			d.logger.Error("Failed to scan category message item", "error", err.Error())
			continue
		}

		item.SenderName = senderName.String
		item.ChatName = chatName.String
		item.Body = body.String
		item.HasMedia = hasMediaInt == 1
		item.MsgType = msgType.String

		if len([]rune(item.Body)) > 120 {
			item.TruncatedBody = string([]rune(item.Body)[:120]) + "..."
		} else {
			item.TruncatedBody = item.Body
		}

		t, _ := time.Parse(time.RFC3339, sentAtStr)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", sentAtStr)
		}
		item.DisplayTime = t.In(d.cfg.Location).Format("02 Jan 15:04")

		if deadlineStr != "" {
			dt, _ := time.Parse(time.RFC3339, deadlineStr)
			if dt.IsZero() {
				dt, _ = time.Parse("2006-01-02 15:04:05", deadlineStr)
			}
			item.DisplayDeadline = dt.In(d.cfg.Location).Format("02 Jan 15:04")
		}

		if eventStartStr != "" {
			et, _ := time.Parse(time.RFC3339, eventStartStr)
			if et.IsZero() {
				et, _ = time.Parse("2006-01-02 15:04:05", eventStartStr)
			}
			item.DisplayEventStart = et.In(d.cfg.Location).Format("02 Jan 15:04")
		}

		if item.Importance > 0 {
			item.StarsFull = make([]int, item.Importance)
			item.StarsEmpty = make([]int, 5-item.Importance)
		}

		targetCol, ok := colMap[item.Category]
		if !ok {
			targetCol = colMap["obrolan"]
		}
		targetCol.Messages = append(targetCol.Messages, item)
		targetCol.TotalCount++
	}

	data := map[string]any{
		"Title":     "Board Kategori",
		"ActiveNav": "categories",
		"CSRFToken": d.auth.GenerateCSRFToken(w, r),
		"Columns":   columns,
		"Query":     q,
		"HideDone":  hideDone,
	}

	if tmpl, ok := d.templates["categories.html"]; ok {
		_ = tmpl.ExecuteTemplate(w, "layout", data)
	} else {
		http.Error(w, "Template not found", http.StatusInternalServerError)
	}
}

// -------------------------------------------------------------
// Agenda Handler
// -------------------------------------------------------------

type AgendaItem struct {
	MessageID         int64
	SenderName        string
	ChatName          string
	Category          string
	Summary           string
	Topic             string
	EventLocation     string
	UserStateStatus   string
	IsEvent           bool
	TargetDate        time.Time
	DisplayTargetDate string
}

func (d *Dashboard) handleAgenda(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT
			m.id, m.sender_name, m.chat_name,
			COALESCE(us.category_override, a.category, ''),
			COALESCE(a.summary, ''),
			COALESCE(a.topic, ''),
			COALESCE(a.event_location, ''),
			COALESCE(a.deadline, ''),
			COALESCE(a.event_start, ''),
			COALESCE(us.status, 'open')
		FROM analyses a
		JOIN messages m ON m.id = a.message_id
		LEFT JOIN user_state us ON us.message_id = m.id
		WHERE a.is_current = 1
		  AND COALESCE(us.category_override, a.category) IN ('tugas', 'ujian_kuis', 'workshop_event')
		  AND COALESCE(us.status, 'open') != 'dismissed'
		  AND (a.deadline IS NOT NULL OR a.event_start IS NOT NULL)
		ORDER BY COALESCE(a.deadline, a.event_start) ASC
	`

	rows, err := d.db.Query(query)
	if err != nil {
		d.logger.Error("Failed to query agenda items", "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	now := time.Now().In(d.cfg.Location)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, d.cfg.Location)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	dayAfterTomorrow := todayStart.AddDate(0, 0, 2)
	// End of this week (Sunday 23:59:59)
	daysUntilSunday := int(time.Sunday - now.Weekday())
	if daysUntilSunday <= 0 {
		daysUntilSunday += 7
	}
	endOfWeek := todayStart.AddDate(0, 0, daysUntilSunday+1)

	var overdue, today, tomorrow, thisWeek, later []AgendaItem

	for rows.Next() {
		var (
			item                       AgendaItem
			senderName, chatName       sql.NullString
			topic, eventLocation       sql.NullString
			deadlineStr, eventStartStr string
		)
		err := rows.Scan(
			&item.MessageID, &senderName, &chatName, &item.Category, &item.Summary,
			&topic, &eventLocation, &deadlineStr, &eventStartStr, &item.UserStateStatus,
		)
		if err != nil {
			continue
		}

		item.SenderName = senderName.String
		item.ChatName = chatName.String
		item.Topic = topic.String
		item.EventLocation = eventLocation.String

		var targetTime time.Time
		if deadlineStr != "" {
			item.IsEvent = false
			targetTime, _ = time.Parse(time.RFC3339, deadlineStr)
			if targetTime.IsZero() {
				targetTime, _ = time.Parse("2006-01-02 15:04:05", deadlineStr)
			}
		} else if eventStartStr != "" {
			item.IsEvent = true
			targetTime, _ = time.Parse(time.RFC3339, eventStartStr)
			if targetTime.IsZero() {
				targetTime, _ = time.Parse("2006-01-02 15:04:05", eventStartStr)
			}
		}

		if targetTime.IsZero() {
			continue
		}

		targetLoc := targetTime.In(d.cfg.Location)
		item.TargetDate = targetLoc
		item.DisplayTargetDate = targetLoc.Format("02 Jan 15:04")

		// Classification
		if targetLoc.Before(now) {
			if item.UserStateStatus != "done" {
				overdue = append(overdue, item)
			}
		} else if targetLoc.Before(tomorrowStart) {
			today = append(today, item)
		} else if targetLoc.Before(dayAfterTomorrow) {
			tomorrow = append(tomorrow, item)
		} else if targetLoc.Before(endOfWeek) {
			thisWeek = append(thisWeek, item)
		} else {
			later = append(later, item)
		}
	}

	data := map[string]any{
		"Title":     "Agenda",
		"ActiveNav": "agenda",
		"CSRFToken": d.auth.GenerateCSRFToken(w, r),
		"Overdue":   overdue,
		"Today":     today,
		"Tomorrow":  tomorrow,
		"ThisWeek":  thisWeek,
		"Later":     later,
	}

	_ = d.templates["agenda.html"].ExecuteTemplate(w, "layout", data)
}

// -------------------------------------------------------------
// Message Detail Handler
// -------------------------------------------------------------

type MessageDetailView struct {
	ID             int64
	WaMessageID    string
	ChatJID        string
	ChatName       string
	SenderJID      string
	SenderName     string
	Body           string
	MsgType        string
	HasMedia       bool
	DisplaySentAt  string
	SentAt         time.Time
}

type AnalysisDetailView struct {
	Category          string
	Importance        int
	Confidence        float64
	Summary           string
	Reasoning         string
	Topic             string
	EventLocation     string
	DisplayDeadline   string
	DisplayEventStart string
	Model             string
	PromptVersion     string
	RawResponse       string
	TokensIn          int
	TokensOut         int
}

type UserStateView struct {
	Status             string
	CategoryOverride   string
	ImportanceOverride int
	Note               string
}

type ContextMessageView struct {
	SenderName  string
	Body        string
	DisplayTime string
}

func (d *Dashboard) handleMessageDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	msgID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// 1. Fetch message
	var msg MessageDetailView
	var senderName, chatName, body, msgType, senderJID sql.NullString
	var hasMediaInt int
	var sentAtStr string

	err = d.db.QueryRow(`
		SELECT id, wa_message_id, chat_jid, chat_name, sender_jid, sender_name, body, msg_type, has_media, sent_at
		FROM messages
		WHERE id = ?
	`, msgID).Scan(&msg.ID, &msg.WaMessageID, &msg.ChatJID, &chatName, &senderJID, &senderName, &body, &msgType, &hasMediaInt, &sentAtStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	msg.ChatName = chatName.String
	msg.SenderJID = senderJID.String
	msg.SenderName = senderName.String
	msg.Body = body.String
	msg.MsgType = msgType.String
	msg.HasMedia = hasMediaInt == 1

	t, _ := time.Parse(time.RFC3339, sentAtStr)
	if t.IsZero() {
		t, _ = time.Parse("2006-01-02 15:04:05", sentAtStr)
	}
	msg.SentAt = t
	msg.DisplaySentAt = t.In(d.cfg.Location).Format("02 January 2006, 15:04")

	// 2. Fetch context messages
	ctxMsgsRaw, _ := worker.FetchContextMessages(d.db, msg.ChatJID, msg.SentAt, msg.ID, d.cfg.ContextMessages, d.cfg.ContextWindowHours)
	var ctxMsgs []ContextMessageView
	for _, cm := range ctxMsgsRaw {
		ctxMsgs = append(ctxMsgs, ContextMessageView{
			SenderName:  cm.SenderName,
			Body:        cm.Body,
			DisplayTime: cm.SentAt.In(d.cfg.Location).Format("02 Jan 15:04"),
		})
	}

	// 3. Fetch current analysis
	var analysis *AnalysisDetailView
	var (
		topic, eventLoc, deadlineStr, eventStartStr sql.NullString
		rawResp, model, promptVer                   string
		cat, summary, reasoning                     string
		imp, tokensIn, tokensOut                    int
		conf                                        float64
	)

	err = d.db.QueryRow(`
		SELECT category, importance, confidence, summary, reasoning, topic, event_location,
		       deadline, event_start, model, prompt_version, raw_response, tokens_in, tokens_out
		FROM analyses
		WHERE message_id = ? AND is_current = 1
	`, msg.ID).Scan(
		&cat, &imp, &conf, &summary, &reasoning, &topic, &eventLoc,
		&deadlineStr, &eventStartStr, &model, &promptVer, &rawResp, &tokensIn, &tokensOut,
	)
	if err == nil {
		analysis = &AnalysisDetailView{
			Category:      cat,
			Importance:    imp,
			Confidence:    conf,
			Summary:       summary,
			Reasoning:     reasoning,
			Topic:         topic.String,
			EventLocation: eventLoc.String,
			Model:         model,
			PromptVersion: promptVer,
			RawResponse:   rawResp,
			TokensIn:      tokensIn,
			TokensOut:     tokensOut,
		}

		if deadlineStr.Valid && deadlineStr.String != "" {
			dt, _ := time.Parse(time.RFC3339, deadlineStr.String)
			if dt.IsZero() {
				dt, _ = time.Parse("2006-01-02 15:04:05", deadlineStr.String)
			}
			analysis.DisplayDeadline = dt.In(d.cfg.Location).Format("02 Jan 2006, 15:04")
		}

		if eventStartStr.Valid && eventStartStr.String != "" {
			et, _ := time.Parse(time.RFC3339, eventStartStr.String)
			if et.IsZero() {
				et, _ = time.Parse("2006-01-02 15:04:05", eventStartStr.String)
			}
			analysis.DisplayEventStart = et.In(d.cfg.Location).Format("02 Jan 2006, 15:04")
		}
	}

	// 4. Fetch User State
	userState := UserStateView{Status: "open"}
	var catOverride, note sql.NullString
	var impOverride sql.NullInt64
	err = d.db.QueryRow(`
		SELECT status, category_override, importance_override, note
		FROM user_state
		WHERE message_id = ?
	`, msg.ID).Scan(&userState.Status, &catOverride, &impOverride, &note)
	if err == nil {
		userState.CategoryOverride = catOverride.String
		userState.ImportanceOverride = int(impOverride.Int64)
		userState.Note = note.String
	}

	// 5. Fetch Job Status
	var jobStatus string
	_ = d.db.QueryRow(`SELECT status FROM jobs WHERE message_id = ?`, msg.ID).Scan(&jobStatus)

	// 6. Check if chat is ignored
	var isIgnoredCount int
	_ = d.db.QueryRow(`SELECT COUNT(1) FROM ignored_chats WHERE chat_jid = ?`, msg.ChatJID).Scan(&isIgnoredCount)

	var starsFull, starsEmpty []int
	if analysis != nil && analysis.Importance > 0 {
		starsFull = make([]int, analysis.Importance)
		starsEmpty = make([]int, 5-analysis.Importance)
	}

	data := map[string]any{
		"Title":           fmt.Sprintf("Pesan #%d", msg.ID),
		"ActiveNav":       "inbox",
		"CSRFToken":       d.auth.GenerateCSRFToken(w, r),
		"Message":         msg,
		"ContextMessages": ctxMsgs,
		"Analysis":        analysis,
		"UserState":       userState,
		"Job":             map[string]any{"Status": jobStatus},
		"IsChatIgnored":   isIgnoredCount > 0,
		"StarsFull":       starsFull,
		"StarsEmpty":      starsEmpty,
	}

	_ = d.templates["detail.html"].ExecuteTemplate(w, "layout", data)
}

// -------------------------------------------------------------
// Action Handlers
// -------------------------------------------------------------

func (d *Dashboard) handleMessageFeedback(w http.ResponseWriter, r *http.Request) {
	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	idStr := chi.URLParam(r, "id")
	msgID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	status := strings.TrimSpace(r.FormValue("status"))
	if status != "open" && status != "done" && status != "dismissed" {
		status = "open"
	}

	catOverride := strings.TrimSpace(r.FormValue("category_override"))
	var catOverrideVal any
	if catOverride != "" {
		catOverrideVal = catOverride
	}

	var impOverrideVal any
	if impStr := strings.TrimSpace(r.FormValue("importance_override")); impStr != "" {
		if imp, err := strconv.Atoi(impStr); err == nil && imp >= 1 && imp <= 5 {
			impOverrideVal = imp
		}
	}

	note := strings.TrimSpace(r.FormValue("note"))

	_, err = d.db.Exec(`
		INSERT INTO user_state (message_id, status, category_override, importance_override, note, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(message_id) DO UPDATE SET
			status = excluded.status,
			category_override = excluded.category_override,
			importance_override = excluded.importance_override,
			note = excluded.note,
			updated_at = CURRENT_TIMESTAMP;
	`, msgID, status, catOverrideVal, impOverrideVal, note)

	if err != nil {
		d.logger.Error("Failed to update user_state", "message_id", msgID, "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/messages/%d", msgID), http.StatusSeeOther)
}

func (d *Dashboard) handleMessageStatus(w http.ResponseWriter, r *http.Request) {
	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	idStr := chi.URLParam(r, "id")
	msgID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	status := strings.TrimSpace(r.FormValue("status"))
	if status != "open" && status != "done" && status != "dismissed" {
		status = "done"
	}

	_, err = d.db.Exec(`
		INSERT INTO user_state (message_id, status, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(message_id) DO UPDATE SET
			status = excluded.status,
			updated_at = CURRENT_TIMESTAMP;
	`, msgID, status)

	if err != nil {
		d.logger.Error("Failed to update status in user_state", "message_id", msgID, "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	redirect := r.FormValue("redirect")
	if redirect == "" {
		redirect = r.FormValue("redirect_to")
	}
	if redirect == "" {
		redirect = fmt.Sprintf("/messages/%d", msgID)
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (d *Dashboard) handleMessageReanalyze(w http.ResponseWriter, r *http.Request) {
	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	idStr := chi.URLParam(r, "id")
	msgID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Enqueue new pending job
	_, err = d.db.Exec(`
		INSERT INTO jobs (message_id, status, run_after)
		VALUES (?, 'pending', CURRENT_TIMESTAMP);
	`, msgID)
	if err != nil {
		d.logger.Error("Failed to enqueue reanalyze job", "message_id", msgID, "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/messages/%d", msgID), http.StatusSeeOther)
}

func (d *Dashboard) handleIgnoreChat(w http.ResponseWriter, r *http.Request) {
	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	chatJID := strings.TrimSpace(r.FormValue("chat_jid"))
	if chatJID == "" {
		http.Error(w, "Missing chat_jid", http.StatusBadRequest)
		return
	}

	_, err := d.db.Exec(`
		INSERT INTO ignored_chats (chat_jid, reason)
		VALUES (?, 'Diabaikan manual dari dashboard')
		ON CONFLICT(chat_jid) DO NOTHING;
	`, chatJID)
	if err != nil {
		d.logger.Error("Failed to ignore chat", "chat_jid", chatJID, "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	msgID := r.FormValue("message_id")
	if msgID != "" {
		http.Redirect(w, r, fmt.Sprintf("/messages/%s", msgID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// -------------------------------------------------------------
// Review Queue Handler
// -------------------------------------------------------------

type ReviewItem struct {
	MessageID    int64
	SenderName   string
	ChatName     string
	Category     string
	Summary      string
	ReviewReason string
	DisplayTime  string
}

type FailedJobItem struct {
	JobID         int64
	MessageID     int64
	SenderName    string
	ChatName      string
	TruncatedBody string
	LastError     string
	Attempts      int
	DisplayTime   string
}

func (d *Dashboard) handleReviewQueue(w http.ResponseWriter, r *http.Request) {
	// 1. Fetch needs_review analyses
	reviewQuery := `
		SELECT m.id, m.sender_name, m.chat_name, a.category, a.summary, a.confidence,
		       a.deadline, a.event_start, m.sent_at
		FROM analyses a
		JOIN messages m ON m.id = a.message_id
		WHERE a.needs_review = 1 AND a.is_current = 1
		ORDER BY m.sent_at DESC
	`
	rows, err := d.db.Query(reviewQuery)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var needsReviewItems []ReviewItem
	for rows.Next() {
		var (
			item                       ReviewItem
			senderName, chatName       sql.NullString
			confidence                 float64
			deadlineStr, eventStartStr sql.NullString
			sentAtStr                  string
		)
		if err := rows.Scan(&item.MessageID, &senderName, &chatName, &item.Category, &item.Summary, &confidence, &deadlineStr, &eventStartStr, &sentAtStr); err != nil {
			continue
		}

		item.SenderName = senderName.String
		item.ChatName = chatName.String

		t, _ := time.Parse(time.RFC3339, sentAtStr)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", sentAtStr)
		}
		item.DisplayTime = t.In(d.cfg.Location).Format("02 Jan 15:04")

		var reasons []string
		if confidence < 0.6 {
			reasons = append(reasons, fmt.Sprintf("Confidence rendah (%.0f%%)", confidence*100))
		}
		if (item.Category == "tugas" || item.Category == "workshop_event" || item.Category == "ujian_kuis") &&
			(!deadlineStr.Valid || deadlineStr.String == "") && (!eventStartStr.Valid || eventStartStr.String == "") {
			reasons = append(reasons, "Batas waktu/acara tidak ditemukan")
		}
		item.ReviewReason = strings.Join(reasons, "; ")

		needsReviewItems = append(needsReviewItems, item)
	}

	// 2. Fetch failed jobs
	failedQuery := `
		SELECT j.id, m.id, m.sender_name, m.chat_name, m.body, j.last_error, j.attempts, m.sent_at
		FROM jobs j
		JOIN messages m ON m.id = j.message_id
		WHERE j.status = 'failed'
		ORDER BY j.updated_at DESC
	`
	fRows, err := d.db.Query(failedQuery)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer fRows.Close()

	var failedJobs []FailedJobItem
	for fRows.Next() {
		var (
			f                    FailedJobItem
			senderName, chatName sql.NullString
			body                 sql.NullString
			lastError            sql.NullString
			sentAtStr            string
		)
		if err := fRows.Scan(&f.JobID, &f.MessageID, &senderName, &chatName, &body, &lastError, &f.Attempts, &sentAtStr); err != nil {
			continue
		}
		f.SenderName = senderName.String
		f.ChatName = chatName.String
		f.LastError = lastError.String

		bodyText := body.String
		if len([]rune(bodyText)) > 80 {
			f.TruncatedBody = string([]rune(bodyText)[:80]) + "..."
		} else {
			f.TruncatedBody = bodyText
		}

		t, _ := time.Parse(time.RFC3339, sentAtStr)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", sentAtStr)
		}
		f.DisplayTime = t.In(d.cfg.Location).Format("02 Jan 15:04")

		failedJobs = append(failedJobs, f)
	}

	data := map[string]any{
		"Title":            "Perlu Review",
		"ActiveNav":        "review",
		"CSRFToken":        d.auth.GenerateCSRFToken(w, r),
		"NeedsReviewItems": needsReviewItems,
		"FailedJobs":       failedJobs,
	}

	_ = d.templates["review.html"].ExecuteTemplate(w, "layout", data)
}

func (d *Dashboard) handleJobRetry(w http.ResponseWriter, r *http.Request) {
	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	jobIDStr := chi.URLParam(r, "id")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_, err = d.db.Exec(`
		UPDATE jobs
		SET status = 'pending', run_after = CURRENT_TIMESTAMP, attempts = 0, last_error = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, jobID)
	if err != nil {
		d.logger.Error("Failed to reset failed job", "job_id", jobID, "error", err.Error())
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/review", http.StatusSeeOther)
}

// -------------------------------------------------------------
// Status & Health Handler
// -------------------------------------------------------------

type CategoryStatItem struct {
	Category   string
	Count      int
	Percentage float64
}

type DailyStatItem struct {
	Date       string
	Count      int
	Percentage float64
}

func (d *Dashboard) handleStatus(w http.ResponseWriter, r *http.Request) {
	var totalMessages, totalAnalyses, pendingJobs, failedJobs int
	var tokensIn, tokensOut int

	_ = d.db.QueryRow("SELECT COUNT(1) FROM messages").Scan(&totalMessages)
	_ = d.db.QueryRow("SELECT COUNT(1) FROM analyses WHERE is_current = 1").Scan(&totalAnalyses)
	_ = d.db.QueryRow("SELECT COUNT(1) FROM jobs WHERE status = 'pending'").Scan(&pendingJobs)
	_ = d.db.QueryRow("SELECT COUNT(1) FROM jobs WHERE status = 'failed'").Scan(&failedJobs)
	_ = d.db.QueryRow("SELECT COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0) FROM analyses").Scan(&tokensIn, &tokensOut)

	// Heartbeat / Last message received
	var lastMsgStr string
	var lastMessageTime string
	var lastMessageAge *float64
	isWebhookActive := false

	err := d.db.QueryRow("SELECT received_at FROM messages ORDER BY id DESC LIMIT 1").Scan(&lastMsgStr)
	if err == nil && lastMsgStr != "" {
		t, parseErr := time.Parse("2006-01-02 15:04:05", lastMsgStr)
		if parseErr != nil {
			t, parseErr = time.Parse(time.RFC3339, lastMsgStr)
		}
		if parseErr == nil {
			age := time.Since(t).Seconds()
			lastMessageAge = &age
			lastMessageTime = t.In(d.cfg.Location).Format("02 Jan 2006, 15:04:05")
			// If received within last 48 hours, consider active
			if age < 48*3600 {
				isWebhookActive = true
			}
		}
	}

	// Category distribution
	catRows, _ := d.db.Query(`
		SELECT COALESCE(us.category_override, a.category, 'lainnya'), COUNT(1)
		FROM messages m
		JOIN analyses a ON a.message_id = m.id AND a.is_current = 1
		LEFT JOIN user_state us ON us.message_id = m.id
		GROUP BY 1
		ORDER BY 2 DESC
	`)
	var categoryStats []CategoryStatItem
	if catRows != nil {
		defer catRows.Close()
		for catRows.Next() {
			var item CategoryStatItem
			if err := catRows.Scan(&item.Category, &item.Count); err == nil {
				if totalAnalyses > 0 {
					item.Percentage = float64(item.Count) / float64(totalAnalyses) * 100.0
				}
				categoryStats = append(categoryStats, item)
			}
		}
	}

	// Daily stats (last 7 days)
	dailyRows, _ := d.db.Query(`
		SELECT strftime('%Y-%m-%d', sent_at) as day, COUNT(1)
		FROM messages
		WHERE sent_at >= datetime('now', '-7 days')
		GROUP BY 1
		ORDER BY 1 DESC
	`)
	var dailyStats []DailyStatItem
	var maxDaily int
	if dailyRows != nil {
		defer dailyRows.Close()
		for dailyRows.Next() {
			var day string
			var count int
			if err := dailyRows.Scan(&day, &count); err == nil {
				if count > maxDaily {
					maxDaily = count
				}
				dailyStats = append(dailyStats, DailyStatItem{Date: day, Count: count})
			}
		}
	}
	for i := range dailyStats {
		if maxDaily > 0 {
			dailyStats[i].Percentage = float64(dailyStats[i].Count) / float64(maxDaily) * 100.0
		}
	}

	var waStatus *gowaclient.DeviceStatus
	if d.gowaClient != nil {
		waStatus, _ = d.gowaClient.GetDeviceStatus(r.Context())
	}

	data := map[string]any{
		"Title":           "Status & Statistik",
		"ActiveNav":       "status",
		"CSRFToken":       d.auth.GenerateCSRFToken(w, r),
		"WAStatus":        waStatus,
		"TotalMessages":   totalMessages,
		"TotalAnalyses":   totalAnalyses,
		"PendingJobs":     pendingJobs,
		"FailedJobs":      failedJobs,
		"TotalTokens":     tokensIn + tokensOut,
		"TokensIn":        tokensIn,
		"TokensOut":       tokensOut,
		"IsWebhookActive": isWebhookActive,
		"LastMessageTime": lastMessageTime,
		"LastMessageAge":  lastMessageAge,
		"DBPath":          d.cfg.DBPath,
		"TZName":          d.cfg.TZName,
		"CategoryStats":   categoryStats,
		"DailyStats":      dailyStats,
	}

	_ = d.templates["status.html"].ExecuteTemplate(w, "layout", data)
}

func (d *Dashboard) handleWhatsAppStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.gowaClient == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "gowa client not configured", "connected": false})
		return
	}
	status, err := d.gowaClient.GetDeviceStatus(r.Context())
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "connected": false, "state": "error"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"connected": status.Connected,
		"state":     status.State,
		"device_id": status.ID,
		"jid":       status.JID,
	})
}

func (d *Dashboard) handleWhatsAppQR(w http.ResponseWriter, r *http.Request) {
	if d.gowaClient == nil {
		http.Error(w, "GOWA client not configured", http.StatusServiceUnavailable)
		return
	}
	pngBytes, duration, err := d.gowaClient.GetQRCodePNG(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-QR-Duration", strconv.Itoa(duration))
	_, _ = w.Write(pngBytes)
}

func (d *Dashboard) handleWhatsAppReset(w http.ResponseWriter, r *http.Request) {
	if !d.auth.ValidateCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	if d.gowaClient != nil {
		_ = d.gowaClient.DeleteDevice(r.Context())
	}
	http.Redirect(w, r, "/status", http.StatusSeeOther)
}

