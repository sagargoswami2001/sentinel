// Package server runs checks continuously and serves the web dashboard.
//
// Two modes:
//   - normal (default): one shared board, persisted to YAML. For your
//     own laptop, VM, or private server.
//   - public demo (SENTINEL_PUBLIC=1 or --public flag): every visitor
//     gets their own private in-memory board via a session cookie.
//     For a public Render/PaaS instance where strangers try the tool.
package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sagargoswami2001/sentinel/internal/checker"
	"github.com/sagargoswami2001/sentinel/internal/config"
	"github.com/sagargoswami2001/sentinel/internal/metrics"
	"github.com/sagargoswami2001/sentinel/internal/notify"
)

//go:embed templates static
var assets embed.FS

const (
	historyLen      = 60
	sessionTTL      = 2 * time.Hour
	maxPublicBoards = 500
	maxPublicTargets = 15
)

type sample struct {
	up bool
	ms int64
}

type incident struct {
	when   time.Time
	name   string
	down   bool
	detail string
}

// board is one visitor's dashboard state.
type board struct {
	targets   []config.Target
	results   []checker.Result
	history   map[string][]sample
	incidents []incident
	notifyCfg config.Notify
	notifiers []notify.Notifier
	lastRun   time.Time
	lastSeen  time.Time
}

func newBoard() *board {
	return &board{history: make(map[string][]sample)}
}

// Server owns all boards and serves HTTP.
type Server struct {
	cfgPath  string
	defaults config.Defaults
	interval time.Duration
	tmpl     *template.Template
	public   bool // true = per-visitor session boards

	mu       sync.RWMutex
	main     *board            // used in normal mode
	sessions map[string]*board // used in public mode
}

func parseTemplates() (*template.Template, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	return tmpl, nil
}

func New(cfg *config.Config, cfgPath string, interval time.Duration, public bool) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	main := newBoard()
	main.targets = cfg.Targets
	main.notifyCfg = cfg.Notify
	main.notifiers = notifiersFrom(cfg.Notify)

	return &Server{
		cfgPath:  cfgPath,
		defaults: cfg.Defaults,
		interval: interval,
		tmpl:     tmpl,
		public:   public,
		main:     main,
		sessions: make(map[string]*board),
	}, nil
}

func (s *Server) Public() bool { return s.public }

type platformOption struct{ Key, Label string }

var platformOptions = []platformOption{
	{"slack", "Slack"},
	{"teams", "Microsoft Teams"},
	{"googlechat", "Google Chat"},
	{"discord", "Discord"},
	{"webhook", "Custom webhook (raw JSON)"},
}

func notifiersFrom(n config.Notify) []notify.Notifier {
	var out []notify.Notifier
	for _, c := range n.Channels {
		nt, err := notify.New(c.Platform, c.URL)
		if err != nil {
			log.Printf("notify: skipping %s channel: %v", c.Platform, err)
			continue
		}
		out = append(out, nt)
	}
	return out
}

// --- background checking ---------------------------------------------------

func (s *Server) Watch(ctx context.Context) {
	s.refreshAll()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.refreshAll()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) refreshAll() {
	type job struct {
		b       *board
		targets []config.Target
	}

	s.mu.Lock()
	now := time.Now()
	for sid, b := range s.sessions {
		if now.Sub(b.lastSeen) > sessionTTL {
			delete(s.sessions, sid)
		}
	}
	var jobs []job
	if s.public {
		for _, b := range s.sessions {
			if len(b.targets) > 0 {
				jobs = append(jobs, job{b, append([]config.Target(nil), b.targets...)})
			}
		}
	} else {
		jobs = append(jobs, job{s.main, append([]config.Target(nil), s.main.targets...)})
	}
	s.mu.Unlock()

	for _, j := range jobs {
		checked := checker.RunAll(j.targets)

		// Update Prometheus metrics for every result — regardless of mode.
		// In public mode this aggregates all visitor boards into one view,
		// which is fine for infra-level observability.
		for _, r := range checked {
			labels := prometheus.Labels{"name": r.Target.Name, "url": r.Target.URL}
			up := 0.0
			status := "down"
			if r.Up {
				up = 1.0
				status = "up"
			}
			metrics.TargetUp.With(labels).Set(up)
			metrics.TargetLatency.With(labels).Set(r.Latency.Seconds())
			metrics.TargetCertDays.With(labels).Set(float64(r.CertDaysLeft))
			metrics.ChecksTotal.With(prometheus.Labels{
				"name": r.Target.Name, "url": r.Target.URL, "status": status,
			}).Inc()
		}

		s.mu.Lock()
		events := j.b.reconcile(checked)
		notifiers := append([]notify.Notifier(nil), j.b.notifiers...)
		s.mu.Unlock()

		if len(events) > 0 && len(notifiers) > 0 {
			go func(evts []notify.Event) {
				for _, e := range evts {
					notify.All(notifiers, e)
				}
			}(events)
		}
	}
}

