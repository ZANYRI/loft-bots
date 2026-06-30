package web

import (
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"loft-bots/internal/db"
	"loft-bots/internal/notify"
	"loft-bots/internal/repository"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

//go:embed admin/index.html
var adminFiles embed.FS

type Server struct {
	port            string
	botToken        string
	clientBot       *bot.Bot
	devMode         bool
	userRepo        *repository.UserRepo
	eventRepo       *repository.EventRepo
	orderRepo       *repository.OrderRepo
	reservationRepo *repository.ReservationRepo
	settingsRepo    *repository.SettingsRepo
	rentalPriceRepo *repository.RentalPriceRepo
	menuCatRepo     *repository.MenuCategoryRepo
	menuItemRepo    *repository.MenuItemRepo
	expenseRepo     *repository.ExpenseRepo
	discountRepo    *repository.DiscountRepo
	reviewRepo      *repository.ReviewRepo
	uploadDir       string
	webAppURL       string
	mux             *http.ServeMux
	browserLinks    map[string]browserLink
	browserLinksMu  sync.Mutex
	broadcastMu     sync.Mutex
	broadcastActive bool
}

type browserLink struct {
	userID    int64
	expiresAt time.Time
}

func NewServer(
	port, botToken string, clientBot *bot.Bot,
	userRepo *repository.UserRepo,
	eventRepo *repository.EventRepo,
	orderRepo *repository.OrderRepo,
	reservationRepo *repository.ReservationRepo,
	settingsRepo *repository.SettingsRepo,
	rentalPriceRepo *repository.RentalPriceRepo,
	menuCatRepo *repository.MenuCategoryRepo,
	menuItemRepo *repository.MenuItemRepo,
	expenseRepo *repository.ExpenseRepo,
	discountRepo *repository.DiscountRepo,
	reviewRepo *repository.ReviewRepo,
) *Server {
	s := &Server{
		port:            port,
		botToken:        botToken,
		clientBot:       clientBot,
		devMode:         os.Getenv("DEV_MODE") == "true",
		userRepo:        userRepo,
		eventRepo:       eventRepo,
		orderRepo:       orderRepo,
		reservationRepo: reservationRepo,
		settingsRepo:    settingsRepo,
		rentalPriceRepo: rentalPriceRepo,
		menuCatRepo:     menuCatRepo,
		menuItemRepo:    menuItemRepo,
		expenseRepo:     expenseRepo,
		discountRepo:    discountRepo,
		reviewRepo:      reviewRepo,
		uploadDir:       "uploads",
		webAppURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("WEBAPP_URL")), "/"),
		mux:             http.NewServeMux(),
		browserLinks:    make(map[string]browserLink),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/admin/verify", s.handleVerify)
	s.mux.HandleFunc("/api/admin/browser-link", s.withAuth(s.handleBrowserLink))
	s.mux.HandleFunc("/api/admin/broadcast", s.withAuth(s.handleBroadcast))
	s.mux.HandleFunc("/browser-auth", s.handleBrowserAuth)
	s.mux.HandleFunc("/api/admin/events", s.withAuth(s.handleEvents))
	s.mux.HandleFunc("/api/admin/events/", s.withAuth(s.handleEventByID))
	s.mux.HandleFunc("/api/admin/menu/items", s.withAuth(s.handleMenuItems))
	s.mux.HandleFunc("/api/admin/menu/items/", s.withAuth(s.handleMenuItemByID))
	s.mux.HandleFunc("/api/admin/menu/categories", s.withAuth(s.handleMenuCategories))
	s.mux.HandleFunc("/api/admin/menu/categories/", s.withAuth(s.handleMenuCategoryByID))
	s.mux.HandleFunc("/api/admin/prices", s.withAuth(s.handlePrices))
	s.mux.HandleFunc("/api/admin/prices/", s.withAuth(s.handlePriceUpdate))
	s.mux.HandleFunc("/api/admin/orders", s.withAuth(s.handleOrders))
	s.mux.HandleFunc("/api/admin/orders/", s.withAuth(s.handleOrderAction))
	s.mux.HandleFunc("/api/admin/expenses", s.withAuth(s.handleExpenses))
	s.mux.HandleFunc("/api/admin/expenses/", s.withAuth(s.handleExpenseByID))
	s.mux.HandleFunc("/api/admin/discounts", s.withAuth(s.handleDiscounts))
	s.mux.HandleFunc("/api/admin/discounts/", s.withAuth(s.handleDiscountByID))
	s.mux.HandleFunc("/api/admin/reviews", s.withAuth(s.handleReviews))
	s.mux.HandleFunc("/api/admin/contacts", s.withAuth(s.handleContacts))
	s.mux.HandleFunc("/api/admin/uploads", s.withAuth(s.handleUpload))
	s.mux.HandleFunc("/api/admin/settings", s.withAuth(s.handleSettings))
	s.mux.HandleFunc("/api/admin/settings/", s.withAuth(s.handleSettingUpdate))
	s.mux.HandleFunc("/api/admin/stats/", s.withAuth(s.handleStats))
	s.mux.HandleFunc("/api/admin/reservations", s.withAuth(s.handleReservations))
	s.mux.HandleFunc("/api/admin/reservations/", s.withAuth(s.handleReservationAction))
	s.mux.HandleFunc("/api/admin/schedule/", s.withAuth(s.handleSchedule))
	s.mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(s.uploadDir))))
	s.mux.HandleFunc("/", s.handleStatic)
}

