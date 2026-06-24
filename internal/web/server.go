package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"loft-bots/internal/db"
	"loft-bots/internal/repository"
)

//go:embed admin/index.html
var adminFiles embed.FS

type Server struct {
	port            string
	botToken        string
	userRepo        *repository.UserRepo
	eventRepo       *repository.EventRepo
	orderRepo       *repository.OrderRepo
	reservationRepo *repository.ReservationRepo
	settingsRepo    *repository.SettingsRepo
	rentalPriceRepo *repository.RentalPriceRepo
	menuCatRepo     *repository.MenuCategoryRepo
	menuItemRepo    *repository.MenuItemRepo
	mux             *http.ServeMux
}

func NewServer(
	port, botToken string,
	userRepo *repository.UserRepo,
	eventRepo *repository.EventRepo,
	orderRepo *repository.OrderRepo,
	reservationRepo *repository.ReservationRepo,
	settingsRepo *repository.SettingsRepo,
	rentalPriceRepo *repository.RentalPriceRepo,
	menuCatRepo *repository.MenuCategoryRepo,
	menuItemRepo *repository.MenuItemRepo,
) *Server {
	s := &Server{
		port:            port,
		botToken:        botToken,
		userRepo:        userRepo,
		eventRepo:       eventRepo,
		orderRepo:       orderRepo,
		reservationRepo: reservationRepo,
		settingsRepo:    settingsRepo,
		rentalPriceRepo: rentalPriceRepo,
		menuCatRepo:     menuCatRepo,
		menuItemRepo:    menuItemRepo,
		mux:             http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/admin/verify", s.handleVerify)
	s.mux.HandleFunc("/api/admin/events", s.withAuth(s.handleEvents))
	s.mux.HandleFunc("/api/admin/events/", s.withAuth(s.handleEventByID))
	s.mux.HandleFunc("/api/admin/menu/items", s.withAuth(s.handleMenuItems))
	s.mux.HandleFunc("/api/admin/menu/items/", s.withAuth(s.handleMenuItemByID))
	s.mux.HandleFunc("/api/admin/menu/categories", s.withAuth(s.handleMenuCategories))
	s.mux.HandleFunc("/api/admin/prices", s.withAuth(s.handlePrices))
	s.mux.HandleFunc("/api/admin/prices/", s.withAuth(s.handlePriceUpdate))
	s.mux.HandleFunc("/api/admin/orders", s.withAuth(s.handleOrders))
	s.mux.HandleFunc("/api/admin/orders/", s.withAuth(s.handleOrderAction))
	s.mux.HandleFunc("/api/admin/settings", s.withAuth(s.handleSettings))
	s.mux.HandleFunc("/api/admin/settings/", s.withAuth(s.handleSettingUpdate))
	s.mux.HandleFunc("/api/admin/stats/", s.withAuth(s.handleStats))
	s.mux.HandleFunc("/api/admin/schedule/", s.withAuth(s.handleSchedule))
	s.mux.HandleFunc("/", s.handleStatic)
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) verifyInitData(r *http.Request) (int64, bool) {
	initData := r.Header.Get("X-Telegram-Init-Data")
	if initData == "" {
		return 0, false
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

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		data, err := adminFiles.ReadFile("admin/index.html")
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
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

	authorized := false
	for _, id := range getAdminIDs() {
		if id == userID {
			authorized = true
			break
		}
	}

	if !authorized {
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
		fn(w, r, userID)
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case "GET":
		events, err := s.eventRepo.GetAll()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, events)
	case "POST":
		var ev db.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
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
		var ev db.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
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

	var newStatus string
	switch action {
	case "confirm":
		newStatus = "confirmed"
	case "reject":
		newStatus = "cancelled"
	case "refund":
		newStatus = "refunded"
	default:
		http.Error(w, "bad action", 400)
		return
	}

	if err := s.orderRepo.UpdateStatus(uint(orderID), newStatus); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
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
	stats, err := s.orderRepo.GetStats(period)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, stats)
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	period := strings.TrimPrefix(r.URL.Path, "/api/admin/schedule/")
	var reservations []db.Reservation
	var err error

	switch period {
	case "today":
		reservations, err = s.reservationRepo.GetFiltered("today")
	case "week":
		reservations, err = s.reservationRepo.GetFiltered("week")
	case "month":
		reservations, err = s.reservationRepo.GetFiltered("month")
	default:
		reservations, err = s.reservationRepo.GetAll()
	}

	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, reservations)
}