func (b *board) reconcile(checked []checker.Result) []notify.Event {
	fresh := make(map[string]checker.Result, len(checked))
	for _, r := range checked {
		fresh[r.Target.URL] = r
	}
	old := make(map[string]checker.Result, len(b.results))
	for _, r := range b.results {
		old[r.Target.URL] = r
	}

	var events []notify.Event
	results := make([]checker.Result, 0, len(b.targets))
	for _, t := range b.targets {
		r, ok := fresh[t.URL]
		if !ok {
			if r, ok = old[t.URL]; ok {
				results = append(results, r)
			}
			continue
		}
		results = append(results, r)
		b.record(r)
		if prev, seen := old[t.URL]; seen && prev.Up != r.Up {
			events = append(events, b.addIncident(r))
		}
	}
	b.results = results
	b.lastRun = time.Now()
	return events
}

func (b *board) record(r checker.Result) {
	h := append(b.history[r.Target.URL], sample{up: r.Up, ms: r.Latency.Milliseconds()})
	if len(h) > historyLen {
		h = h[len(h)-historyLen:]
	}
	b.history[r.Target.URL] = h
}

func (b *board) addIncident(r checker.Result) notify.Event {
	inc := incident{when: time.Now(), name: r.Target.Name, down: !r.Up}
	switch {
	case r.Up:
		inc.detail = "recovered"
	case r.Err != nil:
		inc.detail = r.Err.Error()
	default:
		inc.detail = fmt.Sprintf("returned %d, expected %d", r.StatusCode, r.Target.ExpectStatus)
	}
	b.incidents = append([]incident{inc}, b.incidents...)
	if len(b.incidents) > 30 {
		b.incidents = b.incidents[:30]
	}
	return notify.Event{Name: r.Target.Name, URL: r.Target.URL,
		Down: inc.down, Detail: inc.detail, At: inc.when}
}

func (s *Server) save() {
	if s.public {
		return // visitor boards are in-memory by design
	}
	cfg := &config.Config{Defaults: s.defaults,
		Notify: s.main.notifyCfg, Targets: s.main.targets}
	if err := config.Save(s.cfgPath, cfg); err != nil {
		log.Printf("warning: could not persist config: %v", err)
	}
}