func (s *Server) handleBroadcast(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Message string
		EventID uint
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" && payload.EventID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "введите сообщение"})
		return
	}
	if len([]rune(payload.Message)) > 4000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "сообщение слишком длинное"})
		return
	}
	var event *db.Event
	if payload.EventID != 0 {
		var err error
		event, err = s.eventRepo.GetByID(payload.EventID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "мероприятие не найдено"})
			return
		}
	}
	users, err := s.userRepo.GetAll()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.broadcastMu.Lock()
	if s.broadcastActive {
		s.broadcastMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "другая рассылка ещё выполняется"})
		return
	}
	s.broadcastActive = true
	s.broadcastMu.Unlock()

	go s.runBroadcast(users, payload.Message, event)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"ok": true, "queued": len(users)})
}

func (s *Server) runBroadcast(users []db.User, message string, event *db.Event) {
	defer func() {
		s.broadcastMu.Lock()
		s.broadcastActive = false
		s.broadcastMu.Unlock()
	}()
	sent, failed := 0, 0
	for _, user := range users {
		if user.TelegramID == 0 {
			continue
		}
		var err error
		if event != nil {
			err = s.sendEventAnnouncement(context.Background(), user.TelegramID, event)
		} else {
			_, err = s.clientBot.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: user.TelegramID, Text: message})
		}
		if err != nil {
			failed++
			log.Printf("broadcast failed: telegram_id=%d err=%v", user.TelegramID, err)
		} else {
			sent++
		}
		time.Sleep(40 * time.Millisecond)
	}
	log.Printf("broadcast completed: sent=%d failed=%d", sent, failed)
}

func (s *Server) sendEventAnnouncement(ctx context.Context, chatID int64, event *db.Event) error {
	text := "🎭 Новое мероприятие: " + event.Title
	if strings.TrimSpace(event.Description) != "" {
		text += "\n\n" + strings.TrimSpace(event.Description)
	}
	text += fmt.Sprintf("\n\n📅 %s, %s–%s\n💰 Билет: %.0f ₽\n🔥 Осталось мест: %d из %d", formatDateRU(event.EventDate), event.TimeFrom, event.TimeTo, event.Price, event.PlacesLeft, event.TotalPlaces)
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "🎟 Купить билет", CallbackData: fmt.Sprintf("buy_ticket_%d", event.ID)}}}}
	image := strings.TrimSpace(event.ImageFileID)
	if strings.HasPrefix(image, "/") && s.webAppURL != "" {
		image = s.webAppURL + image
	}
	if image != "" && len([]rune(text)) <= 1024 {
		_, err := s.clientBot.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: image}, Caption: text, ReplyMarkup: kb})
		return err
	}
	_, err := s.clientBot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: kb})
	return err
}

func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

func (s *Server) Start() error {
	addr := ":" + s.port
	log.Printf("web server starting on %s", addr)
	return http.ListenAndServe(addr, corsMiddleware(s.mux))
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Telegram-Init-Data")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) verifyInitData(r *http.Request) (int64, bool) {
	if s.devMode {
		ids := getAdminIDs()
		if len(ids) > 0 {
			return ids[0], true
		}
		return 0, false
	}

	initData := r.Header.Get("X-Telegram-Init-Data")
	if initData == "" {
		return s.verifyBrowserSession(r)
	}

	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, false
	}

	hash := vals.Get("hash")
	if hash == "" {
		return 0, false
	}

	vals.Del("hash")
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		pairs = append(pairs, k+"="+vals.Get(k))
	}
	checkStr := strings.Join(pairs, "\n")

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(s.botToken))
	secretKey := secret.Sum(nil)

	h := hmac.New(sha256.New, secretKey)
	h.Write([]byte(checkStr))
	expected := hex.EncodeToString(h.Sum(nil))

	if !hmac.Equal([]byte(hash), []byte(expected)) {
		return 0, false
	}

	var user struct {
		ID int64 `json:"id"`
	}
	userStr := vals.Get("user")
	json.Unmarshal([]byte(userStr), &user)

	return user.ID, user.ID != 0
}

const browserSessionCookie = "loft_admin_session"

func (s *Server) sessionSignature(value string) string {
	h := hmac.New(sha256.New, []byte(s.botToken))
	h.Write([]byte("browser-session:" + value))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) verifyBrowserSession(r *http.Request) (int64, bool) {
	cookie, err := r.Cookie(browserSessionCookie)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return 0, false
	}
	unsigned := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(s.sessionSignature(unsigned))) {
		return 0, false
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return 0, false
	}
	return userID, userID != 0
}

func isAdmin(userID int64) bool {
	for _, id := range getAdminIDs() {
		if id == userID {
			return true
		}
	}
	return false
}

func (s *Server) handleBrowserLink(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := make([]byte, 32)
	if _, err := cryptorand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create browser link"})
		return
	}
	token := hex.EncodeToString(raw)
	s.browserLinksMu.Lock()
	for key, link := range s.browserLinks {
		if time.Now().After(link.expiresAt) {
			delete(s.browserLinks, key)
		}
	}
	s.browserLinks[token] = browserLink{userID: userID, expiresAt: time.Now().Add(2 * time.Minute)}
	s.browserLinksMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"url": "/browser-auth?token=" + token})
}