// --- sessions ---------------------------------------------------------------

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// boardFor returns the right board for this request: the single shared
// board in normal mode, or a per-visitor board in public mode.
func (s *Server) boardFor(w http.ResponseWriter, r *http.Request) *board {
	if !s.public {
		return s.main
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if c, err := r.Cookie("sentinel_sid"); err == nil {
		if b, ok := s.sessions[c.Value]; ok {
			b.lastSeen = time.Now()
			return b
		}
	}

	// Cap: evict the oldest-idle session if full.
	if len(s.sessions) >= maxPublicBoards {
		oldest, oldestID := time.Now(), ""
		for sid, b := range s.sessions {
			if b.lastSeen.Before(oldest) {
				oldest, oldestID = b.lastSeen, sid
			}
		}
		delete(s.sessions, oldestID)
	}

	id := randomID()
	b := newBoard()
	b.lastSeen = time.Now()
	s.sessions[id] = b

	http.SetCookie(w, &http.Cookie{
		Name:     "sentinel_sid",
		Value:    id,
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return b
}

// --- routing ----------------------------------------------------------------

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /partial/board", s.handleBoard)
	mux.HandleFunc("POST /partial/quickcheck", s.handleQuickCheck)
	mux.HandleFunc("POST /partial/monitor", s.handleMonitor)
	mux.HandleFunc("POST /partial/remove", s.handleRemove)
	mux.HandleFunc("POST /partial/notify", s.handleNotifyAdd)
	mux.HandleFunc("POST /partial/notify-remove", s.handleNotifyRemove)
	mux.HandleFunc("POST /api/test-alert", s.handleTestAlert)
	mux.HandleFunc("GET /api/status", s.handleAPI)
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /static/", http.FileServerFS(assets))
	return mux
}

func makeTarget(raw string) (config.Target, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return config.Target{}, fmt.Errorf("empty URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	name := raw
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+3:]
	}
	name = strings.TrimSuffix(name, "/")
	return config.Target{Name: name, URL: raw, ExpectStatus: 200, TimeoutSeconds: 5}, nil
}

// --- view model -------------------------------------------------------------

type statusView struct {
	Name, URL             string
	Up                    bool
	Code, Latency, Detail string
	Cert                  string
	CertWarn              bool
	Removable             bool
	Uptime                string
	Ticks                 []bool
	Spark                 template.HTML
}

type incidentView struct {
	Time, Name, Detail string
	Down               bool
}

type boardView struct {
	Statuses       []statusView
	Incidents      []incidentView
	UpCount, Total int
	LastRun        string
	Interval       string
	Channels       []config.Channel
	Platforms      []platformOption
}

func toView(r checker.Result) statusView {
	sv := statusView{
		Name: r.Target.Name, URL: r.Target.URL, Up: r.Up,
		Code: "-", Latency: r.Latency.Round(time.Millisecond).String(), Cert: "-",
	}
	if r.StatusCode != 0 {
		sv.Code = fmt.Sprintf("%d", r.StatusCode)
	}
	if r.CertDaysLeft >= 0 {
		sv.Cert = fmt.Sprintf("%dd", r.CertDaysLeft)
		sv.CertWarn = r.CertDaysLeft < 14
	}
	switch {
	case r.Err != nil:
		sv.Detail = r.Err.Error()
	case !r.Up && r.StatusCode != 0:
		sv.Detail = fmt.Sprintf("expected status %d", r.Target.ExpectStatus)
	}
	return sv
}

func decorate(sv *statusView, h []sample) {
	if len(h) == 0 {
		return
	}
	up := 0
	for _, p := range h {
		sv.Ticks = append(sv.Ticks, p.up)
		if p.up {
			up++
		}
	}
	sv.Uptime = fmt.Sprintf("%.0f%%", 100*float64(up)/float64(len(h)))
	sv.Spark = sparkline(h)
}

func sparkline(h []sample) template.HTML {
	if len(h) < 2 {
		return ""
	}
	var max int64 = 1
	for _, p := range h {
		if p.ms > max {
			max = p.ms
		}
	}
	const w, ht, pad = 120.0, 28.0, 3.0
	step := w / float64(len(h)-1)
	var b strings.Builder
	for i, p := range h {
		x := float64(i) * step
		y := ht - pad - (float64(p.ms)/float64(max))*(ht-2*pad)
		fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
	}
	line := strings.TrimSpace(b.String())
	area := fmt.Sprintf("0,%.0f %s %.0f,%.0f", ht, line, w, ht)
	return template.HTML(fmt.Sprintf(
		`<svg class="spark" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none"><polygon class="spark-fill" points="%s"/><polyline points="%s"/></svg>`,
		w, ht, area, line))
}

func (s *Server) buildView(b *board) boardView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := boardView{
		Total: len(b.results), LastRun: b.lastRun.Format("15:04:05"),
		Interval: s.interval.String(), Channels: b.notifyCfg.Channels,
		Platforms: platformOptions,
	}
	for _, r := range b.results {
		sv := toView(r)
		sv.Removable = true
		decorate(&sv, b.history[r.Target.URL])
		if sv.Up {
			v.UpCount++
		}
		v.Statuses = append(v.Statuses, sv)
	}
	for _, inc := range b.incidents {
		v.Incidents = append(v.Incidents, incidentView{
			Time: inc.when.Format("15:04:05"), Name: inc.name,
			Down: inc.down, Detail: inc.detail,
		})
	}
	return v
}