func (s *Server) handleBrowserAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.URL.Query().Get("token")
	s.browserLinksMu.Lock()
	link, ok := s.browserLinks[token]
	delete(s.browserLinks, token)
	s.browserLinksMu.Unlock()
	if !ok || time.Now().After(link.expiresAt) || !isAdmin(link.userID) {
		http.Error(w, "Ссылка недействительна или устарела", http.StatusUnauthorized)
		return
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	unsigned := strconv.FormatInt(link.userID, 10) + "." + strconv.FormatInt(expires.Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name: browserSessionCookie, Value: unsigned + "." + s.sessionSignature(unsigned),
		Path: "/", Expires: expires, MaxAge: 30 * 24 * 60 * 60,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		data, err := adminFiles.ReadFile("admin/index.html")
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Write(data)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	userID, ok := s.verifyInitData(r)
	if !ok {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}

	if !isAdmin(userID) {
		writeJSON(w, 403, map[string]string{"error": "forbidden"})
		return
	}

	writeJSON(w, 200, map[string]interface{}{"ok": true, "user_id": userID})
}

func getAdminIDs() []int64 {
	raw := os.Getenv("ADMIN_TELEGRAM_IDS")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var ids []int64
	for _, p := range parts {
		id, _ := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

type authHandler func(w http.ResponseWriter, r *http.Request, userID int64)

func (s *Server) withAuth(fn authHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := s.verifyInitData(r)
		if !ok {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		if !isAdmin(userID) {
			writeJSON(w, 403, map[string]string{"error": "forbidden"})
			return
		}
		fn(w, r, userID)
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func dateRange(r *http.Request) (*time.Time, *time.Time, error) {
	parse := func(key string) (*time.Time, error) {
		value := r.URL.Query().Get(key)
		if value == "" {
			return nil, nil
		}
		date, err := time.Parse("2006-01-02", value)
		if err != nil {
			return nil, err
		}
		return &date, nil
	}
	from, err := parse("from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parse("to")
	if err != nil {
		return nil, nil, err
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, fmt.Errorf("дата начала позже даты окончания")
	}
	return from, to, nil
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case "GET":
		from, to, err := dateRange(r)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		events, err := s.eventRepo.GetByRange(from, to)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, events)
	case "POST":
		ev, err := decodeEvent(r)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if webDateOnly(ev.EventDate).Before(webDateOnly(time.Now())) {
			writeJSON(w, 400, map[string]string{"error": "нельзя создать мероприятие задним числом"})
			return
		}
		if ev.TotalPlaces <= 0 {
			ev.TotalPlaces = 20
		}
		if ev.PlacesLeft <= 0 {
			ev.PlacesLeft = ev.TotalPlaces
		}
		ev.CreatedAt = time.Now()
		if err := s.eventRepo.Create(&ev); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, ev)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleEventByID(w http.ResponseWriter, r *http.Request, userID int64) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/admin/events/")
	id, _ := strconv.ParseUint(idStr, 10, 64)
	if id == 0 {
		http.Error(w, "bad id", 400)
		return
	}

	switch r.Method {
	case "PUT":
		ev, err := decodeEvent(r)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if webDateOnly(ev.EventDate).Before(webDateOnly(time.Now())) {
			writeJSON(w, 400, map[string]string{"error": "нельзя сохранить мероприятие задним числом"})
			return
		}
		ev.ID = uint(id)
		if err := s.eventRepo.Update(&ev); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, ev)
	case "DELETE":
		if err := s.eventRepo.Delete(uint(id)); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// decodeEvent accepts both the date-only value emitted by an HTML date input
// and the RFC3339 value returned by the API.
func decodeEvent(r *http.Request) (db.Event, error) {
	var payload struct {
		Title        string
		Description  string
		ImageFileID  string
		EventDate    string
		TimeFrom     string
		TimeTo       string
		Price        float64
		TotalPlaces  int
		PlacesLeft   int
		IsActive     bool
		PaymentPhone string
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return db.Event{}, err
	}

	date, err := time.Parse("2006-01-02", payload.EventDate)
	if err != nil {
		date, err = time.Parse(time.RFC3339, payload.EventDate)
	}
	if err != nil {
		return db.Event{}, err
	}

	return db.Event{
		Title:        payload.Title,
		Description:  payload.Description,
		ImageFileID:  payload.ImageFileID,
		EventDate:    date,
		TimeFrom:     payload.TimeFrom,
		TimeTo:       payload.TimeTo,
		Price:        payload.Price,
		TotalPlaces:  payload.TotalPlaces,
		PlacesLeft:   payload.PlacesLeft,
		IsActive:     payload.IsActive,
		PaymentPhone: payload.PaymentPhone,
	}, nil
}

func webDateOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.Local)
}

func webDayType(date time.Time) string {
	weekday := date.Weekday()
	if weekday == time.Friday || weekday == time.Saturday || weekday == time.Sunday {
		return "weekend"
	}
	return "weekday"
}

func webCalcHours(timeFrom, timeTo string) int {
	from := webMinutesOfDay(timeFrom)
	to := webMinutesOfDay(timeTo)
	if to <= from {
		to += 24 * 60
	}
	return (to - from + 59) / 60
}

func webMinutesOfDay(value string) int {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0
	}
	hour, _ := strconv.Atoi(parts[0])
	minute, _ := strconv.Atoi(parts[1])
	return hour*60 + minute
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Server) handleMenuItems(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case "GET":
		items, err := s.menuItemRepo.GetAll()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, items)
	case "POST":
		var item db.MenuItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := s.menuItemRepo.Create(&item); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, item)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleMenuItemByID(w http.ResponseWriter, r *http.Request, userID int64) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/admin/menu/items/")
	id, _ := strconv.ParseUint(idStr, 10, 64)
	if id == 0 {
		http.Error(w, "bad id", 400)
		return
	}

	switch r.Method {
	case "PUT":
		var item db.MenuItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		item.ID = uint(id)
		if err := s.menuItemRepo.Update(&item); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, item)
	case "DELETE":
		if err := s.menuItemRepo.Delete(uint(id)); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleMenuCategories(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case "GET":
		cats, err := s.menuCatRepo.GetAll()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, cats)
	case "POST":
		var cat db.MenuCategory
		if err := json.NewDecoder(r.Body).Decode(&cat); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := s.menuCatRepo.Create(&cat); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, cat)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleMenuCategoryByID(w http.ResponseWriter, r *http.Request, userID int64) {
	id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/api/admin/menu/categories/"), 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case "PUT":
		var category db.MenuCategory
		if err := json.NewDecoder(r.Body).Decode(&category); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		category.ID = uint(id)
		if err := s.menuCatRepo.Update(&category); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, category)
	case "DELETE":
		if err := s.menuCatRepo.Delete(uint(id)); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePrices(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	prices, err := s.rentalPriceRepo.GetAll()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, prices)
}

func (s *Server) handlePriceUpdate(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	dayType := strings.TrimPrefix(r.URL.Path, "/api/admin/prices/")
	if dayType != "weekday" && dayType != "weekend" {
		http.Error(w, "bad day type", 400)
		return
	}
	var body struct {
		PricePerHour float64 `json:"price_per_hour"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if err := s.rentalPriceRepo.Update(dayType, body.PricePerHour); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleExpenses(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case "GET":
		from, to, err := dateRange(r)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		expenses, err := s.expenseRepo.GetByRange(from, to)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, expenses)
	case "POST":
		var expense db.Expense
		if err := json.NewDecoder(r.Body).Decode(&expense); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		expense.Title = strings.TrimSpace(expense.Title)
		if expense.Title == "" || expense.Amount <= 0 {
			writeJSON(w, 400, map[string]string{"error": "укажите описание и сумму расхода"})
			return
		}
		expense.CreatedAt = time.Now()
		if err := s.expenseRepo.Create(&expense); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, expense)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, userID int64) {
	const maxImageSize = 10 << 20
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(maxImageSize); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "изображение должно быть не больше 10 МБ"})
		return
	}

	file, _, err := r.FormFile("image")
	allowPDF := false
	if err != nil {
		file, _, err = r.FormFile("file")
		allowPDF = true
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "выберите файл"})
		return
	}
	defer file.Close()

	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать изображение"})
		return
	}

	extensions := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}
	if allowPDF {
		extensions["application/pdf"] = ".pdf"
	}
	extension, ok := extensions[http.DetectContentType(header[:n])]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "поддерживаются JPG, PNG, WebP и PDF"})
		return
	}
	if err := os.MkdirAll(s.uploadDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	randomBytes := make([]byte, 16)
	if _, err := cryptorand.Read(randomBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	filename := hex.EncodeToString(randomBytes) + extension
	destination, err := os.Create(filepath.Join(s.uploadDir, filename))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer destination.Close()
	if _, err := destination.Write(header[:n]); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := io.Copy(destination, io.LimitReader(file, maxImageSize-int64(n))); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	baseURL := s.webAppURL
	if baseURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		baseURL = scheme + "://" + r.Host
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": baseURL + "/uploads/" + filename})
}

func (s *Server) handleExpenseByID(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "DELETE" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/api/admin/expenses/"), 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.expenseRepo.Delete(uint(id)); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDiscounts(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case "GET":
		items, err := s.discountRepo.GetAll()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, items)
	case "POST":
		var item db.Discount
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil || strings.TrimSpace(item.Name) == "" || item.Percent <= 0 || item.Percent > 100 {
			writeJSON(w, 400, map[string]string{"error": "укажите название и скидку от 1 до 100%"})
			return
		}
		item.Name = strings.TrimSpace(item.Name)
		if err := s.discountRepo.Create(&item); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, item)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleDiscountByID(w http.ResponseWriter, r *http.Request, userID int64) {
	id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/api/admin/discounts/"), 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "bad id", 400)
		return
	}
	if r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var item db.Discount
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil || strings.TrimSpace(item.Name) == "" || item.Percent <= 0 || item.Percent > 100 {
		writeJSON(w, 400, map[string]string{"error": "укажите название и скидку от 1 до 100%"})
		return
	}
	item.ID = uint(id)
	item.Name = strings.TrimSpace(item.Name)
	if err := s.discountRepo.Update(&item); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, item)
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	status := r.URL.Query().Get("status")
	period := r.URL.Query().Get("period")
	orders, err := s.orderRepo.GetFiltered(status, period)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, orders)
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	reviews, err := s.reviewRepo.GetAll()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, reviews)
}

func (s *Server) handleContacts(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	users, err := s.userRepo.GetAll()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, users)
}

func (s *Server) handleOrderAction(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/orders/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.Error(w, "bad path", 400)
		return
	}
	orderID, _ := strconv.ParseUint(parts[0], 10, 64)
	action := parts[1]

	order, err := s.orderRepo.GetByID(uint(orderID))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "заказ не найден"})
		return
	}

	var newStatus string
	switch action {
	case "confirm":
		newStatus = "confirmed"
		if order.Reservation != nil && order.TotalPrice < order.Reservation.TotalPrice {
			newStatus = "prepaid"
		}
	case "reject":
		newStatus = "cancelled"
	case "refund":
		newStatus = "refunded"
	default:
		http.Error(w, "bad action", 400)
		return
	}
	if action == "reject" {
		if order.Status == "pending" && order.EventID != nil {
			quantity := order.TicketQuantity
			if quantity < 1 {
				quantity = 1
			}
			if err := s.eventRepo.ReleasePlaces(*order.EventID, quantity); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
	}

	if err := s.orderRepo.UpdateStatus(uint(orderID), newStatus); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	order, err = s.orderRepo.GetByID(uint(orderID))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "заказ не найден"})
		return
	}
	if order.ReservationID != nil {
		switch newStatus {
		case "confirmed", "prepaid":
			if err := s.reservationRepo.UpdateStatus(*order.ReservationID, "confirmed"); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		case "cancelled", "refunded":
			if err := s.reservationRepo.Cancel(*order.ReservationID); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	settings, err := s.settingsRepo.GetAll()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, settings)
}

func (s *Server) handleSettingUpdate(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/api/admin/settings/")
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if err := s.settingsRepo.Set(key, body.Value); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	period := strings.TrimPrefix(r.URL.Path, "/api/admin/stats/")
	from, to, err := dateRange(r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	stats, err := s.orderRepo.GetStats(period, from, to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var expenses float64
	if from != nil || to != nil {
		expenses, err = s.expenseRepo.TotalByRange(from, to)
	} else {
		expenses, err = s.expenseRepo.TotalForPeriod(period)
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	stats["total_expenses"] = expenses
	stats["net_profit"] = stats["total_revenue"].(float64) - expenses
	writeJSON(w, 200, stats)
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	from, to, err := dateRange(r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	reservations, err := s.reservationRepo.GetByRange(from, to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	for i := range reservations {
		if reservations[i].UserID == nil {
			continue
		}
		paid, err := s.orderRepo.PrepaidByReservationID(reservations[i].ID)
		if err != nil {
			log.Printf("failed to calculate reservation payment: reservation_id=%d err=%v", reservations[i].ID, err)
			continue
		}
		reservations[i].PaidAmount = paid
	}
	events, err := s.eventRepo.GetByRange(from, to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"reservations": reservations, "events": events})
}

type reservationPayload struct {
	UserID           uint
	Date             string
	TimeFrom         string
	TimeTo           string
	Status           string
	TotalPrice       float64
	PrepaymentAmount float64
}

func (s *Server) handleReservations(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var payload reservationPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if payload.Date == "" || payload.TimeFrom == "" || payload.TimeTo == "" || payload.TotalPrice <= 0 {
		writeJSON(w, 400, map[string]string{"error": "заполните дату, время и сумму"})
		return
	}
	date, err := time.Parse("2006-01-02", payload.Date)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "неверная дата"})
		return
	}
	if payload.Status == "" {
		payload.Status = "pending"
	}
	if payload.Status != "pending" && payload.Status != "confirmed" && payload.Status != "cancelled" {
		writeJSON(w, 400, map[string]string{"error": "неверный статус брони"})
		return
	}
	if payload.PrepaymentAmount < 0 {
		payload.PrepaymentAmount = 0
	}
	if payload.PrepaymentAmount > payload.TotalPrice {
		payload.PrepaymentAmount = payload.TotalPrice
	}
	var reservationUserID *uint
	if payload.UserID != 0 {
		if _, err := s.userRepo.GetByID(payload.UserID); err != nil {
			writeJSON(w, 404, map[string]string{"error": "пользователь не найден"})
			return
		}
		reservationUserID = &payload.UserID
	}
	reservation := &db.Reservation{
		UserID:       reservationUserID,
		Date:         date,
		TimeFrom:     payload.TimeFrom,
		TimeTo:       payload.TimeTo,
		DayType:      webDayType(date),
		PricePerHour: payload.TotalPrice / float64(maxInt(webCalcHours(payload.TimeFrom, payload.TimeTo), 1)),
		TotalPrice:   payload.TotalPrice,
		Status:       payload.Status,
	}
	if reservationUserID == nil {
		reservation.PaidAmount = payload.PrepaymentAmount
	}
	if err := s.reservationRepo.Create(reservation); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if payload.PrepaymentAmount > 0 && reservationUserID != nil {
		order := &db.Order{
			UserID:        payload.UserID,
			ReservationID: &reservation.ID,
			TotalPrice:    payload.PrepaymentAmount,
			Status:        "prepaid",
			CreatedAt:     time.Now(),
		}
		if payload.PrepaymentAmount >= payload.TotalPrice {
			order.Status = "confirmed"
		}
		if err := s.orderRepo.Create(order); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, 201, reservation)
}

func (s *Server) handleReservationAction(w http.ResponseWriter, r *http.Request, userID int64) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/reservations/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if r.Method == http.MethodPut && len(parts) == 1 {
		s.handleReservationUpdate(w, r, parts[0])
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(parts) != 2 || parts[1] != "cancel" {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}

	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || id == 0 {
		writeJSON(w, 400, map[string]string{"error": "invalid reservation id"})
		return
	}

	reservation, err := s.reservationRepo.GetByID(uint(id))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "reservation not found"})
		return
	}
	if reservation.Status == "cancelled" {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}

	if err := s.reservationRepo.Cancel(uint(id)); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if err := s.orderRepo.CancelByReservationID(uint(id)); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	s.notifyReservationCancelled(r.Context(), reservation)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleReservationUpdate(w http.ResponseWriter, r *http.Request, idPart string) {
	id, err := strconv.ParseUint(idPart, 10, 64)
	if err != nil || id == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный номер брони"})
		return
	}
	reservation, err := s.reservationRepo.GetByID(uint(id))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "бронь не найдена"})
		return
	}
	var payload reservationPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if payload.Date == "" || payload.TimeFrom == "" || payload.TimeTo == "" || payload.TotalPrice <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "заполните дату, время и сумму"})
		return
	}
	date, err := time.Parse("2006-01-02", payload.Date)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверная дата"})
		return
	}
	if payload.Status != "pending" && payload.Status != "confirmed" && payload.Status != "cancelled" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный статус брони"})
		return
	}
	var selectedUserID *uint
	if payload.UserID != 0 {
		if _, err := s.userRepo.GetByID(payload.UserID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "пользователь не найден"})
			return
		}
		selectedUserID = &payload.UserID
	}
	previousStatus := reservation.Status
	reservation.UserID = selectedUserID
	reservation.Date = date
	reservation.TimeFrom = payload.TimeFrom
	reservation.TimeTo = payload.TimeTo
	reservation.DayType = webDayType(date)
	reservation.TotalPrice = payload.TotalPrice
	reservation.PricePerHour = payload.TotalPrice / float64(maxInt(webCalcHours(payload.TimeFrom, payload.TimeTo), 1))
	reservation.Status = payload.Status
	if selectedUserID == nil {
		reservation.PaidAmount = payload.PrepaymentAmount
		if reservation.PaidAmount < 0 {
			reservation.PaidAmount = 0
		}
		if reservation.PaidAmount > reservation.TotalPrice {
			reservation.PaidAmount = reservation.TotalPrice
		}
	} else {
		reservation.PaidAmount = 0
	}
	if err := s.reservationRepo.UpdateDetails(reservation); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if payload.Status == "cancelled" && previousStatus != "cancelled" {
		_ = s.orderRepo.CancelByReservationID(reservation.ID)
		s.notifyReservationCancelled(r.Context(), reservation)
	}
	writeJSON(w, http.StatusOK, reservation)
}

func (s *Server) notifyReservationCancelled(ctx context.Context, reservation *db.Reservation) {
	if reservation.UserID == nil || reservation.User.TelegramID == 0 {
		return
	}
	message := "❌ Ваша бронь на {date}, {time_from}–{time_to} была отменена администратором."
	if setting, err := s.settingsRepo.Get("message_reservation_cancelled"); err == nil && setting != nil && strings.TrimSpace(setting.Value) != "" {
		message = setting.Value
	}
	message = strings.ReplaceAll(message, "{date}", formatDateRU(reservation.Date))
	message = strings.ReplaceAll(message, "{time_from}", reservation.TimeFrom)
	message = strings.ReplaceAll(message, "{time_to}", reservation.TimeTo)
	notify.SendToUser(ctx, s.clientBot, reservation.User.TelegramID, message)
}

func formatDateRU(date time.Time) string {
	months := []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	return fmt.Sprintf("%d %s %d", date.Day(), months[date.Month()-1], date.Year())
}