// --- handlers ---------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	b := s.boardFor(w, r)
	if err := s.tmpl.ExecuteTemplate(w, "index.html", s.buildView(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	b := s.boardFor(w, r)
	if err := s.tmpl.ExecuteTemplate(w, "board", s.buildView(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleQuickCheck(w http.ResponseWriter, r *http.Request) {
	t, err := makeTarget(r.FormValue("url"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := checker.Run(t)
	if err := s.tmpl.ExecuteTemplate(w, "card", toView(res)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	t, err := makeTarget(r.FormValue("url"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b := s.boardFor(w, r)

	s.mu.RLock()
	exists := false
	for _, existing := range b.targets {
		if existing.URL == t.URL {
			exists = true
			break
		}
	}
	full := s.public && len(b.targets) >= maxPublicTargets
	s.mu.RUnlock()

	if full && !exists {
		http.Error(w,
			fmt.Sprintf("demo limit: max %d targets — remove one first", maxPublicTargets),
			http.StatusBadRequest)
		return
	}

	if !exists {
		res := checker.Run(t)
		s.mu.Lock()
		b.targets = append(b.targets, t)
		b.results = append(b.results, res)
		b.record(res)
		if b.lastRun.IsZero() {
			b.lastRun = time.Now()
		}
		s.save()
		s.mu.Unlock()
	}

	if err := s.tmpl.ExecuteTemplate(w, "board", s.buildView(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	url := r.FormValue("url")
	b := s.boardFor(w, r)

	s.mu.Lock()
	targets := b.targets[:0]
	for _, t := range b.targets {
		if t.URL != url {
			targets = append(targets, t)
		}
	}
	b.targets = targets
	results := b.results[:0]
	for _, res := range b.results {
		if res.Target.URL != url {
			results = append(results, res)
		}
	}
	b.results = results
	delete(b.history, url)
	s.save()
	s.mu.Unlock()

	if err := s.tmpl.ExecuteTemplate(w, "board", s.buildView(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) renderNotifyPanel(w http.ResponseWriter, b *board) {
	if err := s.tmpl.ExecuteTemplate(w, "notify", s.buildView(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleNotifyAdd(w http.ResponseWriter, r *http.Request) {
	platform := r.FormValue("platform")
	url := strings.TrimSpace(r.FormValue("url"))
	if _, err := notify.New(platform, url); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b := s.boardFor(w, r)
	s.mu.Lock()
	exists := false
	for _, c := range b.notifyCfg.Channels {
		if c.URL == url {
			exists = true
			break
		}
	}
	if !exists {
		b.notifyCfg.Channels = append(b.notifyCfg.Channels, config.Channel{Platform: platform, URL: url})
		b.notifiers = notifiersFrom(b.notifyCfg)
		s.save()
	}
	s.mu.Unlock()
	s.renderNotifyPanel(w, b)
}

func (s *Server) handleNotifyRemove(w http.ResponseWriter, r *http.Request) {
	url := r.FormValue("url")
	b := s.boardFor(w, r)
	s.mu.Lock()
	kept := b.notifyCfg.Channels[:0]
	for _, c := range b.notifyCfg.Channels {
		if c.URL != url {
			kept = append(kept, c)
		}
	}
	b.notifyCfg.Channels = kept
	b.notifiers = notifiersFrom(b.notifyCfg)
	s.save()
	s.mu.Unlock()
	s.renderNotifyPanel(w, b)
}

func (s *Server) handleTestAlert(w http.ResponseWriter, r *http.Request) {
	b := s.boardFor(w, r)
	s.mu.RLock()
	notifiers := append([]notify.Notifier(nil), b.notifiers...)
	s.mu.RUnlock()
	if len(notifiers) == 0 {
		http.Error(w, "no webhook configured — paste one and click Add first", http.StatusBadRequest)
		return
	}
	e := notify.Event{Name: "Sentinel test", URL: "your dashboard",
		Detail: "test alert — notifications are working 🎉", At: time.Now()}
	var failed []string
	for _, n := range notifiers {
		if err := n.Send(e); err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		http.Error(w, "send failed: "+strings.Join(failed, "; "), http.StatusBadGateway)
		return
	}
	fmt.Fprintf(w, "test alert sent to %d channel(s) ✓", len(notifiers))
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	b := s.boardFor(w, r)
	s.mu.RLock()
	type apiStatus struct {
		Name         string `json:"name"`
		URL          string `json:"url"`
		Up           bool   `json:"up"`
		StatusCode   int    `json:"status_code"`
		LatencyMs    int64  `json:"latency_ms"`
		CertDaysLeft int    `json:"cert_days_left"`
		Error        string `json:"error,omitempty"`
	}
	out := struct {
		LastRun  time.Time   `json:"last_run"`
		Statuses []apiStatus `json:"statuses"`
	}{LastRun: b.lastRun}
	for _, res := range b.results {
		a := apiStatus{Name: res.Target.Name, URL: res.Target.URL, Up: res.Up,
			StatusCode: res.StatusCode, LatencyMs: res.Latency.Milliseconds(),
			CertDaysLeft: res.CertDaysLeft}
		if res.Err != nil {
			a.Error = res.Err.Error()
		}
		out.Statuses = append(out.Statuses, a)
	}
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
