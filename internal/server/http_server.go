package server

import (
	"bytes"
	"cmp"
	"context"
	"embed"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"os"
	"os/exec"
	"path/filepath"

	"github.com/gorilla/websocket"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/area"
	"local/internal/svc/internal/gamelib/data/difficulty"
	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/bot"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/action"
	ctx "local/internal/svc/internal/context"
	"local/internal/svc/internal/ui"
	"local/internal/svc/internal/drop"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/remote/droplog"
	"local/internal/svc/internal/secrets"
	terrorzones "local/internal/svc/internal/terrorzone"
	"local/internal/svc/internal/updater"
	"local/internal/svc/internal/presenter"
	"local/internal/svc/internal/utils"
	"local/internal/svc/internal/utils/winproc"
	"github.com/lxn/win"
	cp "github.com/otiai10/copy"
	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

// isValidSupervisorName rejects names that could cause path traversal or are otherwise invalid for use in file paths.
func isValidSupervisorName(name string) bool {
	if name == "" {
		return false
	}
	if strings.ContainsAny(name, `<>:"/\|?*`) {
		return false
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	// Reject if cleaned path differs from the name (catches sneaky traversal)
	if filepath.Base(name) != name {
		return false
	}
	return true
}

type HttpServer struct {
	logger              *slog.Logger
	server              *http.Server
	manager             *bot.SupervisorManager
	scheduler           *bot.Scheduler
	templates           *template.Template
	wsServer            *WebSocketServer
	pickitAPI           *PickitAPI
	sequenceAPI         *SequenceAPI
	updater             *updater.Updater
	DropHistory         []DropHistoryEntry
	RunewordHistory     []RunewordHistoryEntry
	DropFilters         map[string]drop.Filters
	DropCardInfo        map[string]dropCardInfo
	DropMux             sync.Mutex
	RunewordMux         sync.Mutex
	autoStartPromptOnce sync.Once
	hwbpReenumCancels   sync.Map // character → context.CancelFunc for running reenum loop
}

var (
	//go:embed all:assets
	assetsFS embed.FS
	//go:embed all:templates
	templatesFS embed.FS

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			host := u.Hostname()
			return host == "localhost" || host == "127.0.0.1"
		},
	}
)

type Client struct {
	conn *websocket.Conn
	send chan []byte
}

type WebSocketServer struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

func NewWebSocketServer() *WebSocketServer {
	return &WebSocketServer{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

type Process struct {
	WindowTitle string `json:"windowTitle"`
	ProcessName string `json:"processName"`
	PID         uint32 `json:"pid"`
}

type dropCardInfo struct {
	ID   int
	Name string
}

func (s *WebSocketServer) Run() {
	for {
		select {
		case client := <-s.register:
			s.clients[client] = true
		case client := <-s.unregister:
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				close(client.send)
			}
		case message := <-s.broadcast:
			for client := range s.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(s.clients, client)
				}
			}
		}
	}
}

func (s *WebSocketServer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("Failed to upgrade connection to WebSocket", "error", err)
		return
	}

	client := &Client{conn: conn, send: make(chan []byte, 256)}
	s.register <- client

	go s.writePump(client)
	go s.readPump(client)
}

func (s *WebSocketServer) writePump(client *Client) {
	defer func() {
		client.conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.send:
			if !ok {
				client.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := client.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}
		}
	}
}

func (s *WebSocketServer) readPump(client *Client) {
	defer func() {
		s.unregister <- client
		client.conn.Close()
	}()

	for {
		_, _, err := client.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("WebSocket read error", "error", err)
			}
			break
		}
	}
}

func (s *HttpServer) BroadcastStatus() {
	for {
		data := s.getStatusData()
		jsonData, err := json.Marshal(data)
		if err != nil {
			slog.Error("Failed to marshal status data", "error", err)
			continue
		}

		s.wsServer.broadcast <- jsonData
		time.Sleep(1 * time.Second)
	}
}

func New(logger *slog.Logger, manager *bot.SupervisorManager, scheduler *bot.Scheduler) (*HttpServer, error) {
	var templates *template.Template
	helperFuncs := template.FuncMap{
		"isInSlice": func(slice []stat.Resist, value string) bool {
			return slices.Contains(slice, stat.Resist(value))
		},
		"isTZSelected": func(slice []area.ID, value int) bool {
			return slices.Contains(slice, area.ID(value))
		},
		"executeTemplateByName": func(name string, data interface{}) template.HTML {
			tmpl := templates.Lookup(name)
			var buf bytes.Buffer
			if tmpl == nil {
				return "This run is not configurable."
			}

			tmpl.Execute(&buf, data)
			return template.HTML(buf.String())
		},
		"runDisplayName": func(run string) string {
			switch run {
			case string(config.OrgansRun):
				return "Uber (Organs)"
			case string(config.PandemoniumRun):
				return "Uber (Torch)"
			default:
				return run
			}
		},
		"qualityClass": qualityClass,
		"statIDToText": statIDToText,
		"contains":     containss,
		"seq": func(start, end int) []int {
			var result []int
			for i := start; i <= end; i++ {
				result = append(result, i)
			}
			return result
		},
		"allImmunities": func() []string {
			return []string{"f", "c", "l", "p", "ph", "m"}
		},
		"upper": strings.ToUpper,
		"trim":  strings.TrimSpace,
		"isLevelingBuild": func(build string) bool {
			if strings.HasSuffix(build, "_leveling") {
				return true
			}
			switch build {
			case "paladin", "necromancer", "assassin", "barb_leveling":
				return true
			default:
				return false
			}
		},
		"toJSON": func(v interface{}) template.JS {
			b, err := json.Marshal(v)
			if err != nil {
				return template.JS("{}")
			}
			return template.JS(b)
		},
		// Armory template helpers
		"iterate": func(count int) []int {
			result := make([]int, count)
			for i := range result {
				result[i] = i
			}
			return result
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"json": func(v interface{}) template.JS {
			b, err := json.Marshal(v)
			if err != nil {
				return template.JS("{}")
			}
			return template.JS(b)
		},
		"lower": strings.ToLower,
	}
	templates, err := template.New("").Funcs(helperFuncs).ParseFS(templatesFS, "templates/*.gohtml")
	if err != nil {
		return nil, err
	}

	// Debug: List all loaded templates
	logger.Info("Loaded templates:")
	for _, t := range templates.Templates() {
		logger.Info("  - " + t.Name())
	}

	server := &HttpServer{
		logger:       logger,
		manager:      manager,
		scheduler:    scheduler,
		templates:    templates,
		pickitAPI:    NewPickitAPI(),
		sequenceAPI:  NewSequenceAPI(logger),
		updater:      updater.NewUpdater(logger),
		DropFilters:  make(map[string]drop.Filters),
		DropCardInfo: make(map[string]dropCardInfo),
	}

	server.updater.SetPreRestartCallback(func() error {
		server.logger.Info("Stopping HTTP server before restart")
		return server.Stop()
	})

	server.initDropCallbacks()
	return server, nil
}

func (s *HttpServer) getProcessList(w http.ResponseWriter, r *http.Request) {
	processes, err := getRunningProcesses()
	if err != nil {
		http.Error(w, "Failed to get process list", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(processes)
}

func (s *HttpServer) attachProcess(w http.ResponseWriter, r *http.Request) {
	characterName := r.URL.Query().Get("characterName")
	pidStr := r.URL.Query().Get("pid")

	pid, err := strconv.ParseUint(pidStr, 10, 32)
	if err != nil {
		s.logger.Error("Invalid PID", "error", err)
		return
	}

	// Find the main window handle (HWND) for the process
	var hwnd win.HWND
	enumWindowsCallback := func(h win.HWND, param uintptr) uintptr {
		var processID uint32
		win.GetWindowThreadProcessId(h, &processID)
		if processID == uint32(pid) {
			hwnd = h
			return 0 // Stop enumeration
		}
		return 1 // Continue enumeration
	}

	windows.EnumWindows(syscall.NewCallback(enumWindowsCallback), nil)

	if hwnd == 0 {
		s.logger.Error("Failed to find window handle for process", "pid", pid)
		return
	}

	// Call manager.Start with the correct arguments, including the HWND
	go s.manager.Start(characterName, true, false, uint32(pid), uint32(hwnd))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// Add this helper function
func getRunningProcesses() ([]Process, error) {
	var processes []Process

	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	err = windows.Process32First(snapshot, &entry)
	if err != nil {
		return nil, err
	}

	for {
		windowTitle, _ := getWindowTitle(entry.ProcessID)

		if strings.EqualFold(syscall.UTF16ToString(entry.ExeFile[:]), utils.GameExeName()) {
			processes = append(processes, Process{
				WindowTitle: windowTitle,
				ProcessName: syscall.UTF16ToString(entry.ExeFile[:]),
				PID:         entry.ProcessID,
			})
		}

		err = windows.Process32Next(snapshot, &entry)
		if err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return nil, err
		}
	}

	return processes, nil
}

func getWindowTitle(pid uint32) (string, error) {
	var windowTitle string
	var hwnd windows.HWND

	cb := syscall.NewCallback(func(h win.HWND, param uintptr) uintptr {
		var currentPID uint32
		_ = win.GetWindowThreadProcessId(h, &currentPID)

		if currentPID == pid {
			hwnd = windows.HWND(h)
			return 0 // stop enumeration
		}
		return 1 // continue enumeration
	})

	// Enumerate all windows
	windows.EnumWindows(cb, nil)

	if hwnd == 0 {
		return "", fmt.Errorf("no window found for process ID %d", pid)
	}

	// Get window title
	var title [256]uint16
	_, _, _ = winproc.GetWindowText.Call(
		uintptr(hwnd),
		uintptr(unsafe.Pointer(&title[0])),
		uintptr(len(title)),
	)

	windowTitle = syscall.UTF16ToString(title[:])
	return windowTitle, nil

}

func qualityClass(quality string) string {
	switch quality {
	case "LowQuality":
		return "low-quality"
	case "Normal":
		return "normal-quality"
	case "Superior":
		return "superior-quality"
	case "Magic":
		return "magic-quality"
	case "Set":
		return "set-quality"
	case "Rare":
		return "rare-quality"
	case "Unique":
		return "unique-quality"
	case "Crafted":
		return "crafted-quality"
	default:
		return "unknown-quality"
	}
}

func statIDToText(id stat.ID) string {
	return stat.StringStats[id]
}

func containss(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func formatCommitDate(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("2006-01-02 15:04:05")
}

func resolveSkillClassFromBuild(build string) string {
	switch build {
	case "amazon_leveling", "javazon":
		return "ama"
	case "sorceress", "nova", "hydraorb", "lightsorc", "fireballsorc", "sorceress_leveling":
		return "sor"
	case "necromancer":
		return "nec"
	case "paladin", "hammerdin", "foh", "dragondin", "smiter":
		return "pal"
	case "barb_leveling", "berserker", "warcry_barb":
		return "bar"
	case "druid_leveling", "winddruid":
		return "dru"
	case "assassin", "trapsin", "mosaic":
		return "ass"
	default:
		return ""
	}
}

func buildSkillOptionsForBuild(build string) []SkillOption {
	classKey := resolveSkillClassFromBuild(build)
	options := make([]SkillOption, 0)
	for id, sk := range skill.Skills {
		if sk.Class == "" {
			continue
		}
		if classKey != "" && sk.Class != classKey {
			continue
		}
		key := skill.SkillNames[id]
		name := sk.Name
		if name == "" {
			name = key
		}
		options = append(options, SkillOption{Key: key, Name: name})
	}
	sort.Slice(options, func(i, j int) bool {
		return options[i].Name < options[j].Name
	})
	return options
}

func buildSkillPrereqsForBuild(build string) map[string][]string {
	classKey := resolveSkillClassFromBuild(build)
	nameToKey := make(map[string]string)
	for id, sk := range skill.Skills {
		key := skill.SkillNames[id]
		if key == "" {
			continue
		}
		nameToKey[strings.ToLower(key)] = key
		if sk.Name != "" {
			nameToKey[strings.ToLower(sk.Name)] = key
		}
	}

	prereqs := make(map[string][]string)
	for id, sk := range skill.Skills {
		if sk.Class == "" {
			continue
		}
		if classKey != "" && sk.Class != classKey {
			continue
		}
		key := skill.SkillNames[id]
		if key == "" {
			continue
		}
		reqs := make([]string, 0, 2)
		for _, reqName := range []string{sk.ReqSkill1, sk.ReqSkill2} {
			if reqName == "" {
				continue
			}
			if reqKey, ok := nameToKey[strings.ToLower(reqName)]; ok {
				reqs = append(reqs, reqKey)
			}
		}
		if len(reqs) > 0 {
			prereqs[key] = reqs
		}
	}

	return prereqs
}

func (s *HttpServer) updateAutoStatSkillFromForm(values url.Values, cfg *config.CharacterCfg) {
	oldRespec := cfg.Character.AutoStatSkill.Respec

	cfg.Character.AutoStatSkill.Enabled = values.Has("autoStatSkillEnabled")
	cfg.Character.AutoStatSkill.ExcludeQuestStats = values.Has("autoStatSkillExcludeQuestStats")
	cfg.Character.AutoStatSkill.ExcludeQuestSkills = values.Has("autoStatSkillExcludeQuestSkills")

	statKeys := values["autoStatSkillStat[]"]
	statTargets := values["autoStatSkillStatTarget[]"]
	stats := make([]config.AutoStatSkillStat, 0, len(statKeys))
	for i, statKey := range statKeys {
		if i >= len(statTargets) {
			break
		}
		statKey = strings.TrimSpace(statKey)
		if statKey == "" {
			continue
		}
		target, err := strconv.Atoi(strings.TrimSpace(statTargets[i]))
		if err != nil || target <= 0 {
			continue
		}
		stats = append(stats, config.AutoStatSkillStat{Stat: statKey, Target: target})
	}
	cfg.Character.AutoStatSkill.Stats = stats

	skillKeys := values["autoStatSkillSkill[]"]
	skillTargets := values["autoStatSkillSkillTarget[]"]
	skills := make([]config.AutoStatSkillSkill, 0, len(skillKeys))
	for i, skillKey := range skillKeys {
		if i >= len(skillTargets) {
			break
		}
		skillKey = strings.TrimSpace(skillKey)
		if skillKey == "" {
			continue
		}
		target, err := strconv.Atoi(strings.TrimSpace(skillTargets[i]))
		if err != nil || target <= 0 {
			continue
		}
		skills = append(skills, config.AutoStatSkillSkill{Skill: skillKey, Target: target})
	}
	cfg.Character.AutoStatSkill.Skills = skills

	respecEnabled := values.Has("autoRespecEnabled") && cfg.Character.AutoStatSkill.Enabled
	targetLevel := 0
	if raw := strings.TrimSpace(values.Get("autoRespecTargetLevel")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			if n < 0 {
				n = 0
			} else if n > 0 && n < 2 {
				n = 2
			} else if n > 99 {
				n = 99
			}
			targetLevel = n
		}
	}
	cfg.Character.AutoStatSkill.Respec.Enabled = respecEnabled
	cfg.Character.AutoStatSkill.Respec.TokenFirst = values.Has("autoRespecTokenFirst") && respecEnabled
	cfg.Character.AutoStatSkill.Respec.TargetLevel = targetLevel

	if !respecEnabled {
		cfg.Character.AutoStatSkill.Respec.Applied = false
	} else if !oldRespec.Enabled || oldRespec.TargetLevel != targetLevel {
		cfg.Character.AutoStatSkill.Respec.Applied = false
	}
}

func (s *HttpServer) initialData(w http.ResponseWriter, r *http.Request) {
	data := s.getStatusData()

	skipPrompt := r.URL.Query().Get("skipAutoStartPrompt") == "true"

	// Decide whether to show the auto-start confirmation prompt.
	// This should only happen once per program run, on the first
	// dashboard load where global auto-start is enabled and at
	// least one character is marked for auto-start.
	showPrompt := false
	if !skipPrompt && data.GlobalAutoStartEnabled {
		hiddenSet := make(map[string]struct{}, len(data.HiddenSupervisors))
		for _, name := range data.HiddenSupervisors {
			hiddenSet[name] = struct{}{}
		}

		s.autoStartPromptOnce.Do(func() {
			for name, enabled := range data.AutoStart {
				if !enabled {
					continue
				}
				if _, hidden := hiddenSet[name]; hidden {
					continue
				}
				showPrompt = true
				break
			}
		})
	}
	data.ShowAutoStartPrompt = showPrompt

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *HttpServer) getStatusData() IndexData {
	status := make(map[string]bot.Stats)
	drops := make(map[string]int)
	autoStart := make(map[string]bool)
	supervisors := config.OrderedSupervisors()
	hiddenSupervisors := config.HiddenSupervisors()

	for _, supervisorName := range supervisors {
		stats := s.manager.Status(supervisorName)

		// Enrich with lightweight live character overview for UI
		if data := s.manager.GetData(supervisorName); data != nil {
			// Defaults
			var lvl, life, maxLife, mana, maxMana, mf, gold, gf int
			var exp, lastExp, nextExp uint64
			var fr, cr, lr, pr int
			var mfr, mcr, mlr, mpr int

			if v, ok := data.PlayerUnit.FindStat(stat.Level, 0); ok {
				lvl = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.Experience, 0); ok {
				// Treat as unsigned to handle values > 2^31-1
				exp = uint64(uint32(v.Value))
			}
			if v, ok := data.PlayerUnit.FindStat(stat.LastExp, 0); ok {
				// Treat as unsigned to handle values > 2^31-1
				lastExp = uint64(uint32(v.Value))
			}
			if v, ok := data.PlayerUnit.FindStat(stat.NextExp, 0); ok {
				// Treat as unsigned to handle values > 2^31-1
				nextExp = uint64(uint32(v.Value))
			}
			if v, ok := data.PlayerUnit.FindStat(stat.Life, 0); ok {
				life = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.MaxLife, 0); ok {
				maxLife = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.Mana, 0); ok {
				mana = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.MaxMana, 0); ok {
				maxMana = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.MagicFind, 0); ok {
				mf = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.GoldFind, 0); ok {
				gf = v.Value
			}

			gold = data.PlayerUnit.TotalPlayerGold()

			if v, ok := data.PlayerUnit.FindStat(stat.FireResist, 0); ok {
				fr = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.ColdResist, 0); ok {
				cr = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.LightningResist, 0); ok {
				lr = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.PoisonResist, 0); ok {
				pr = v.Value
			}
			// Max resists (increase cap)
			if v, ok := data.PlayerUnit.FindStat(stat.MaxFireResist, 0); ok {
				mfr = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.MaxColdResist, 0); ok {
				mcr = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.MaxLightningResist, 0); ok {
				mlr = v.Value
			}
			if v, ok := data.PlayerUnit.FindStat(stat.MaxPoisonResist, 0); ok {
				mpr = v.Value
			}

			// Apply difficulty penalty and cap to compute current/effective resists
			penalty := 0
			switch data.CharacterCfg.Game.Difficulty {
			case difficulty.Nightmare:
				penalty = 40
			case difficulty.Hell:
				penalty = 100
			}
			capFR := 75 + mfr
			capCR := 75 + mcr
			capLR := 75 + mlr
			capPR := 75 + mpr
			if fr-penalty > capFR {
				fr = capFR
			} else {
				fr = fr - penalty
			}
			if cr-penalty > capCR {
				cr = capCR
			} else {
				cr = cr - penalty
			}
			if lr-penalty > capLR {
				lr = capLR
			} else {
				lr = lr - penalty
			}
			if pr-penalty > capPR {
				pr = capPR
			} else {
				pr = pr - penalty
			}

			// Resolve difficulty and area names
			diffStr := fmt.Sprint(data.CharacterCfg.Game.Difficulty)
			areaStr := ""
			// Prefer human-readable area name if available
			if lvl := data.PlayerUnit.Area.Area(); lvl.Name != "" {
				areaStr = lvl.Name
			} else {
				areaStr = fmt.Sprint(data.PlayerUnit.Area)
			}

			stats.UI = bot.CharacterOverview{
				Class:           data.CharacterCfg.Character.Class,
				Level:           lvl,
				Experience:      exp,
				LastExp:         lastExp,
				NextExp:         nextExp,
				Difficulty:      diffStr,
				Area:            areaStr,
				Ping:            data.Game.Ping,
				Life:            life,
				MaxLife:         maxLife,
				Mana:            mana,
				MaxMana:         maxMana,
				MagicFind:       mf,
				Gold:            gold,
				GoldFind:        gf,
				FireResist:      fr,
				ColdResist:      cr,
				LightningResist: lr,
				PoisonResist:    pr,
			}
		}

		// Expose game name/password for party and lobby game bots
		if data := s.manager.GetData(supervisorName); data != nil {
			stats.GameName = data.Game.LastGameName
			stats.GamePassword = data.Game.LastGamePassword
		}

		// Check if this is a companion follower & ensure we always expose class
		cfg, found := config.GetCharacter(supervisorName)
		if found {
			if stats.UI.Class == "" {
				stats.UI.Class = cfg.Character.Class
			}
			// Add companion information to the stats
			if cfg.Companion.Enabled {
				if cfg.Companion.Leader {
					stats.PartyRole = "leader"
				} else {
					stats.IsCompanionFollower = true
					stats.PartyRole = "follower"
					stats.PartyLeaderName = cfg.Companion.LeaderName
				}
			}
			stats.MuleEnabled = cfg.Muling.Enabled

			// Per-character Auto Start flag
			autoStart[supervisorName] = cfg.AutoStart
		}

		status[supervisorName] = stats

		if s.manager.GetSupervisorStats(supervisorName).Drops != nil {
			drops[supervisorName] = len(s.manager.GetSupervisorStats(supervisorName).Drops)
		} else {
			drops[supervisorName] = 0
		}
	}

	// Collect scheduler status for each supervisor
	schedulerStatus := make(map[string]*SchedulerStatusInfo)
	if s.scheduler != nil {
		for _, supervisorName := range supervisors {
			cfg := config.GetCharacters()[supervisorName]
			if cfg == nil {
				continue
			}

			info := &SchedulerStatusInfo{
				Enabled: cfg.Scheduler.Enabled,
				Mode:    cfg.Scheduler.Mode,
			}

			// For duration mode, get live state from scheduler
			if cfg.Scheduler.Mode == "duration" && cfg.Scheduler.Enabled {
				state := s.scheduler.GetDurationState(supervisorName)
				if state != nil {
					info.Phase = string(state.CurrentPhase)
					info.PhaseStartTime = state.PhaseStartTime.Format(time.RFC3339)
					info.PhaseEndTime = state.PhaseEndTime.Format(time.RFC3339)
					info.TodayWakeTime = state.TodayWakeTime.Format(time.RFC3339)
					info.TodayRestTime = state.TodayRestTime.Format(time.RFC3339)
					info.PlayedMinutes = state.PlayedMinutes

					// Get next 3 breaks
					nextBreaks := []SchedulerBreak{}
					now := time.Now()
					for i := state.CurrentBreakIdx; i < len(state.ScheduledBreaks) && len(nextBreaks) < 3; i++ {
						brk := state.ScheduledBreaks[i]
						if brk.StartTime.After(now) {
							nextBreaks = append(nextBreaks, SchedulerBreak{
								Type:      brk.Type,
								StartTime: brk.StartTime.Format(time.RFC3339),
								Duration:  brk.Duration,
							})
						}
					}
					info.NextBreaks = nextBreaks
				}
			}

			schedulerStatus[supervisorName] = info
		}
	}

	return IndexData{
		Version:                     config.Version,
		Supervisors:                 supervisors,
		HiddenSupervisors:           hiddenSupervisors,
		Status:                      status,
		DropCount:                   drops,
		AutoStart:                   autoStart,
		SchedulerStatus:             schedulerStatus,
		GlobalAutoStartEnabled:      config.App.AutoStart.Enabled,
		GlobalAutoStartDelaySeconds: config.App.AutoStart.DelaySeconds,
	}
}

func (s *HttpServer) Listen(port int) error {
	s.wsServer = NewWebSocketServer()
	go s.wsServer.Run()
	go s.BroadcastStatus()

	http.HandleFunc("/", s.getRoot)
	http.HandleFunc("/config", s.config)
	http.HandleFunc("/supervisorSettings", s.characterSettings)
	http.HandleFunc("/runewords", s.runewordSettings)
	http.HandleFunc("/api/runewords/rolls", s.runewordRolls)
	http.HandleFunc("/api/runewords/base-types", s.runewordBaseTypes)
	http.HandleFunc("/api/runewords/bases", s.runewordBases)
	http.HandleFunc("/api/runewords/history", s.runewordHistory)
	http.HandleFunc("/start", s.startSupervisor)
	http.HandleFunc("/stop", s.stopSupervisor)
	http.HandleFunc("/togglePause", s.togglePause)
	http.HandleFunc("/autostart/toggle", s.toggleAutoStart)
	http.HandleFunc("/autostart/run-once", s.runAutoStartOnce)
	http.HandleFunc("/debug", s.debugHandler)
	http.HandleFunc("/debug-data", s.debugData)
	http.HandleFunc("/debug/sendpacket", s.debugSendPacket)
	http.HandleFunc("/debug/click", s.debugClick)
	http.HandleFunc("/debug/hidclick", s.debugHIDClick)
	http.HandleFunc("/debug/walkpacket", s.debugWalkPacket)
	http.HandleFunc("/debug/senduipacket-apc", s.debugSendUIPacketAPC)
	http.HandleFunc("/debug/set-game-tid", s.debugSetGameTID)
	http.HandleFunc("/debug/presskey", s.debugPressKey)
	http.HandleFunc("/debug/pressrawkey", s.debugPressRawKey)
	http.HandleFunc("/debug/gamestate", s.debugGameState)
	http.HandleFunc("/debug/npcs", s.debugNPCs)
	http.HandleFunc("/debug/inventory", s.debugInventory)
	http.HandleFunc("/debug/screenshot", s.debugScreenshot)
	http.HandleFunc("/debug/panels", s.debugPanels)
	http.HandleFunc("/debug/readmem", s.debugReadMem)
	http.HandleFunc("/debug/rpm-counter", s.debugRPMCounter)
	http.HandleFunc("/debug/rop-scan", s.debugRopScan)
	http.HandleFunc("/debug/rop-read", s.debugRopRead)
	http.HandleFunc("/debug/rop-read-batch", s.debugRopReadBatch)
	http.HandleFunc("/debug/rop-worker-hb", s.debugRopWorkerHb)
	http.HandleFunc("/debug/rop-dbg", s.debugRopDbg)
	http.HandleFunc("/debug/read-trace", s.debugReadTrace)
	http.HandleFunc("/debug/read-trace-stats", s.debugReadTraceStats)
	http.HandleFunc("/debug/read-trace-enable", s.debugReadTraceEnable)
	http.HandleFunc("/debug/dispatch-ping", s.debugDispatchPing)
	http.HandleFunc("/debug/handle-audit", s.debugHandleAudit)
	http.HandleFunc("/debug/writemem", s.debugWriteMem)
	http.HandleFunc("/debug/memdiff", s.debugMemDiff)
	http.HandleFunc("/debug/dumprange", s.debugDumpRange)
	http.HandleFunc("/debug/scanmem", s.debugScanMem)
	http.HandleFunc("/debug/snifflog", s.debugSniffLog)
	http.HandleFunc("/debug/sniff/install", s.debugSniffInstall)
	http.HandleFunc("/debug/sniff/uninstall", s.debugSniffUninstall)
	http.HandleFunc("/debug/hwbp/install", s.debugHwbpInstall)
	http.HandleFunc("/debug/hwbp/uninstall", s.debugHwbpUninstall)
	http.HandleFunc("/debug/hwbp/verify", s.debugHwbpVerify)
	http.HandleFunc("/debug/hwbp/reenum", s.debugHwbpReenum)
	http.HandleFunc("/debug/hwbp/status", s.debugHwbpStatus)
	http.HandleFunc("/debug/hwbp/drain", s.debugHwbpDrain)
	http.HandleFunc("/debug/drprobe", s.debugDrProbe)
	http.HandleFunc("/debug/callfn", s.debugCallFn)
	http.HandleFunc("/debug/callfn-gt", s.debugCallFnGT)
	http.HandleFunc("/debug/inproc-writemem", s.debugInprocWriteMem)
	http.HandleFunc("/debug/unlock-cursor", s.debugUnlockCursor)
	http.HandleFunc("/debug/capture/start", s.debugCaptureStart)
	http.HandleFunc("/debug/capture/stop", s.debugCaptureStop)
	http.HandleFunc("/debug/capture/drain", s.debugCaptureDrain)
	http.HandleFunc("/debug/capture/stats", s.debugCaptureStats)
	http.HandleFunc("/debug/packettrace/install", s.debugTraceInstall)
	http.HandleFunc("/debug/packettrace/uninstall", s.debugTraceUninstall)
	http.HandleFunc("/debug/packettrace/dump", s.debugTraceDump)
	http.HandleFunc("/debug/packettrace/status", s.debugTraceStatus)
	// Capture hook (Discord 2026-04-15 approach — inline JMP on send_fn in rmod.dll)
	// Paths distinct from /debug/capture/{start,stop,drain,stats} which are the
	// legacy bufpoll-polling path.
	http.HandleFunc("/debug/caphook/install", s.debugCaptureHookInstall)
	http.HandleFunc("/debug/caphook/uninstall", s.debugCaptureHookUninstall)
	http.HandleFunc("/debug/caphook/drain", s.debugCaptureHookDrain)
	http.HandleFunc("/debug/caphook/status", s.debugCaptureHookStatus)
	http.HandleFunc("/debug/crash-info", s.debugCrashInfo)
	http.HandleFunc("/debug/clickworld", s.debugClickWorld)
	http.HandleFunc("/debug/clickitem", s.debugClickItem)
	http.HandleFunc("/debug/movetocoords", s.debugMoveToCoords)
	http.HandleFunc("/debug/test-stash-packet", s.debugTestStashPacket)
	http.HandleFunc("/claude-attach", s.claudeAttach)
	http.HandleFunc("/shutdown", s.shutdown)
	http.HandleFunc("/drops", s.drops)
	http.HandleFunc("/all-drops", s.allDrops)
	http.HandleFunc("/export-drops", s.exportDrops)
	http.HandleFunc("/open-droplogs", s.openDroplogs)
	http.HandleFunc("/reset-droplogs", s.resetDroplogs)
	http.HandleFunc("/process-list", s.getProcessList)
	http.HandleFunc("/attach-process", s.attachProcess)
	http.HandleFunc("/ws", s.wsServer.HandleWebSocket)                         // Web socket
	http.HandleFunc("/initial-data", s.initialData)                            // Web socket data
	http.HandleFunc("/api/reload-config", s.reloadConfig)
	http.HandleFunc("/api/supervisors/reorder", s.reorderSupervisors)
	http.HandleFunc("/api/supervisors/hide", s.hideSupervisor)
	http.HandleFunc("/api/supervisors/unhide", s.unhideSupervisor)
	http.HandleFunc("/api/supervisors/rename", s.renameSupervisorConfig)
	http.HandleFunc("/api/supervisors/copy", s.copySupervisorConfig)
	http.HandleFunc("/api/supervisors/delete", s.deleteSupervisorConfig)
	http.HandleFunc("/api/party/set-leader", s.partySetLeader)
	http.HandleFunc("/api/party/set-follower", s.partySetFollower)
	http.HandleFunc("/api/party/remove", s.partyRemove)
	http.HandleFunc("/api/gen-session-token", s.generateBattleNetToken) // Battle.net token generation
	http.HandleFunc("/reset-muling", s.resetMuling)

	// Updater routes
	http.HandleFunc("/api/updater/version", s.getVersion)
	http.HandleFunc("/api/updater/check", s.checkUpdates)
	http.HandleFunc("/api/updater/current-commits", s.getCurrentCommits)
	http.HandleFunc("/api/updater/update", s.performUpdate)
	http.HandleFunc("/api/updater/status", s.getUpdaterStatus)
	http.HandleFunc("/api/updater/backups", s.getBackups)
	http.HandleFunc("/api/updater/rollback", s.performRollback)
	http.HandleFunc("/api/updater/prs", s.getUpstreamPRs)
	http.HandleFunc("/api/updater/cherry-pick", s.cherryPickPRs)
	http.HandleFunc("/api/updater/prs/revert", s.revertPR)

	// Pickit Editor routes
	http.HandleFunc("/pickit-editor", s.pickitEditorPage)
	http.HandleFunc("/sequence-editor", s.sequenceEditorPage)
	http.HandleFunc("/api/pickit/items", s.pickitAPI.handleGetItems)
	http.HandleFunc("/api/pickit/items/search", s.pickitAPI.handleSearchItems)
	http.HandleFunc("/api/pickit/items/categories", s.pickitAPI.handleGetCategories)
	http.HandleFunc("/api/pickit/stats", s.pickitAPI.handleGetStats)
	http.HandleFunc("/api/pickit/templates", s.pickitAPI.handleGetTemplates)
	http.HandleFunc("/api/pickit/presets", s.pickitAPI.handleGetPresets)
	http.HandleFunc("/api/pickit/rules", s.pickitAPI.handleGetRules)
	http.HandleFunc("/api/pickit/rules/create", s.pickitAPI.handleCreateRule)
	http.HandleFunc("/api/pickit/rules/update", s.pickitAPI.handleUpdateRule)
	http.HandleFunc("/api/pickit/rules/delete", s.pickitAPI.handleDeleteRule)
	http.HandleFunc("/api/pickit/rules/validate", s.pickitAPI.handleValidateRule)
	http.HandleFunc("/api/pickit/rules/validate-nip", s.pickitAPI.handleValidateNIPLine)
	http.HandleFunc("/api/pickit/files", s.pickitAPI.handleGetFiles)
	http.HandleFunc("/api/pickit/files/import", s.pickitAPI.handleImportFile)
	http.HandleFunc("/api/pickit/files/export", s.pickitAPI.handleExportFile)
	http.HandleFunc("/api/pickit/files/rules/delete", s.pickitAPI.handleDeleteFileRule)
	http.HandleFunc("/api/pickit/files/rules/update", s.pickitAPI.handleUpdateFileRule)
	http.HandleFunc("/api/pickit/files/rules/append", s.pickitAPI.handleAppendNIPLine)
	http.HandleFunc("/api/pickit/browse-folder", s.pickitAPI.handleBrowseFolder)
	http.HandleFunc("/api/pickit/simulate", s.pickitAPI.handleSimulate)
	http.HandleFunc("/api/sequence-editor/runs", s.sequenceAPI.handleListRuns)
	http.HandleFunc("/api/sequence-editor/file", s.sequenceAPI.handleGetSequence)
	http.HandleFunc("/api/sequence-editor/open", s.sequenceAPI.handleBrowseSequence)
	http.HandleFunc("/api/sequence-editor/save", s.sequenceAPI.handleSaveSequence)
	http.HandleFunc("/api/sequence-editor/delete", s.sequenceAPI.handleDeleteSequence)
	http.HandleFunc("/api/sequence-editor/files", s.sequenceAPI.handleListSequenceFiles)
	http.HandleFunc("/api/skill-options", s.skillOptionsAPI)

	http.HandleFunc("/api/supervisors/bulk-apply", s.bulkApplyCharacterSettings)
	http.HandleFunc("/api/scheduler-history", s.schedulerHistory)
	http.HandleFunc("/Drop-manager", s.DropManagerPage)

	// Armory routes
	http.HandleFunc("/armory", s.armoryPage)
	http.HandleFunc("/api/armory", s.armoryAPI)
	http.HandleFunc("/api/armory/characters", s.armoryCharactersAPI)
	// http.HandleFunc("/api/armory/all", s.armoryAllAPI) // Commented out - method not available in this version

	// Traderie integration routes
	http.HandleFunc("/api/traderie/search", s.traderieSearchItems)
	http.HandleFunc("/api/traderie/listings", s.traderieGetListings)
	http.HandleFunc("/api/traderie/create-listing", s.traderieCreateListing)
	http.HandleFunc("/api/traderie/status", s.traderieGetCookieStatus)
	http.HandleFunc("/api/traderie/cookies", s.traderieSetCookies)

	s.registerDropRoutes()

	assets, _ := fs.Sub(assetsFS, "assets")
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))

	// Serve item images from the filesystem (assets/items folder relative to executable)
	http.Handle("/items/", http.StripPrefix("/items/", http.FileServer(http.Dir("../assets/items"))))

	s.server = &http.Server{
		Addr: fmt.Sprintf("127.0.0.1:%d", port),
	}

	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

func (s *HttpServer) reloadConfig(w http.ResponseWriter, r *http.Request) {
	result := s.manager.ReloadConfig()
	if result != nil {
		http.Error(w, result.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("Config reloaded")
	w.WriteHeader(http.StatusOK)
}

// SchedulerHistoryEntry matches bot.HistoryEntry for JSON serialization
type SchedulerHistoryEntry struct {
	Date              string                `json:"date"`
	WakeTime          string                `json:"wakeTime"`
	SleepTime         string                `json:"sleepTime"`
	TotalPlayMinutes  int                   `json:"totalPlayMinutes"`
	TotalBreakMinutes int                   `json:"totalBreakMinutes"`
	Breaks            []SchedulerBreakEntry `json:"breaks"`
}

type SchedulerBreakEntry struct {
	Type      string `json:"type"`
	StartTime string `json:"startTime"`
	Duration  int    `json:"duration"`
}

type SchedulerHistoryResponse struct {
	History []SchedulerHistoryEntry `json:"history"`
}

func (s *HttpServer) schedulerHistory(w http.ResponseWriter, r *http.Request) {
	supervisor := r.URL.Query().Get("supervisor")
	if supervisor == "" {
		http.Error(w, "supervisor parameter required", http.StatusBadRequest)
		return
	}
	if !isValidSupervisorName(supervisor) {
		http.Error(w, "Invalid supervisor name", http.StatusBadRequest)
		return
	}

	// Read history file directly (same path as scheduler uses)
	historyPath := filepath.Join("config", supervisor, "scheduler_history.json")
	data, err := os.ReadFile(historyPath)
	if err != nil {
		// No history yet - return empty array
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SchedulerHistoryResponse{History: []SchedulerHistoryEntry{}})
		return
	}

	// Parse and return
	var history SchedulerHistoryResponse
	if err := json.Unmarshal(data, &history); err != nil {
		s.logger.Error("Failed to parse scheduler history", "error", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SchedulerHistoryResponse{History: []SchedulerHistoryEntry{}})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}

func (s *HttpServer) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

func (s *HttpServer) getRoot(w http.ResponseWriter, r *http.Request) {
	if !utils.HasAdminPermission() {
		s.templates.ExecuteTemplate(w, "templates/admin_required.gohtml", nil)
		return
	}

	if config.App.FirstRun {
		http.Redirect(w, r, "/config", http.StatusSeeOther)
		return
	}

	s.index(w)
}

func (s *HttpServer) debugData(w http.ResponseWriter, r *http.Request) {
	characterName := r.URL.Query().Get("characterName")
	if characterName == "" {
		http.Error(w, "Character name is required", http.StatusBadRequest)
		return
	}

	type DebugData struct {
		DebugData map[ctx.Priority]*ctx.Debug
		GameData  *game.Data
	}

	context := s.manager.GetContext(characterName)

	debugData := DebugData{
		DebugData: context.ContextDebug,
		GameData:  context.Data,
	}

	jsonData, err := json.Marshal(debugData)
	if err != nil {
		http.Error(w, "Failed to serialize game data", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonData)
}

func (s *HttpServer) debugHandler(w http.ResponseWriter, r *http.Request) {
	s.templates.ExecuteTemplate(w, "debug.gohtml", nil)
}

func (s *HttpServer) pickitEditorPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Try without templates/ prefix first (like debug.gohtml)
	err := s.templates.ExecuteTemplate(w, "pickit_editor.gohtml", nil)
	if err != nil {
		// If that fails, log what templates we have
		s.logger.Error("Failed to execute pickit_editor template", "error", err)
		s.logger.Info("Available templates:")
		for _, t := range s.templates.Templates() {
			s.logger.Info("  - " + t.Name())
		}
		http.Error(w, fmt.Sprintf("Template error: %v", err), http.StatusInternalServerError)
		return
	}
}

func (s *HttpServer) sequenceEditorPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := s.templates.ExecuteTemplate(w, "sequence_editor.gohtml", nil); err != nil {
		s.logger.Error("Failed to execute sequence_editor template", "error", err)
		http.Error(w, fmt.Sprintf("Template error: %v", err), http.StatusInternalServerError)
		return
	}
}

func (s *HttpServer) startSupervisor(w http.ResponseWriter, r *http.Request) {
	supervisorList := s.manager.AvailableSupervisors()
	supervisor := r.URL.Query().Get("characterName")
	manualMode := r.URL.Query().Get("manualMode") == "true"
	claudeMode := r.URL.Query().Get("claudeMode") == "true"

	if supervisor == "" {
		http.Error(w, "missing characterName", http.StatusBadRequest)
		return
	}

	// Get the current auth method for the supervisor we wanna start
	supCfg, currFound := config.GetCharacter(supervisor)
	if !currFound || supCfg == nil {
		http.Error(w, "character configuration not found", http.StatusNotFound)
		return
	}

	if err := s.canStartSupervisor(supervisor, supervisorList, supCfg); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	go func(name string, manual bool, claude bool) {
		if claude {
			if err := s.manager.StartClaudeLaunch(name); err != nil {
				s.logger.Error("Failed to start supervisor in Claude mode", slog.String("supervisor", name), slog.Any("error", err))
			}
			return
		}
		if err := s.manager.Start(name, false, manual); err != nil {
			s.logger.Error("Failed to start supervisor", slog.String("supervisor", name), slog.Any("error", err))
		}
	}(supervisor, manualMode, claudeMode)

	s.initialData(w, r)
}


func (s *HttpServer) reorderSupervisors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisors []string `json:"supervisors"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := config.SetSupervisorOrder(req.Supervisors); err != nil {
		s.logger.Error("failed to save dashboard supervisor order", slog.Any("error", err))
		http.Error(w, "Failed to save supervisor order", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *HttpServer) hideSupervisor(w http.ResponseWriter, r *http.Request) {
	s.setSupervisorHidden(w, r, true)
}

func (s *HttpServer) unhideSupervisor(w http.ResponseWriter, r *http.Request) {
	s.setSupervisorHidden(w, r, false)
}

func (s *HttpServer) setSupervisorHidden(w http.ResponseWriter, r *http.Request, hidden bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisor string `json:"supervisor"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	supervisor := strings.TrimSpace(req.Supervisor)
	if supervisor == "" {
		http.Error(w, "Missing supervisor", http.StatusBadRequest)
		return
	}
	if supervisor == "template" {
		http.Error(w, "Template cannot be hidden", http.StatusBadRequest)
		return
	}
	if _, found := config.GetCharacter(supervisor); !found {
		http.Error(w, "Supervisor not found", http.StatusNotFound)
		return
	}
	if hidden && s.isSupervisorRunning(supervisor) {
		http.Error(w, "Stop the supervisor before hiding it", http.StatusConflict)
		return
	}

	currentHidden := config.HiddenSupervisors()
	updatedHidden := make([]string, 0, len(currentHidden)+1)
	seen := false
	for _, name := range currentHidden {
		if name == supervisor {
			seen = true
			if !hidden {
				continue
			}
		}
		updatedHidden = append(updatedHidden, name)
	}
	if hidden && !seen {
		updatedHidden = append(updatedHidden, supervisor)
	}

	if err := config.SetHiddenSupervisors(updatedHidden); err != nil {
		s.logger.Error("failed to update hidden supervisors", slog.String("supervisor", supervisor), slog.Bool("hidden", hidden), slog.Any("error", err))
		http.Error(w, "Failed to update supervisor visibility", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *HttpServer) deleteSupervisorConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisor string `json:"supervisor"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	supervisor := strings.TrimSpace(req.Supervisor)
	if supervisor == "" {
		http.Error(w, "Missing supervisor", http.StatusBadRequest)
		return
	}
	if !isValidSupervisorName(supervisor) {
		http.Error(w, "Invalid supervisor name", http.StatusBadRequest)
		return
	}
	if supervisor == "template" {
		http.Error(w, "Template cannot be deleted", http.StatusBadRequest)
		return
	}

	if _, found := config.GetCharacter(supervisor); !found {
		http.Error(w, "Supervisor not found", http.StatusNotFound)
		return
	}

	if s.isSupervisorRunning(supervisor) {
		http.Error(w, "Stop the supervisor before deleting it", http.StatusConflict)
		return
	}

	if refs := supervisorDeleteReferences(supervisor); len(refs) > 0 {
		http.Error(w, "Supervisor is referenced by: "+strings.Join(refs, ", "), http.StatusConflict)
		return
	}

	configDir := filepath.Join("config", supervisor)
	if err := os.RemoveAll(configDir); err != nil {
		s.logger.Error("failed to delete supervisor config directory", slog.String("supervisor", supervisor), slog.Any("error", err))
		http.Error(w, "Failed to delete supervisor config", http.StatusInternalServerError)
		return
	}

	if err := config.Load(); err != nil {
		s.logger.Error("failed to reload config after supervisor deletion", slog.String("supervisor", supervisor), slog.Any("error", err))
		http.Error(w, "Failed to reload config after deletion", http.StatusInternalServerError)
		return
	}

	if err := config.RemoveSupervisorFromDashboard(supervisor); err != nil {
		s.logger.Error("failed to update dashboard state after supervisor deletion", slog.String("supervisor", supervisor), slog.Any("error", err))
		http.Error(w, "Failed to update dashboard after deletion", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *HttpServer) isSupervisorRunning(supervisor string) bool {
	stats := s.manager.Status(supervisor)
	return stats.SupervisorStatus == bot.Starting ||
		stats.SupervisorStatus == bot.InGame ||
		stats.SupervisorStatus == bot.Paused
}

func (s *HttpServer) copySupervisorConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisor string `json:"supervisor"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	supervisor := strings.TrimSpace(req.Supervisor)
	if supervisor == "" {
		http.Error(w, "Missing supervisor", http.StatusBadRequest)
		return
	}
	if !isValidSupervisorName(supervisor) {
		http.Error(w, "Invalid supervisor name", http.StatusBadRequest)
		return
	}
	if supervisor == "template" {
		http.Error(w, "Template cannot be copied from the dashboard", http.StatusBadRequest)
		return
	}

	if _, found := config.GetCharacter(supervisor); !found {
		http.Error(w, "Supervisor not found", http.StatusNotFound)
		return
	}

	currentOrder := config.OrderedSupervisors()
	targetSupervisor := ""
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s_copy%d", supervisor, i)
		if _, found := config.GetCharacter(candidate); found {
			continue
		}
		if _, err := os.Stat(filepath.Join("config", candidate)); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			s.logger.Error("failed to inspect supervisor copy destination", slog.String("supervisor", candidate), slog.Any("error", err))
			http.Error(w, "Failed to prepare supervisor copy", http.StatusInternalServerError)
			return
		}
		targetSupervisor = candidate
		break
	}

	sourceDir := filepath.Join("config", supervisor)
	targetDir := filepath.Join("config", targetSupervisor)
	if err := cp.Copy(sourceDir, targetDir); err != nil {
		s.logger.Error("failed to copy supervisor config directory", slog.String("source", supervisor), slog.String("target", targetSupervisor), slog.Any("error", err))
		http.Error(w, "Failed to copy supervisor config", http.StatusInternalServerError)
		return
	}

	if err := config.Load(); err != nil {
		s.logger.Error("failed to reload config after supervisor copy", slog.String("source", supervisor), slog.String("target", targetSupervisor), slog.Any("error", err))
		http.Error(w, "Failed to reload config after copy", http.StatusInternalServerError)
		return
	}

	copiedCfg, found := config.GetCharacter(targetSupervisor)
	if !found || copiedCfg == nil {
		s.logger.Error("copied supervisor config not found after reload", slog.String("source", supervisor), slog.String("target", targetSupervisor))
		http.Error(w, "Failed to load copied supervisor config", http.StatusInternalServerError)
		return
	}
	if copiedCfg.AutoStart {
		copiedCfg.AutoStart = false
		if err := config.SaveSupervisorConfig(targetSupervisor, copiedCfg); err != nil {
			s.logger.Error("failed to disable autostart on copied supervisor", slog.String("source", supervisor), slog.String("target", targetSupervisor), slog.Any("error", err))
			http.Error(w, "Failed to finalize supervisor copy", http.StatusInternalServerError)
			return
		}
	}

	newOrder := make([]string, 0, len(currentOrder)+1)
	inserted := false
	for _, name := range currentOrder {
		newOrder = append(newOrder, name)
		if name == supervisor {
			newOrder = append(newOrder, targetSupervisor)
			inserted = true
		}
	}
	if !inserted {
		newOrder = append(newOrder, targetSupervisor)
	}

	if err := config.SetSupervisorOrder(newOrder); err != nil {
		s.logger.Error("failed to save supervisor order after copy", slog.String("source", supervisor), slog.String("target", targetSupervisor), slog.Any("error", err))
		http.Error(w, "Failed to save supervisor order", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"supervisor": targetSupervisor})
}

func (s *HttpServer) renameSupervisorConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisor string `json:"supervisor"`
		NewName    string `json:"newName"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	supervisor := strings.TrimSpace(req.Supervisor)
	newName := strings.TrimSpace(req.NewName)
	if supervisor == "" || newName == "" {
		http.Error(w, "Missing supervisor name", http.StatusBadRequest)
		return
	}
	if !isValidSupervisorName(supervisor) || !isValidSupervisorName(newName) {
		http.Error(w, "Invalid supervisor name", http.StatusBadRequest)
		return
	}
	if supervisor == "template" || newName == "template" {
		http.Error(w, "Template cannot be renamed", http.StatusBadRequest)
		return
	}
	if supervisor == newName {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"supervisor": supervisor})
		return
	}

	if _, found := config.GetCharacter(supervisor); !found {
		http.Error(w, "Supervisor not found", http.StatusNotFound)
		return
	}
	if _, found := config.GetCharacter(newName); found {
		http.Error(w, "A supervisor with that name already exists", http.StatusConflict)
		return
	}
	if s.manager.GetSupervisor(supervisor) != nil {
		http.Error(w, "Stop the supervisor before renaming it", http.StatusConflict)
		return
	}
	references := supervisorReferences(supervisor)
	activeReferences := make([]string, 0, len(references))
	for _, ref := range references {
		if s.isSupervisorRunning(ref.Name) {
			activeReferences = append(activeReferences, ref.String())
		}
	}
	if len(activeReferences) > 0 {
		slices.Sort(activeReferences)
		http.Error(w, "Stop referencing supervisors before renaming: "+strings.Join(activeReferences, ", "), http.StatusConflict)
		return
	}
	if _, err := os.Stat(filepath.Join("config", newName)); err == nil {
		http.Error(w, "A supervisor with that name already exists", http.StatusConflict)
		return
	} else if !os.IsNotExist(err) {
		s.logger.Error("failed to inspect supervisor rename destination", slog.String("supervisor", newName), slog.Any("error", err))
		http.Error(w, "Failed to prepare supervisor rename", http.StatusInternalServerError)
		return
	}

	sourceDir := filepath.Join("config", supervisor)
	targetDir := filepath.Join("config", newName)
	if err := os.Rename(sourceDir, targetDir); err != nil {
		s.logger.Error("failed to rename supervisor config directory", slog.String("source", supervisor), slog.String("target", newName), slog.Any("error", err))
		http.Error(w, "Failed to rename supervisor", http.StatusInternalServerError)
		return
	}

	if err := config.Load(); err != nil {
		rollbackErr := os.Rename(targetDir, sourceDir)
		if rollbackErr != nil {
			s.logger.Error("failed to rollback supervisor rename after reload failure", slog.String("source", supervisor), slog.String("target", newName), slog.Any("error", rollbackErr))
		}
		s.logger.Error("failed to reload config after supervisor rename", slog.String("source", supervisor), slog.String("target", newName), slog.Any("error", err))
		http.Error(w, "Failed to reload config after rename", http.StatusInternalServerError)
		return
	}

	allNames := make([]string, 0, len(config.GetCharacters()))
	for name := range config.GetCharacters() {
		allNames = append(allNames, name)
	}
	slices.Sort(allNames)

	for _, name := range allNames {
		cfg, found := config.GetCharacter(name)
		if !found || cfg == nil {
			continue
		}

		changed := false
		if cfg.Companion.LeaderName == supervisor {
			cfg.Companion.LeaderName = newName
			changed = true
		}
		if cfg.Muling.SwitchToMule == supervisor {
			cfg.Muling.SwitchToMule = newName
			changed = true
		}
		if cfg.Muling.ReturnTo == supervisor {
			cfg.Muling.ReturnTo = newName
			changed = true
		}
		for i, muleName := range cfg.Muling.MuleProfiles {
			if muleName != supervisor {
				continue
			}
			cfg.Muling.MuleProfiles[i] = newName
			changed = true
		}

		if !changed {
			continue
		}
		if err := config.SaveSupervisorConfig(name, cfg); err != nil {
			s.logger.Error("failed to update supervisor references after rename", slog.String("source", supervisor), slog.String("target", newName), slog.String("config", name), slog.Any("error", err))
			http.Error(w, "Failed to update supervisor references after rename", http.StatusInternalServerError)
			return
		}
	}

	if err := config.RenameSupervisorInDashboard(supervisor, newName); err != nil {
		s.logger.Error("failed to save dashboard state after rename", slog.String("source", supervisor), slog.String("target", newName), slog.Any("error", err))
		http.Error(w, "Failed to save dashboard state after rename", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"supervisor": newName})
}

type supervisorReference struct {
	Name   string
	Reason string
}

func (r supervisorReference) String() string {
	return r.Name + " " + r.Reason
}

func supervisorReferences(target string) []supervisorReference {
	allCharacters := config.GetCharacters()
	references := make([]supervisorReference, 0)

	for name, cfg := range allCharacters {
		if name == target || name == "template" || cfg == nil {
			continue
		}

		if cfg.Companion.LeaderName == target {
			references = append(references, supervisorReference{Name: name, Reason: "companion leader"})
		}
		if cfg.Muling.SwitchToMule == target {
			references = append(references, supervisorReference{Name: name, Reason: "muling switchToMule"})
		}
		if cfg.Muling.ReturnTo == target {
			references = append(references, supervisorReference{Name: name, Reason: "muling returnTo"})
		}
		if slices.Contains(cfg.Muling.MuleProfiles, target) {
			references = append(references, supervisorReference{Name: name, Reason: "muling muleProfiles"})
		}
	}

	slices.SortFunc(references, func(a, b supervisorReference) int {
		if a.Name == b.Name {
			return strings.Compare(a.Reason, b.Reason)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return references
}

func supervisorDeleteReferences(target string) []string {
	references := supervisorReferences(target)
	formatted := make([]string, 0, len(references))
	for _, ref := range references {
		formatted = append(formatted, ref.String())
	}
	return formatted
}

// canStartSupervisor enforces TokenAuth concurrency rules before starting a supervisor.
func (s *HttpServer) canStartSupervisor(target string, supervisorList []string, targetCfg *config.CharacterCfg) error {
	// Prevent launching of other clients while there's a client with TokenAuth still starting
	for _, sup := range supervisorList {
		// Skip the target itself
		if sup == target {
			continue
		}

		if s.manager.GetSupervisorStats(sup).SupervisorStatus == bot.Starting {
			// Prevent launching if we're using token auth & another client is starting (no matter what auth method)
			if targetCfg.AuthMethod == "TokenAuth" {
				return fmt.Errorf("waiting to start %s: another client (%s) is still starting", target, sup)
			}

			// Prevent launching if another client that is using token auth is starting
			sCfg, found := config.GetCharacter(sup)
			if found && sCfg.AuthMethod == "TokenAuth" {
				return fmt.Errorf("waiting to start %s: token-auth client %s is still starting", target, sup)
			}
		}
	}

	return nil
}

func (s *HttpServer) toggleAutoStart(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("characterName")
	enabled := r.URL.Query().Get("enabled") == "true"

	if name == "" {
		http.Error(w, "missing characterName", http.StatusBadRequest)
		return
	}

	cfg, found := config.GetCharacter(name)
	if !found || cfg == nil {
		http.Error(w, "character not found", http.StatusNotFound)
		return
	}

	cfg.AutoStart = enabled
	if err := config.SaveSupervisorConfig(name, cfg); err != nil {
		http.Error(w, "failed to save supervisor config", http.StatusInternalServerError)
		return
	}

	s.initialData(w, r)
}

func (s *HttpServer) runAutoStartOnce(w http.ResponseWriter, r *http.Request) {
	if err := s.autoStartOnceInternal(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// autoStartOnceInternal contains the core logic for starting all supervisors
// marked with AutoStart, using the global delay setting. It is used both by
// the HTTP handler and by application startup.
func (s *HttpServer) autoStartOnceInternal() error {
	supervisorList := s.manager.AvailableSupervisors()
	if len(supervisorList) == 0 {
		return fmt.Errorf("no supervisors available")
	}

	hiddenSet := make(map[string]struct{}, len(config.HiddenSupervisors()))
	for _, name := range config.HiddenSupervisors() {
		hiddenSet[name] = struct{}{}
	}

	// Collect supervisors marked for AutoStart
	var targets []string
	for _, name := range supervisorList {
		if _, hidden := hiddenSet[name]; hidden {
			continue
		}
		cfg, found := config.GetCharacter(name)
		if !found || cfg == nil {
			continue
		}
		if cfg.AutoStart {
			targets = append(targets, name)
		}
	}

	if len(targets) == 0 {
		return fmt.Errorf("no visible supervisors marked for auto start")
	}

	// Fallback to a sensible default if not configured
	delaySeconds := config.App.AutoStart.DelaySeconds
	if delaySeconds <= 0 {
		delaySeconds = 60
	}
	delay := time.Duration(delaySeconds) * time.Second

	go func() {
		s.logger.Info("Auto-start sequence begin",
			"characters", targets,
			"delay_seconds", delaySeconds)

		const concurrencyRetryDelay = 5 * time.Second

		for i, name := range targets {
			if i > 0 && delay > 0 {
				s.logger.Info("Waiting before next auto-start",
					"next_name", name,
					"delay", delay)
				time.Sleep(delay)
			}

			cfg, found := config.GetCharacter(name)
			if !found || cfg == nil {
				s.logger.Warn("Skipping auto-start because configuration was not found",
					slog.String("name", name))
				continue
			}

			for {
				if err := s.canStartSupervisor(name, supervisorList, cfg); err != nil {
					s.logger.Info("Auto-start waiting for available slot",
						"name", name,
						"reason", err.Error())
					time.Sleep(concurrencyRetryDelay)
					continue
				}
				break
			}

			s.logger.Info("Auto-starting character",
				"name", name,
				"position", fmt.Sprintf("%d/%d", i+1, len(targets)))

			// Run each supervisor start in its own goroutine so that
			// a long-running Start call for one character does not block
			// the scheduling of subsequent characters.
			go func(supervisorName string) {
				if err := s.manager.Start(supervisorName, false, false); err != nil {
					s.logger.Error("Auto-start failed",
						"name", supervisorName,
						"error", err)
				}
			}(name)
		}

		s.logger.Info("Auto-start sequence completed",
			"total", len(targets))
	}()

	return nil
}

// AutoStartOnStartup triggers a one-off Auto Start sequence if it is enabled
// in the global configuration. This is intended to be called when application starts.
func (s *HttpServer) AutoStartOnStartup() {
	if !config.App.AutoStart.Enabled {
		return
	}

	if err := s.autoStartOnceInternal(); err != nil {
		s.logger.Error("Auto start on startup failed", slog.Any("error", err))
	}
}

func (s *HttpServer) stopSupervisor(w http.ResponseWriter, r *http.Request) {
	s.manager.Stop(r.URL.Query().Get("characterName"))
	s.initialData(w, r)
}

func (s *HttpServer) togglePause(w http.ResponseWriter, r *http.Request) {
	s.manager.TogglePause(r.URL.Query().Get("characterName"))
	s.initialData(w, r)
}

func (s *HttpServer) index(w http.ResponseWriter) {
	status := make(map[string]bot.Stats)
	drops := make(map[string]int)
	supervisors := config.OrderedSupervisors()

	for _, supervisorName := range supervisors {
		status[supervisorName] = bot.Stats{
			SupervisorStatus: bot.NotStarted,
		}

		status[supervisorName] = s.manager.Status(supervisorName)

		if s.manager.GetSupervisorStats(supervisorName).Drops != nil {
			drops[supervisorName] = len(s.manager.GetSupervisorStats(supervisorName).Drops)
		} else {
			drops[supervisorName] = 0
		}

	}

	s.templates.ExecuteTemplate(w, "index.gohtml", IndexData{
		Version:   config.Version,
		Supervisors: supervisors,
		Status:    status,
		DropCount: drops,
	})
}

func (s *HttpServer) drops(w http.ResponseWriter, r *http.Request) {
	sup := r.URL.Query().Get("supervisor")
	cfg, found := config.GetCharacter(sup)
	if !found {
		http.Error(w, "Can't fetch drop data because the configuration "+sup+" wasn't found", http.StatusNotFound)
		return
	}

	var Drops []data.Drop

	if s.manager.GetSupervisorStats(sup).Drops == nil {
		Drops = make([]data.Drop, 0)
	} else {
		Drops = s.manager.GetSupervisorStats(sup).Drops
	}

	s.templates.ExecuteTemplate(w, "drops.gohtml", DropData{
		NumberOfDrops: len(Drops),
		Character:     cfg.CharacterName,
		Drops:         Drops,
	})
}

// allDrops renders a centralized droplog view across all characters.
func (s *HttpServer) allDrops(w http.ResponseWriter, r *http.Request) {
	// Determine droplog directory
	base := config.App.LogSaveDirectory
	if base == "" {
		base = "logs"
	}
	dir := filepath.Join(base, "droplogs")

	records, err := droplog.ReadAll(dir)
	if err != nil {
		s.templates.ExecuteTemplate(w, "all_drops.gohtml", AllDropsData{ErrorMessage: err.Error()})
		return
	}

	// Optional filters via query:
	qSup := strings.TrimSpace(r.URL.Query().Get("supervisor"))
	qChar := strings.TrimSpace(r.URL.Query().Get("character"))
	qText := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	var rows []AllDropRecord
	for _, rec := range records {
		if qSup != "" && !strings.EqualFold(qSup, rec.Supervisor) {
			continue
		}
		if qChar != "" && !strings.EqualFold(qChar, rec.Character) {
			continue
		}
		// text filter on name or stats string
		if qText != "" {
			name := rec.Drop.Item.IdentifiedName
			if name == "" {
				name = fmt.Sprint(rec.Drop.Item.Name)
			}
			blob := strings.ToLower(name + " " + strings.Join(statsToStrings(rec.Drop.Item.Stats), " "))
			if !strings.Contains(blob, qText) {
				continue
			}
		}
		rows = append(rows, AllDropRecord{
			Time:       rec.Time.Format("2006-01-02 15:04:05"),
			Supervisor: rec.Supervisor,
			Character:  rec.Character,
			Profile:    rec.Profile,
			Drop:       rec.Drop,
		})
	}

	// Sort newest first
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time > rows[j].Time })

	s.templates.ExecuteTemplate(w, "all_drops.gohtml", AllDropsData{
		Total:   len(rows),
		Records: rows,
	})
}

// exportDrops renders a static HTML of the centralized drops and returns it as a file download.
func (s *HttpServer) exportDrops(w http.ResponseWriter, r *http.Request) {
	// Reuse allDrops data generation
	base := config.App.LogSaveDirectory
	if base == "" {
		base = "logs"
	}
	dir := filepath.Join(base, "droplogs")

	records, err := droplog.ReadAll(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var rows []AllDropRecord
	for _, rec := range records {
		rows = append(rows, AllDropRecord{
			Time:       rec.Time.Format("2006-01-02 15:04:05"),
			Supervisor: rec.Supervisor,
			Character:  rec.Character,
			Profile:    rec.Profile,
			Drop:       rec.Drop,
		})
	}

	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, "all_drops.gohtml", AllDropsData{Total: len(rows), Records: rows}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create export directory: %v", err), http.StatusInternalServerError)
		return
	}

	// Write to a timestamped HTML file under droplogs
	outName := fmt.Sprintf("all-drops-%s.html", time.Now().Format("2006-01-02-15-04-05"))
	outPath := filepath.Join(dir, outName)
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		http.Error(w, fmt.Sprintf("failed to write export: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "file": outPath})
}

// helper: convert stats to strings for filtering
func statsToStrings(stats any) []string {
	v := reflect.ValueOf(stats)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return nil
	}
	out := make([]string, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		sv := v.Index(i)
		if sv.Kind() == reflect.Pointer {
			sv = sv.Elem()
		}
		if sv.Kind() == reflect.Struct {
			f := sv.FieldByName("String")
			if f.IsValid() && f.Kind() == reflect.String {
				s := f.String()
				if s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func validateSchedulerData(cfg *config.CharacterCfg) error {
	for day := 0; day < 7; day++ {

		cfg.Scheduler.Days[day].DayOfWeek = day

		// Sort time ranges
		sort.Slice(cfg.Scheduler.Days[day].TimeRanges, func(i, j int) bool {
			return cfg.Scheduler.Days[day].TimeRanges[i].Start.Before(cfg.Scheduler.Days[day].TimeRanges[j].Start)
		})

		daysOfWeek := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

		// Check for overlapping time ranges
		for i := 0; i < len(cfg.Scheduler.Days[day].TimeRanges); i++ {
			if !cfg.Scheduler.Days[day].TimeRanges[i].End.After(cfg.Scheduler.Days[day].TimeRanges[i].Start) {
				return fmt.Errorf("end time must be after start time for day %s", daysOfWeek[day])
			}

			if i > 0 {
				if !cfg.Scheduler.Days[day].TimeRanges[i].Start.After(cfg.Scheduler.Days[day].TimeRanges[i-1].End) {
					return fmt.Errorf("overlapping time ranges for day %s", daysOfWeek[day])
				}
			}
		}
	}

	return nil
}

func (s *HttpServer) getVersionData() *VersionData {
	versionInfo, _ := updater.GetCurrentVersionNoClone()
	if versionInfo != nil {
		return &VersionData{
			CommitHash: versionInfo.CommitHash,
			CommitDate: formatCommitDate(versionInfo.CommitDate),
			CommitMsg:  versionInfo.CommitMsg,
			Branch:     versionInfo.Branch,
		}
	}
	return nil
}

func (s *HttpServer) config(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		err := r.ParseForm()
		if err != nil {
			s.templates.ExecuteTemplate(w, "config.gohtml", ConfigData{
				AppCfg:       config.App,
				ErrorMessage:   "Error parsing form",
				CurrentVersion: s.getVersionData(),
			})
			return
		}

		newConfig := *config.App
		newConfig.FirstRun = false // Disable the welcome assistant
		newConfig.AppPath = r.Form.Get("apppath")
		newConfig.LegacyAppPath = r.Form.Get("legacyapppath")
		newConfig.CentralizedPickitPath = r.Form.Get("centralized_pickit_path")
		newConfig.UseCustomSettings = r.Form.Get("use_custom_settings") == "true"
		newConfig.GameWindowArrangement = r.Form.Get("game_window_arrangement") == "true"
		// Debug
		newConfig.Debug.Log = r.Form.Get("debug_log") == "true"
		newConfig.Debug.Screenshots = r.Form.Get("debug_screenshots") == "true"
		// newConfig.Debug.OpenOverlayMapOnGameStart = r.Form.Get("debug_open_overlay_map") == "true" // Commented out - field not available in this version
		// Discord
		newConfig.Discord.Enabled = r.Form.Get("discord_enabled") == "true"
		newConfig.Discord.EnableGameCreatedMessages = r.Form.Has("enable_game_created_messages")
		newConfig.Discord.EnableNewRunMessages = r.Form.Has("enable_new_run_messages")
		newConfig.Discord.EnableRunFinishMessages = r.Form.Has("enable_run_finish_messages")
		newConfig.Discord.EnableDiscordChickenMessages = r.Form.Has("enable_discord_chicken_messages")
		newConfig.Discord.EnableDiscordErrorMessages = r.Form.Has("enable_discord_error_messages")
		newConfig.Discord.DisableItemStashScreenshots = r.Form.Has("discord_disable_item_stash_screenshots")
		newConfig.Discord.IncludePickitInfoInItemText = r.Form.Has("discord_include_pickit_info_in_item_text")
		newConfig.Discord.Token = r.Form.Get("discord_token")
		newConfig.Discord.ChannelID = r.Form.Get("discord_channel_id")
		newConfig.Discord.ItemChannelID = r.Form.Get("discord_item_channel_id")
		newConfig.Discord.UseWebhook = r.Form.Get("discord_use_webhook") == "true"
		newConfig.Discord.WebhookURL = strings.TrimSpace(r.Form.Get("discord_webhook_url"))
		newConfig.Discord.ItemWebhookURL = strings.TrimSpace(r.Form.Get("discord_item_webhook_url"))
		newConfig.Discord.EnableFancyItemDrops = r.Form.Get("discord_enable_fancy_item_drops") == "true"
		newConfig.Discord.ClaudeAPIKey = strings.TrimSpace(r.Form.Get("discord_claude_api_key"))
		newConfig.Discord.ClaudeModel = strings.TrimSpace(r.Form.Get("discord_claude_model"))
		newConfig.Discord.FlareSolverrURL = strings.TrimSpace(r.Form.Get("discord_flaresolverr_url"))
		newConfig.Discord.D2JSPCookie = strings.TrimSpace(r.Form.Get("discord_d2jsp_cookie"))

		// Discord admins who can use bot commands
		discordAdmins := r.Form.Get("discord_admins")
		cleanedAdmins := strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || r == ',' {
				return r
			}
			return -1
		}, discordAdmins)
		newConfig.Discord.BotAdmins = strings.Split(cleanedAdmins, ",")
		// Traderie integration
		newConfig.Traderie.Token = strings.TrimSpace(r.Form.Get("traderie_token"))
		newConfig.Traderie.CfClearance = strings.TrimSpace(r.Form.Get("traderie_cf_clearance"))

		newConfig.Telegram.Enabled = r.Form.Get("telegram_enabled") == "true"
		newConfig.Telegram.Token = r.Form.Get("telegram_token")
		telegramChatId, err := strconv.ParseInt(r.Form.Get("telegram_chat_id"), 10, 64)
		if err != nil {
			s.templates.ExecuteTemplate(w, "config.gohtml", ConfigData{
				AppCfg:       &newConfig,
				ErrorMessage:   "Invalid Telegram Chat ID",
				CurrentVersion: s.getVersionData(),
			})
			return
		}
		newConfig.Telegram.ChatID = telegramChatId

		newConfig.Ngrok.Enabled = r.Form.Get("ngrok_enabled") == "true"
		newConfig.Ngrok.SendURL = r.Form.Get("ngrok_send_url") == "true"
		newConfig.Ngrok.Authtoken = strings.TrimSpace(r.Form.Get("ngrok_authtoken"))
		newConfig.Ngrok.Region = strings.TrimSpace(r.Form.Get("ngrok_region"))
		newConfig.Ngrok.Domain = strings.TrimSpace(r.Form.Get("ngrok_domain"))
		newConfig.Ngrok.BasicAuthUser = strings.TrimSpace(r.Form.Get("ngrok_basic_auth_user"))
		newConfig.Ngrok.BasicAuthPass = strings.TrimSpace(r.Form.Get("ngrok_basic_auth_pass"))
		if newConfig.Ngrok.BasicAuthUser != "" && newConfig.Ngrok.BasicAuthPass == "" {
			s.templates.ExecuteTemplate(w, "config.gohtml", ConfigData{
				AppCfg:       &newConfig,
				ErrorMessage:   "ngrok basic auth password is required when a username is set",
				CurrentVersion: s.getVersionData(),
			})
			return
		}
		if newConfig.Ngrok.BasicAuthPass != "" && newConfig.Ngrok.BasicAuthUser == "" {
			s.templates.ExecuteTemplate(w, "config.gohtml", ConfigData{
				AppCfg:       &newConfig,
				ErrorMessage:   "ngrok basic auth username is required when a password is set",
				CurrentVersion: s.getVersionData(),
			})
			return
		}
		if newConfig.Ngrok.BasicAuthPass != "" && len(newConfig.Ngrok.BasicAuthPass) < 8 {
			s.templates.ExecuteTemplate(w, "config.gohtml", ConfigData{
				AppCfg:       &newConfig,
				ErrorMessage:   "ngrok basic auth password must be at least 8 characters",
				CurrentVersion: s.getVersionData(),
			})
			return
		}

		// Ping Monitor
		newConfig.PingMonitor.Enabled = r.Form.Get("ping_monitor_enabled") == "true"
		pingThreshold, err := strconv.Atoi(r.Form.Get("ping_monitor_threshold"))
		if err != nil || pingThreshold < 100 {
			pingThreshold = 500 // Default to 500ms
		}
		newConfig.PingMonitor.HighPingThreshold = pingThreshold

		pingDuration, err := strconv.Atoi(r.Form.Get("ping_monitor_duration"))
		if err != nil || pingDuration < 5 {
			pingDuration = 30 // Default to 30 seconds
		}
		newConfig.PingMonitor.SustainedDuration = pingDuration

		// Auto Start
		newConfig.AutoStart.Enabled = r.Form.Get("autostart_enabled") == "true"
		autoStartDelay, err := strconv.Atoi(r.Form.Get("autostart_delay"))
		if err != nil || autoStartDelay < 0 {
			autoStartDelay = 0
		}
		newConfig.AutoStart.DelaySeconds = autoStartDelay

		err = config.ValidateAndSaveConfig(newConfig)
		if err != nil {
			s.templates.ExecuteTemplate(w, "config.gohtml", ConfigData{
				AppCfg:       &newConfig,
				ErrorMessage:   err.Error(),
				CurrentVersion: s.getVersionData(),
			})
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Get current version info
	versionInfo, _ := updater.GetCurrentVersionNoClone()
	var versionData *VersionData
	if versionInfo != nil {
		versionData = &VersionData{
			CommitHash: versionInfo.CommitHash,
			CommitDate: formatCommitDate(versionInfo.CommitDate),
			CommitMsg:  versionInfo.CommitMsg,
			Branch:     versionInfo.Branch,
		}
	}

	s.templates.ExecuteTemplate(w, "config.gohtml", ConfigData{
		AppCfg:       config.App,
		ErrorMessage:   "",
		CurrentVersion: versionData,
	})
}

// ConfigUpdateOptions defines which sections of the configuration should be updated
// from the provided form data.
type ConfigUpdateOptions struct {
	Identity            bool `json:"identity"` // Name, Auth, etc.
	Health              bool `json:"health"`
	Runs                bool `json:"runs"`
	PacketCasting       bool `json:"packetCasting"`
	CubeRecipes         bool `json:"cubeRecipes"`
	RunewordMaker       bool `json:"runewordMaker"`
	Merc                bool `json:"merc"`
	General             bool `json:"general"` // Includes class specific options too
	GeneralExtras       bool `json:"generalExtras"`
	Client              bool `json:"client"`
	Scheduler           bool `json:"scheduler"`
	Muling              bool `json:"muling"`
	Shopping            bool `json:"shopping"`
	CharacterCreation   bool `json:"characterCreation"` // Auto-create character setting
	UpdateAllRunDetails bool `json:"updateAllRunDetails"`
}

func (s *HttpServer) updateConfigFromForm(values url.Values, cfg *config.CharacterCfg, sections ConfigUpdateOptions, runDetailTargets []string) error {
	// Identity / Basic Settings
	if sections.Identity {
		if v := values.Get("maxGameLength"); v != "" {
			cfg.MaxGameLength, _ = strconv.Atoi(v)
		}
		cfg.CharacterName = values.Get("characterName")
		cfg.Character.Class = values.Get("characterClass")
		cfg.CommandLineArgs = values.Get("commandLineArgs")
		cfg.AutoCreateCharacter = values.Has("autoCreateCharacter")
		cfg.Username = values.Get("username")
		cfg.Password = values.Get("password")
		cfg.Realm = values.Get("realm")
		cfg.AuthMethod = values.Get("authmethod")
		cfg.AuthToken = values.Get("AuthToken")
	}

	// Character Creation Settings
	if sections.CharacterCreation {
		cfg.AutoCreateCharacter = values.Has("autoCreateCharacter")
	}

	// Client Settings
	if sections.Client {
		if !sections.Identity { // If Identity was skipped, handle these here if Client is checked
			cfg.CommandLineArgs = values.Get("commandLineArgs")
		}
		cfg.KillD2OnStop = values.Has("kill_d2_process")
		cfg.ClassicMode = values.Has("classic_mode")
		cfg.HidePortraits = values.Has("hide_portraits")
	}

	// Scheduler
	if sections.Scheduler {
		cfg.Scheduler.Enabled = values.Has("schedulerEnabled")
		cfg.Scheduler.Mode = values.Get("schedulerMode")
		if cfg.Scheduler.Mode == "" {
			cfg.Scheduler.Mode = "timeSlots"
		}

		// Global variance for time slots mode
		if v := values.Get("globalVarianceMin"); v != "" {
			cfg.Scheduler.GlobalVarianceMin, _ = strconv.Atoi(v)
		}

		// Reset scheduler days if we are updating them
		if len(cfg.Scheduler.Days) != 7 {
			cfg.Scheduler.Days = make([]config.Day, 7)
		}

		// Parse time slots mode data
		for day := 0; day < 7; day++ {
			starts := values[fmt.Sprintf("scheduler[%d][start][]", day)]
			ends := values[fmt.Sprintf("scheduler[%d][end][]", day)]
			startVars := values[fmt.Sprintf("scheduler[%d][startVar][]", day)]
			endVars := values[fmt.Sprintf("scheduler[%d][endVar][]", day)]

			cfg.Scheduler.Days[day].DayOfWeek = day
			cfg.Scheduler.Days[day].TimeRanges = make([]config.TimeRange, 0)

			for i := 0; i < len(starts); i++ {
				start, err := time.Parse("15:04", starts[i])
				if err != nil {
					continue
				}
				end, err := time.Parse("15:04", ends[i])
				if err != nil {
					continue
				}

				var startVar, endVar int
				if i < len(startVars) {
					startVar, _ = strconv.Atoi(startVars[i])
				}
				if i < len(endVars) {
					endVar, _ = strconv.Atoi(endVars[i])
				}

				cfg.Scheduler.Days[day].TimeRanges = append(cfg.Scheduler.Days[day].TimeRanges, config.TimeRange{
					Start:            start,
					End:              end,
					StartVarianceMin: startVar,
					EndVarianceMin:   endVar,
				})
			}
		}

		// Parse duration mode data
		cfg.Scheduler.Duration.WakeUpTime = values.Get("durationWakeUpTime")
		if v := values.Get("durationWakeUpVariance"); v != "" {
			cfg.Scheduler.Duration.WakeUpVariance, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationPlayHours"); v != "" {
			cfg.Scheduler.Duration.PlayHours, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationPlayHoursVariance"); v != "" {
			cfg.Scheduler.Duration.PlayHoursVariance, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationMealBreakCount"); v != "" {
			cfg.Scheduler.Duration.MealBreakCount, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationMealBreakDuration"); v != "" {
			cfg.Scheduler.Duration.MealBreakDuration, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationMealBreakVariance"); v != "" {
			cfg.Scheduler.Duration.MealBreakVariance, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationShortBreakCount"); v != "" {
			cfg.Scheduler.Duration.ShortBreakCount, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationShortBreakDuration"); v != "" {
			cfg.Scheduler.Duration.ShortBreakDuration, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationShortBreakVariance"); v != "" {
			cfg.Scheduler.Duration.ShortBreakVariance, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationBreakTimingVariance"); v != "" {
			cfg.Scheduler.Duration.BreakTimingVariance, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationJitterMin"); v != "" {
			cfg.Scheduler.Duration.JitterMin, _ = strconv.Atoi(v)
		}
		if v := values.Get("durationJitterMax"); v != "" {
			cfg.Scheduler.Duration.JitterMax, _ = strconv.Atoi(v)
		}

		if err := validateSchedulerData(cfg); err != nil {
			return err
		}
	}

	// Health
	if sections.Health {
		if v := values.Get("healingPotionAt"); v != "" {
			cfg.Health.HealingPotionAt, _ = strconv.Atoi(v)
		}
		if v := values.Get("manaPotionAt"); v != "" {
			cfg.Health.ManaPotionAt, _ = strconv.Atoi(v)
		}
		if v := values.Get("rejuvPotionAtLife"); v != "" {
			cfg.Health.RejuvPotionAtLife, _ = strconv.Atoi(v)
		}
		if v := values.Get("rejuvPotionAtMana"); v != "" {
			cfg.Health.RejuvPotionAtMana, _ = strconv.Atoi(v)
		}
		if v := values.Get("chickenAt"); v != "" {
			cfg.Health.ChickenAt, _ = strconv.Atoi(v)
		}
		if v := values.Get("townChickenAt"); v != "" {
			cfg.Health.TownChickenAt, _ = strconv.Atoi(v)
		}
		cfg.ChickenOnCurses.AmplifyDamage = values.Has("chickenAmplifyDamage")
		cfg.ChickenOnCurses.Decrepify = values.Has("chickenDecrepify")
		cfg.ChickenOnCurses.LowerResist = values.Has("chickenLowerResist")
		cfg.ChickenOnCurses.BloodMana = values.Has("chickenBloodMana")
		cfg.ChickenOnAuras.Fanaticism = values.Has("chickenFanaticism")
		cfg.ChickenOnAuras.Might = values.Has("chickenMight")
		cfg.ChickenOnAuras.Conviction = values.Has("chickenConviction")
		cfg.ChickenOnAuras.HolyFire = values.Has("chickenHolyFire")
		cfg.ChickenOnAuras.BlessedAim = values.Has("chickenBlessedAim")
		cfg.ChickenOnAuras.HolyFreeze = values.Has("chickenHolyFreeze")
		cfg.ChickenOnAuras.HolyShock = values.Has("chickenHolyShock")
		// Back to town config handled with Health or General?
		// It was in General in bulkApply but logic is closer to Health/Safety.
		// Let's allow updating it if either General or Health is selected, or stick to General.
		// For now, let's keep it under General to match previous bulk logic, or move it if needed.
	}

	// Mercenary
	if sections.Merc {
		cfg.Character.UseMerc = values.Has("useMerc")
		if v := values.Get("mercHealingPotionAt"); v != "" {
			cfg.Health.MercHealingPotionAt, _ = strconv.Atoi(v)
		}
		if v := values.Get("mercRejuvPotionAt"); v != "" {
			cfg.Health.MercRejuvPotionAt, _ = strconv.Atoi(v)
		}
		if v := values.Get("mercChickenAt"); v != "" {
			cfg.Health.MercChickenAt, _ = strconv.Atoi(v)
		}
	}

	// General (Character & Game)
	if sections.General {
		cfg.Character.StashToShared = values.Has("characterStashToShared")
		cfg.Character.UseTeleport = values.Has("characterUseTeleport")
		cfg.Character.UseExtraBuffs = values.Has("characterUseExtraBuffs")
		s.updateAutoStatSkillFromForm(values, cfg)

		// Game Settings (General)
		if v := values.Get("gameMinGoldPickupThreshold"); v != "" {
			cfg.Game.MinGoldPickupThreshold, _ = strconv.Atoi(v)
		}
		cfg.UseCentralizedPickit = values.Has("useCentralizedPickit")
		cfg.Game.UseCainIdentify = values.Has("useCainIdentify")
		cfg.Game.DisableIdentifyTome = values.Get("game.disableIdentifyTome") == "on"
		cfg.Game.InteractWithShrines = values.Has("interactWithShrines")
		cfg.Game.InteractWithChests = values.Has("interactWithChests")
		cfg.Game.InteractWithSuperChests = values.Has("interactWithSuperChests")

		// Ensure the two chest options are mutually exclusive. If both are enabled
		// (e.g. due to manual edits), keep the legacy behavior (all chests).
		if cfg.Game.InteractWithChests {
			cfg.Game.InteractWithSuperChests = false
		}
		if v := values.Get("stopLevelingAt"); v != "" {
			cfg.Game.StopLevelingAt, _ = strconv.Atoi(v)
		}

		if sections.GeneralExtras {
			cfg.Character.UseSwapForBuffs = values.Has("useSwapForBuffs")
			cfg.Character.BuffOnNewArea = values.Has("characterBuffOnNewArea")
			cfg.Character.BuffAfterWP = values.Has("characterBuffAfterWP")

			// Process ClearPathDist - only relevant when teleport is disabled
			if !cfg.Character.UseTeleport {
				clearPathDist, err := strconv.Atoi(values.Get("clearPathDist"))
				if err == nil && clearPathDist >= 0 && clearPathDist <= 30 {
					cfg.Character.ClearPathDist = clearPathDist
				} else {
					// Set default value if invalid
					cfg.Character.ClearPathDist = 7
				}
			} else {
				cfg.Character.ClearPathDist = 7
			}

			// Inventory Lock
			for y, row := range cfg.Inventory.InventoryLock {
				for x := range row {
					if values.Has(fmt.Sprintf("inventoryLock[%d][%d]", y, x)) {
						cfg.Inventory.InventoryLock[y][x] = 0
					} else {
						cfg.Inventory.InventoryLock[y][x] = 1
					}
				}
			}

			// Belt Columns
			if cols, ok := values["inventoryBeltColumns[]"]; ok {
				copy(cfg.Inventory.BeltColumns[:], cols)
			}

			if v := values.Get("healingPotionCount"); v != "" {
				cfg.Inventory.HealingPotionCount, _ = strconv.Atoi(v)
			}
			if v := values.Get("manaPotionCount"); v != "" {
				cfg.Inventory.ManaPotionCount, _ = strconv.Atoi(v)
			}
			if v := values.Get("rejuvPotionCount"); v != "" {
				cfg.Inventory.RejuvPotionCount, _ = strconv.Atoi(v)
			}

			cfg.Game.CreateLobbyGames = values.Has("createLobbyGames")
			cfg.Game.IsNonLadderChar = values.Has("isNonLadderChar")
			cfg.Game.IsHardCoreChar = values.Has("isHardCoreChar")
			cfg.Game.Difficulty = difficulty.Difficulty(values.Get("gameDifficulty"))
			cfg.Game.RandomizeRuns = values.Has("gameRandomizeRuns")

			// Back To Town Settings
			cfg.BackToTown.NoHpPotions = values.Has("noHpPotions")
			cfg.BackToTown.NoMpPotions = values.Has("noMpPotions")
			cfg.BackToTown.MercDied = values.Has("mercDied")
			cfg.BackToTown.EquipmentBroken = values.Has("equipmentBroken")

			// Companion
			cfg.Companion.Enabled = values.Has("companionEnabled")
			cfg.Companion.Leader = values.Has("companionLeader")
			cfg.Companion.LeaderName = values.Get("companionLeaderName")
			cfg.Companion.GameNameTemplate = values.Get("companionGameNameTemplate")
			cfg.Companion.GamePassword = values.Get("companionGamePassword")
			cfg.Companion.WaitForParty = values.Has("companionWaitForParty")
			if v := values.Get("companionPartyWaitTimeout"); v != "" {
				cfg.Companion.PartyWaitTimeout, _ = strconv.Atoi(v)
			}
			cfg.Companion.OpenTPForPlayer = values.Has("companionOpenTPForPlayer")
			cfg.Companion.BonusRuns = values.Has("companionBonusRuns")
			if bonusRunsList, ok := values["companionBonusRunsList"]; ok {
				cfg.Companion.BonusRunsList = bonusRunsList
			}
			cfg.Companion.RandomGameNames = values.Has("companionRandomGameNames")
			cfg.Companion.LeaderPriorityRuns = values.Has("companionLeaderPriorityRuns")

			// Gambling
			cfg.Gambling.Enabled = values.Has("gamblingEnabled")
			if raw := strings.TrimSpace(values.Get("gamblingItems")); raw != "" {
				parts := strings.Split(raw, ",")
				items := make([]string, 0, len(parts))
				for _, p := range parts {
					if p = strings.TrimSpace(p); p != "" {
						items = append(items, p)
					}
				}
				cfg.Gambling.Items = items
			} else {
				cfg.Gambling.Items = []string{}
			}
		}

		// Class-specific options are only updated when identity is explicitly updated.
		if sections.Identity {
			s.updateClassSpecificConfig(values, cfg)
		}
	}

	// Packet Casting
	if sections.PacketCasting {
		cfg.PacketCasting.UseForEntranceInteraction = values.Has("packetCastingUseForEntranceInteraction")
		cfg.PacketCasting.UseForItemPickup = values.Has("packetCastingUseForItemPickup")
		cfg.PacketCasting.UseForTpInteraction = values.Has("packetCastingUseForTpInteraction")
		cfg.PacketCasting.UseForTeleport = values.Has("packetCastingUseForTeleport")
		cfg.PacketCasting.UseForEntitySkills = values.Has("packetCastingUseForEntitySkills")
		cfg.PacketCasting.UseForSkillSelection = values.Has("packetCastingUseForSkillSelection")
		cfg.PacketCasting.UseForNPCInteraction = values.Has("packetCastingUseForNPCInteraction")
		cfg.PacketCasting.UseForWeaponSwap = values.Has("packetCastingUseForWeaponSwap")
		cfg.PacketCasting.UseForMovement = values.Has("packetCastingUseForMovement")
		cfg.PacketCasting.UseForBuySell = values.Has("packetCastingUseForBuySell")
		cfg.PacketCasting.UseForCubeTransmute = values.Has("packetCastingUseForCubeTransmute")
		cfg.PacketCasting.UseForGamble = values.Has("packetCastingUseForGamble")
		cfg.PacketCasting.UseForRepair = values.Has("packetCastingUseForRepair")
		cfg.PacketCasting.UseForIdentify = values.Has("packetCastingUseForIdentify")
		cfg.PacketCasting.UseForPotionUse = values.Has("packetCastingUseForPotionUse")
		cfg.PacketCasting.UseForStashManagement = values.Has("packetCastingUseForStashManagement")
		cfg.PacketCasting.UseForInventoryManagement = values.Has("packetCastingUseForInventoryManagement")
	}

	// Cube Recipes
	if sections.CubeRecipes {
		cfg.CubeRecipes.Enabled = values.Has("enableCubeRecipes")
		if recipes, ok := values["enabledRecipes"]; ok {
			cfg.CubeRecipes.EnabledRecipes = recipes
		}
		cfg.CubeRecipes.SkipPerfectAmethysts = values.Has("skipPerfectAmethysts")
		cfg.CubeRecipes.SkipPerfectRubies = values.Has("skipPerfectRubies")
		if v := values.Get("jewelsToKeep"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				cfg.CubeRecipes.JewelsToKeep = n
			} else {
				cfg.CubeRecipes.JewelsToKeep = 1
			}
		}
	}

	// Muling
	if sections.Muling {
		cfg.Muling.Enabled = values.Get("mulingEnabled") == "on"
		cfg.Muling.ReturnTo = values.Get("mulingReturnTo")

		// Validate mule profiles
		requestedMuleProfiles := values["mulingMuleProfiles[]"]
		validMuleProfiles := []string{}
		allCharacters := config.GetCharacters()
		for _, muleName := range requestedMuleProfiles {
			if muleCfg, exists := allCharacters[muleName]; exists && strings.ToLower(muleCfg.Character.Class) == "mule" {
				validMuleProfiles = append(validMuleProfiles, muleName)
			}
		}
		cfg.Muling.MuleProfiles = validMuleProfiles
	}

	// Shopping
	if sections.Shopping {
		s.applyShoppingFromForm(values, cfg)
	}

	// Runs
	if sections.Runs {
		if raw := values.Get("gameRuns"); raw != "" {
			var enabledRuns []config.Run
			if err := json.Unmarshal([]byte(raw), &enabledRuns); err == nil {
				cfg.Game.Runs = enabledRuns
			}
		}

		// Run Details
		if sections.UpdateAllRunDetails {
			// Update ALL run details if UpdateAllRunDetails is true
			s.applyRunDetails(values, cfg, getAllRunIDs())
		} else if len(runDetailTargets) > 0 {
			// Update only specific run details
			s.applyRunDetails(values, cfg, runDetailTargets)
		}
	}

	return nil
}

func (s *HttpServer) updateClassSpecificConfig(values url.Values, cfg *config.CharacterCfg) {
	// Smiter specific options
	if cfg.Character.Class == "smiter" {
		cfg.Character.Smiter.UberMephAura = values.Get("smiterUberMephAura")
		if cfg.Character.Smiter.UberMephAura == "" {
			cfg.Character.Smiter.UberMephAura = "resist_lightning"
		}
	}

	// Berserker Barb specific options
	if cfg.Character.Class == "berserker" {
		cfg.Character.BerserkerBarb.SkipPotionPickupInTravincal = values.Has("barbSkipPotionPickupInTravincal")
		cfg.Character.BerserkerBarb.FindItemSwitch = values.Has("characterFindItemSwitch")
		cfg.Character.BerserkerBarb.UseHowl = values.Has("barbUseHowl")
		if cfg.Character.BerserkerBarb.UseHowl {
			howlCooldown, err := strconv.Atoi(values.Get("barbHowlCooldown"))
			if err == nil && howlCooldown >= 1 && howlCooldown <= 60 {
				cfg.Character.BerserkerBarb.HowlCooldown = howlCooldown
			} else {
				cfg.Character.BerserkerBarb.HowlCooldown = 6
			}
			howlMinMonsters, err := strconv.Atoi(values.Get("barbHowlMinMonsters"))
			if err == nil && howlMinMonsters >= 1 && howlMinMonsters <= 20 {
				cfg.Character.BerserkerBarb.HowlMinMonsters = howlMinMonsters
			} else {
				cfg.Character.BerserkerBarb.HowlMinMonsters = 4
			}
		}
		cfg.Character.BerserkerBarb.UseBattleCry = values.Has("barbUseBattleCry")
		if cfg.Character.BerserkerBarb.UseBattleCry {
			battleCryCooldown, err := strconv.Atoi(values.Get("barbBattleCryCooldown"))
			if err == nil && battleCryCooldown >= 1 && battleCryCooldown <= 60 {
				cfg.Character.BerserkerBarb.BattleCryCooldown = battleCryCooldown
			} else {
				cfg.Character.BerserkerBarb.BattleCryCooldown = 6
			}
			battleCryMinMonsters, err := strconv.Atoi(values.Get("barbBattleCryMinMonsters"))
			if err == nil && battleCryMinMonsters >= 1 && battleCryMinMonsters <= 20 {
				cfg.Character.BerserkerBarb.BattleCryMinMonsters = battleCryMinMonsters
			} else {
				cfg.Character.BerserkerBarb.BattleCryMinMonsters = 4
			}
		}
		cfg.Character.BerserkerBarb.HorkNormalMonsters = values.Has("berserkerBarbHorkNormalMonsters")
		horkRange, err := strconv.Atoi(values.Get("berserkerBarbHorkMonsterCheckRange"))
		if err == nil && horkRange > 0 {
			cfg.Character.BerserkerBarb.HorkMonsterCheckRange = horkRange
		} else {
			cfg.Character.BerserkerBarb.HorkMonsterCheckRange = 7
		}
	}

	// Barb Leveling specific options
	if cfg.Character.Class == "barb_leveling" {
		cfg.Character.BarbLeveling.UseHowl = values.Has("barbLevelingUseHowl")
		if cfg.Character.BarbLeveling.UseHowl {
			howlCooldown, err := strconv.Atoi(values.Get("barbLevelingHowlCooldown"))
			if err == nil && howlCooldown >= 1 && howlCooldown <= 60 {
				cfg.Character.BarbLeveling.HowlCooldown = howlCooldown
			} else {
				cfg.Character.BarbLeveling.HowlCooldown = 8
			}
			howlMinMonsters, err := strconv.Atoi(values.Get("barbLevelingHowlMinMonsters"))
			if err == nil && howlMinMonsters >= 1 && howlMinMonsters <= 20 {
				cfg.Character.BarbLeveling.HowlMinMonsters = howlMinMonsters
			} else {
				cfg.Character.BarbLeveling.HowlMinMonsters = 4
			}
		}
		cfg.Character.BarbLeveling.UseBattleCry = values.Has("barbLevelingUseBattleCry")
		if cfg.Character.BarbLeveling.UseBattleCry {
			battleCryCooldown, err := strconv.Atoi(values.Get("barbLevelingBattleCryCooldown"))
			if err == nil && battleCryCooldown >= 1 && battleCryCooldown <= 60 {
				cfg.Character.BarbLeveling.BattleCryCooldown = battleCryCooldown
			} else {
				cfg.Character.BarbLeveling.BattleCryCooldown = 6
			}
			battleCryMinMonsters, err := strconv.Atoi(values.Get("barbLevelingBattleCryMinMonsters"))
			if err == nil && battleCryMinMonsters >= 1 && battleCryMinMonsters <= 20 {
				cfg.Character.BarbLeveling.BattleCryMinMonsters = battleCryMinMonsters
			} else {
				cfg.Character.BarbLeveling.BattleCryMinMonsters = 1
			}
			cfg.Character.BarbLeveling.UsePacketLearning = values.Has("usePacketLearning")
		}
	}

	// Warcry Barb specific options
	if cfg.Character.Class == "warcry_barb" {
		cfg.Character.WarcryBarb.FindItemSwitch = values.Has("warcryBarbFindItemSwitch")
		cfg.Character.WarcryBarb.SkipPotionPickupInTravincal = values.Has("warcryBarbSkipPotionPickupInTravincal")
		cfg.Character.WarcryBarb.UseHowl = values.Has("warcryBarbUseHowl")
		if cfg.Character.WarcryBarb.UseHowl {
			howlCooldown, err := strconv.Atoi(values.Get("warcryBarbHowlCooldown"))
			if err == nil && howlCooldown >= 1 && howlCooldown <= 60 {
				cfg.Character.WarcryBarb.HowlCooldown = howlCooldown
			} else {
				cfg.Character.WarcryBarb.HowlCooldown = 8
			}
			howlMinMonsters, err := strconv.Atoi(values.Get("warcryBarbHowlMinMonsters"))
			if err == nil && howlMinMonsters >= 1 && howlMinMonsters <= 20 {
				cfg.Character.WarcryBarb.HowlMinMonsters = howlMinMonsters
			} else {
				cfg.Character.WarcryBarb.HowlMinMonsters = 4
			}
		}
		cfg.Character.WarcryBarb.UseBattleCry = values.Has("warcryBarbUseBattleCry")
		if cfg.Character.WarcryBarb.UseBattleCry {
			battleCryCooldown, err := strconv.Atoi(values.Get("warcryBarbBattleCryCooldown"))
			if err == nil && battleCryCooldown >= 1 && battleCryCooldown <= 60 {
				cfg.Character.WarcryBarb.BattleCryCooldown = battleCryCooldown
			} else {
				cfg.Character.WarcryBarb.BattleCryCooldown = 6
			}
			battleCryMinMonsters, err := strconv.Atoi(values.Get("warcryBarbBattleCryMinMonsters"))
			if err == nil && battleCryMinMonsters >= 1 && battleCryMinMonsters <= 20 {
				cfg.Character.WarcryBarb.BattleCryMinMonsters = battleCryMinMonsters
			} else {
				cfg.Character.WarcryBarb.BattleCryMinMonsters = 1
			}
		}
		cfg.Character.WarcryBarb.UseGrimWard = values.Has("warcryBarbUseGrimWard")
		cfg.Character.WarcryBarb.HorkNormalMonsters = values.Has("warcryBarbHorkNormalMonsters")
		horkRange, err := strconv.Atoi(values.Get("warcryBarbHorkMonsterCheckRange"))
		if err == nil && horkRange > 0 {
			cfg.Character.WarcryBarb.HorkMonsterCheckRange = horkRange
		} else {
			cfg.Character.WarcryBarb.HorkMonsterCheckRange = 7
		}
	}

	// Nova Sorceress specific options
	if cfg.Character.Class == "nova" || cfg.Character.Class == "lightsorc" {
		bossStaticThreshold, err := strconv.Atoi(values.Get("novaBossStaticThreshold"))
		if err == nil {
			minThreshold := 65
			switch cfg.Game.Difficulty {
			case difficulty.Normal:
				minThreshold = 1
			case difficulty.Nightmare:
				minThreshold = 33
			case difficulty.Hell:
				minThreshold = 50
			}
			if bossStaticThreshold >= minThreshold && bossStaticThreshold <= 100 {
				cfg.Character.NovaSorceress.BossStaticThreshold = bossStaticThreshold
			} else {
				cfg.Character.NovaSorceress.BossStaticThreshold = minThreshold
			}
		} else {
			cfg.Character.NovaSorceress.BossStaticThreshold = 65
		}
	}

	// Mosaic specific options
	if cfg.Character.Class == "mosaic" {
		cfg.Character.MosaicSin.UseTigerStrike = values.Has("mosaicUseTigerStrike")
		cfg.Character.MosaicSin.UseCobraStrike = values.Has("mosaicUseCobraStrike")
		cfg.Character.MosaicSin.UseClawsOfThunder = values.Has("mosaicUseClawsOfThunder")
		cfg.Character.MosaicSin.UseBladesOfIce = values.Has("mosaicUseBladesOfIce")
		cfg.Character.MosaicSin.UseFistsOfFire = values.Has("mosaicUseFistsOfFire")
	}

	// Blizzard Sorc specific options
	if cfg.Character.Class == "sorceress" {
		cfg.Character.BlizzardSorceress.UseMoatTrick = values.Has("blizzardUseMoatTrick")
		cfg.Character.BlizzardSorceress.UseStaticOnMephisto = values.Has("blizzardUseStaticOnMephisto")
		cfg.Character.BlizzardSorceress.UseBlizzardPackets = values.Has("blizzardUseBlizzardPackets")
	}

	// Sorceress Leveling specific options
	if cfg.Character.Class == "sorceress_leveling" {
		cfg.Character.SorceressLeveling.UseMoatTrick = values.Has("levelingUseMoatTrick")
		cfg.Character.SorceressLeveling.UseStaticOnMephisto = values.Has("levelingUseStaticOnMephisto")
		cfg.Character.SorceressLeveling.UseBlizzardPackets = values.Has("levelingUseBlizzardPackets")
		cfg.Character.SorceressLeveling.UsePacketLearning = values.Has("levelingUsePacketLearning")
	}

	// Assassin Leveling specific options
	if cfg.Character.Class == "assassin" {
		cfg.Character.AssassinLeveling.UsePacketLearning = values.Has("usePacketLearning")
	}

	// Amazon Leveling specific options
	if cfg.Character.Class == "amazon_leveling" {
		cfg.Character.AmazonLeveling.UsePacketLearning = values.Has("usePacketLearning")
	}

	// Druid Leveling specific options
	if cfg.Character.Class == "druid_leveling" {
		cfg.Character.DruidLeveling.UsePacketLearning = values.Has("usePacketLearning")
	}

	// Necromancer Leveling specific options
	if cfg.Character.Class == "necromancer" {
		cfg.Character.NecromancerLeveling.UsePacketLearning = values.Has("usePacketLearning")
	}

	// Paladin Leveling specific options
	if cfg.Character.Class == "paladin" {
		cfg.Character.PaladinLeveling.UsePacketLearning = values.Has("usePacketLearning")
	}

	// Nova Sorceress specific options (Extra)
	if cfg.Character.Class == "nova" {
		cfg.Character.NovaSorceress.AggressiveNovaPositioning = values.Has("aggressiveNovaPositioning")
		if v := values.Get("aggressiveSkipMinNormals"); v != "" {
			cfg.Character.NovaSorceress.AggressiveSkipMinNormals, _ = strconv.Atoi(v)
		}
	}

	// Javazon specific options
	if cfg.Character.Class == "javazon" {
		cfg.Character.Javazon.DensityKillerEnabled = values.Has("javazonDensityKillerEnabled")
		if v := values.Get("javazonDensityKillerIgnoreWhitesBelow"); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow = i
			} else {
				cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow = 4
			}
		} else if cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow == 0 {
			cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow = 4
		}
		if v := values.Get("javazonDensityKillerForceRefillBelowPercent"); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				if i < 1 {
					i = 1
				}
				if i > 100 {
					i = 100
				}
				cfg.Character.Javazon.DensityKillerForceRefillBelowPercent = i
			} else {
				cfg.Character.Javazon.DensityKillerForceRefillBelowPercent = 50
			}
		} else if cfg.Character.Javazon.DensityKillerForceRefillBelowPercent == 0 {
			cfg.Character.Javazon.DensityKillerForceRefillBelowPercent = 50
		}
	}

	// Lightning Sorceress specific options
	if cfg.Character.Class == "lightsorc" {
	}

	// Hydra Orb Sorceress specific options
	if cfg.Character.Class == "hydraorb" {
	}

	// Fireball Sorceress specific options
	if cfg.Character.Class == "fireballsorc" {
	}
}

func getAllRunIDs() []string {
	// A helper to get all possible run keys if we want to apply everything
	// This list should ideally match all case statements in applyRunDetails
	return []string{
		"andariel", "countess", "duriel", "pit", "cows", "pindleskin",
		"stony_tomb", "mausoleum", "ancient_tunnels", "drifter_cavern",
		"spider_cavern", "arachnid_lair", "mephisto", "tristram",
		"nihlathak", "summoner", "baal", "eldritch", "lower_kurast_chest",
		"diablo", "leveling", "leveling_sequence", "quests", "terror_zone",
		"utility", "shopping",
	}
}

func sanitizeFavoriteRunSelection(selected []string) []string {
	seen := make(map[string]struct{}, len(selected))
	result := make([]string, 0, len(selected))
	for _, name := range selected {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := config.AvailableRuns[config.Run(trimmed)]; !ok {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func cloneCharacterCfg(cfg *config.CharacterCfg) (*config.CharacterCfg, error) {
	if cfg == nil {
		return nil, errors.New("nil source config")
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var out config.CharacterCfg
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func applyRunewordSettings(dst *config.CharacterCfg, src *config.CharacterCfg) {
	if dst == nil || src == nil {
		return
	}
	dst.Game.RunewordMaker = src.Game.RunewordMaker
	dst.Game.RunewordOverrides = src.Game.RunewordOverrides
	dst.Game.RunewordRerollRules = src.Game.RunewordRerollRules
	dst.CubeRecipes.PrioritizeRunewords = src.CubeRecipes.PrioritizeRunewords
}

func (s *HttpServer) characterSettings(w http.ResponseWriter, r *http.Request) {
	sequenceFiles := s.listLevelingSequenceFiles()
	defaultSkillOptions := buildSkillOptionsForBuild("")
	var err error
	if r.Method == http.MethodPost {
		err = r.ParseForm()
		if err != nil {
			s.templates.ExecuteTemplate(w, "character_settings.gohtml", CharacterSettings{
				Version:               config.Version,
				ErrorMessage:          err.Error(),
				SkillOptions:          defaultSkillOptions,
				LevelingSequenceFiles: sequenceFiles,
				RunFavoriteRuns:       config.App.RunFavoriteRuns,
				Realms:                secrets.Realms(),
			})
			return
		}

		supervisorName := r.Form.Get("name")
		cloneSource := strings.TrimSpace(r.Form.Get("cloneSource"))
		cfg, found := config.GetCharacter(supervisorName)
		if !found {
			err = config.CreateFromTemplate(supervisorName)
			if err != nil {
				s.templates.ExecuteTemplate(w, "character_settings.gohtml", CharacterSettings{
					Version:               config.Version,
					ErrorMessage:          err.Error(),
					Supervisor:            supervisorName,
					SkillOptions:          defaultSkillOptions,
					LevelingSequenceFiles: sequenceFiles,
					RunFavoriteRuns:       config.App.RunFavoriteRuns,
					Realms:                secrets.Realms(),
				})
				return
			}
			cfg, found = config.GetCharacter(supervisorName)
			if !found || cfg == nil {
				s.templates.ExecuteTemplate(w, "character_settings.gohtml", CharacterSettings{
					Version:               config.Version,
					ErrorMessage:          "failed to load newly created configuration",
					Supervisor:            supervisorName,
					SkillOptions:          defaultSkillOptions,
					LevelingSequenceFiles: sequenceFiles,
					RunFavoriteRuns:       config.App.RunFavoriteRuns,
					Realms:                secrets.Realms(),
				})
				return
			}

			if cloneSource != "" {
				if cloneCfg, ok := config.GetCharacter(cloneSource); ok && cloneCfg != nil {
					cloned, err := cloneCharacterCfg(cloneCfg)
					if err != nil {
						s.logger.Warn("failed to clone character config", slog.String("source", cloneSource), slog.Any("error", err))
					} else {
						cloned.ConfigFolderName = cfg.ConfigFolderName
						*cfg = *cloned
					}
				}
			}
		}

		if v := strings.TrimSpace(r.Form.Get("characterName")); v != "" {
			cfg.CharacterName = v
		}
		cfg.Game.RunewordMaker.Enabled = r.Form.Has("runewordMakerEnabled")
		cfg.AutoCreateCharacter = r.Form.Has("autoCreateCharacter")
		cfg.Username = r.Form.Get("username")
		cfg.Password = r.Form.Get("password")
		cfg.Realm = r.Form.Get("realm")
		cfg.AuthMethod = r.Form.Get("authmethod")
		cfg.AuthToken = r.Form.Get("AuthToken")
		cfg.CommandLineArgs = r.Form.Get("commandLineArgs")
		cfg.KillD2OnStop = r.Form.Has("kill_d2_process")
		cfg.ClassicMode = r.Form.Has("classic_mode")
		cfg.HidePortraits = r.Form.Has("hide_portraits")

		// Health config
		cfg.Health.HealingPotionAt, _ = strconv.Atoi(r.Form.Get("healingPotionAt"))
		cfg.Health.ManaPotionAt, _ = strconv.Atoi(r.Form.Get("manaPotionAt"))
		cfg.Health.RejuvPotionAtLife, _ = strconv.Atoi(r.Form.Get("rejuvPotionAtLife"))
		cfg.Health.RejuvPotionAtMana, _ = strconv.Atoi(r.Form.Get("rejuvPotionAtMana"))
		cfg.Health.ChickenAt, _ = strconv.Atoi(r.Form.Get("chickenAt"))
		cfg.Health.TownChickenAt, _ = strconv.Atoi(r.Form.Get("townChickenAt"))
		cfg.Character.UseMerc = r.Form.Has("useMerc")
		cfg.Health.MercHealingPotionAt, _ = strconv.Atoi(r.Form.Get("mercHealingPotionAt"))
		cfg.Health.MercRejuvPotionAt, _ = strconv.Atoi(r.Form.Get("mercRejuvPotionAt"))
		cfg.Health.MercChickenAt, _ = strconv.Atoi(r.Form.Get("mercChickenAt"))

		// Chicken on Curses/Auras
		cfg.ChickenOnCurses.AmplifyDamage = r.Form.Has("chickenAmplifyDamage")
		cfg.ChickenOnCurses.Decrepify = r.Form.Has("chickenDecrepify")
		cfg.ChickenOnCurses.LowerResist = r.Form.Has("chickenLowerResist")
		cfg.ChickenOnCurses.BloodMana = r.Form.Has("chickenBloodMana")
		cfg.ChickenOnAuras.Fanaticism = r.Form.Has("chickenFanaticism")
		cfg.ChickenOnAuras.Might = r.Form.Has("chickenMight")
		cfg.ChickenOnAuras.Conviction = r.Form.Has("chickenConviction")
		cfg.ChickenOnAuras.HolyFire = r.Form.Has("chickenHolyFire")
		cfg.ChickenOnAuras.BlessedAim = r.Form.Has("chickenBlessedAim")
		cfg.ChickenOnAuras.HolyFreeze = r.Form.Has("chickenHolyFreeze")
		cfg.ChickenOnAuras.HolyShock = r.Form.Has("chickenHolyShock")

		// Character config section
		cfg.Character.Class = r.Form.Get("characterClass")
		if strings.HasSuffix(cfg.Character.Class, "_leveling") {
			// Leveling characters should start without cloned runeword reroll/override rules.
			cfg.Game.RunewordOverrides = nil
			cfg.Game.RunewordRerollRules = nil
		}
		cfg.Character.StashToShared = r.Form.Has("characterStashToShared")
		cfg.Character.UseTeleport = r.Form.Has("characterUseTeleport")
		cfg.Character.UseExtraBuffs = r.Form.Has("characterUseExtraBuffs")
		cfg.Character.UseSwapForBuffs = r.Form.Has("useSwapForBuffs")
		cfg.Character.BuffOnNewArea = r.Form.Has("characterBuffOnNewArea")
		cfg.Character.BuffAfterWP = r.Form.Has("characterBuffAfterWP")
		s.updateAutoStatSkillFromForm(r.Form, cfg)

		// Process ClearPathDist - only relevant when teleport is disabled
		if !cfg.Character.UseTeleport {
			clearPathDist, err := strconv.Atoi(r.Form.Get("clearPathDist"))
			if err == nil && clearPathDist >= 0 && clearPathDist <= 30 {
				cfg.Character.ClearPathDist = clearPathDist
			} else {
				// Set default value if invalid
				cfg.Character.ClearPathDist = 7
				s.logger.Debug("Using default ClearPathDist value",
					slog.Int("default", 7),
					slog.String("input", r.Form.Get("clearPathDist")))
			}
		} else {
			cfg.Character.ClearPathDist = 7
		}

		// Smiter specific options
		if cfg.Character.Class == "smiter" {
			cfg.Character.Smiter.UberMephAura = r.Form.Get("smiterUberMephAura")
			if cfg.Character.Smiter.UberMephAura == "" {
				cfg.Character.Smiter.UberMephAura = "resist_lightning"
			}
		}

		// Berserker Barb specific options
		if cfg.Character.Class == "berserker" {
			cfg.Character.BerserkerBarb.SkipPotionPickupInTravincal = r.Form.Has("barbSkipPotionPickupInTravincal")
			cfg.Character.BerserkerBarb.FindItemSwitch = r.Form.Has("characterFindItemSwitch")
			cfg.Character.BerserkerBarb.UseHowl = r.Form.Has("barbUseHowl")
			if cfg.Character.BerserkerBarb.UseHowl {
				howlCooldown, err := strconv.Atoi(r.Form.Get("barbHowlCooldown"))
				if err == nil && howlCooldown >= 1 && howlCooldown <= 60 {
					cfg.Character.BerserkerBarb.HowlCooldown = howlCooldown
				} else {
					cfg.Character.BerserkerBarb.HowlCooldown = 6
				}
				howlMinMonsters, err := strconv.Atoi(r.Form.Get("barbHowlMinMonsters"))
				if err == nil && howlMinMonsters >= 1 && howlMinMonsters <= 20 {
					cfg.Character.BerserkerBarb.HowlMinMonsters = howlMinMonsters
				} else {
					cfg.Character.BerserkerBarb.HowlMinMonsters = 4
				}
			}
			cfg.Character.BerserkerBarb.UseBattleCry = r.Form.Has("barbUseBattleCry")
			if cfg.Character.BerserkerBarb.UseBattleCry {
				battleCryCooldown, err := strconv.Atoi(r.Form.Get("barbBattleCryCooldown"))
				if err == nil && battleCryCooldown >= 1 && battleCryCooldown <= 60 {
					cfg.Character.BerserkerBarb.BattleCryCooldown = battleCryCooldown
				} else {
					cfg.Character.BerserkerBarb.BattleCryCooldown = 6
				}
				battleCryMinMonsters, err := strconv.Atoi(r.Form.Get("barbBattleCryMinMonsters"))
				if err == nil && battleCryMinMonsters >= 1 && battleCryMinMonsters <= 20 {
					cfg.Character.BerserkerBarb.BattleCryMinMonsters = battleCryMinMonsters
				} else {
					cfg.Character.BerserkerBarb.BattleCryMinMonsters = 4
				}
			}
			cfg.Character.BerserkerBarb.HorkNormalMonsters = r.Form.Has("berserkerBarbHorkNormalMonsters")
			horkRange, err := strconv.Atoi(r.Form.Get("berserkerBarbHorkMonsterCheckRange"))
			if err == nil && horkRange > 0 {
				cfg.Character.BerserkerBarb.HorkMonsterCheckRange = horkRange
			} else {
				cfg.Character.BerserkerBarb.HorkMonsterCheckRange = 7
			}
		}

		// Barb Leveling specific options
		if cfg.Character.Class == "barb_leveling" {
			cfg.Character.BarbLeveling.UseHowl = r.Form.Has("barbLevelingUseHowl")
			if cfg.Character.BarbLeveling.UseHowl {
				howlCooldown, err := strconv.Atoi(r.Form.Get("barbLevelingHowlCooldown"))
				if err == nil && howlCooldown >= 1 && howlCooldown <= 60 {
					cfg.Character.BarbLeveling.HowlCooldown = howlCooldown
				} else {
					cfg.Character.BarbLeveling.HowlCooldown = 8
				}
				howlMinMonsters, err := strconv.Atoi(r.Form.Get("barbLevelingHowlMinMonsters"))
				if err == nil && howlMinMonsters >= 1 && howlMinMonsters <= 20 {
					cfg.Character.BarbLeveling.HowlMinMonsters = howlMinMonsters
				} else {
					cfg.Character.BarbLeveling.HowlMinMonsters = 4
				}
			}
			cfg.Character.BarbLeveling.UseBattleCry = r.Form.Has("barbLevelingUseBattleCry")
			if cfg.Character.BarbLeveling.UseBattleCry {
				battleCryCooldown, err := strconv.Atoi(r.Form.Get("barbLevelingBattleCryCooldown"))
				if err == nil && battleCryCooldown >= 1 && battleCryCooldown <= 60 {
					cfg.Character.BarbLeveling.BattleCryCooldown = battleCryCooldown
				} else {
					cfg.Character.BarbLeveling.BattleCryCooldown = 6
				}
				battleCryMinMonsters, err := strconv.Atoi(r.Form.Get("barbLevelingBattleCryMinMonsters"))
				if err == nil && battleCryMinMonsters >= 1 && battleCryMinMonsters <= 20 {
					cfg.Character.BarbLeveling.BattleCryMinMonsters = battleCryMinMonsters
				} else {
					cfg.Character.BarbLeveling.BattleCryMinMonsters = 1
				}
				cfg.Character.BarbLeveling.UsePacketLearning = r.Form.Has("usePacketLearning")
			}
		}

		// Warcry Barb specific options
		if cfg.Character.Class == "warcry_barb" {
			cfg.Character.WarcryBarb.FindItemSwitch = r.Form.Has("warcryBarbFindItemSwitch")
			cfg.Character.WarcryBarb.SkipPotionPickupInTravincal = r.Form.Has("warcryBarbSkipPotionPickupInTravincal")
			cfg.Character.WarcryBarb.UseHowl = r.Form.Has("warcryBarbUseHowl")
			if cfg.Character.WarcryBarb.UseHowl {
				howlCooldown, err := strconv.Atoi(r.Form.Get("warcryBarbHowlCooldown"))
				if err == nil && howlCooldown >= 1 && howlCooldown <= 60 {
					cfg.Character.WarcryBarb.HowlCooldown = howlCooldown
				} else {
					cfg.Character.WarcryBarb.HowlCooldown = 8
				}
				howlMinMonsters, err := strconv.Atoi(r.Form.Get("warcryBarbHowlMinMonsters"))
				if err == nil && howlMinMonsters >= 1 && howlMinMonsters <= 20 {
					cfg.Character.WarcryBarb.HowlMinMonsters = howlMinMonsters
				} else {
					cfg.Character.WarcryBarb.HowlMinMonsters = 4
				}
			}
			cfg.Character.WarcryBarb.UseBattleCry = r.Form.Has("warcryBarbUseBattleCry")
			if cfg.Character.WarcryBarb.UseBattleCry {
				battleCryCooldown, err := strconv.Atoi(r.Form.Get("warcryBarbBattleCryCooldown"))
				if err == nil && battleCryCooldown >= 1 && battleCryCooldown <= 60 {
					cfg.Character.WarcryBarb.BattleCryCooldown = battleCryCooldown
				} else {
					cfg.Character.WarcryBarb.BattleCryCooldown = 6
				}
				battleCryMinMonsters, err := strconv.Atoi(r.Form.Get("warcryBarbBattleCryMinMonsters"))
				if err == nil && battleCryMinMonsters >= 1 && battleCryMinMonsters <= 20 {
					cfg.Character.WarcryBarb.BattleCryMinMonsters = battleCryMinMonsters
				} else {
					cfg.Character.WarcryBarb.BattleCryMinMonsters = 1
				}
			}
			cfg.Character.WarcryBarb.UseGrimWard = r.Form.Has("warcryBarbUseGrimWard")
			cfg.Character.WarcryBarb.HorkNormalMonsters = r.Form.Has("warcryBarbHorkNormalMonsters")
			horkRange, err := strconv.Atoi(r.Form.Get("warcryBarbHorkMonsterCheckRange"))
			if err == nil && horkRange > 0 {
				cfg.Character.WarcryBarb.HorkMonsterCheckRange = horkRange
			} else {
				cfg.Character.WarcryBarb.HorkMonsterCheckRange = 7
			}
		}

		// Nova Sorceress specific options
		if cfg.Character.Class == "nova" || cfg.Character.Class == "lightsorc" {
			bossStaticThreshold, err := strconv.Atoi(r.Form.Get("novaBossStaticThreshold"))
			if err == nil {
				minThreshold := 65 // Default
				switch cfg.Game.Difficulty {
				case difficulty.Normal:
					minThreshold = 1
				case difficulty.Nightmare:
					minThreshold = 33
				case difficulty.Hell:
					minThreshold = 50
				}
				if bossStaticThreshold >= minThreshold && bossStaticThreshold <= 100 {
					cfg.Character.NovaSorceress.BossStaticThreshold = bossStaticThreshold
				} else {
					cfg.Character.NovaSorceress.BossStaticThreshold = minThreshold
					s.logger.Warn("Invalid Boss Static Threshold, setting to minimum for difficulty",
						slog.Int("min", minThreshold),
						slog.String("difficulty", string(cfg.Game.Difficulty)))
				}
			} else {
				cfg.Character.NovaSorceress.BossStaticThreshold = 65 // Default value
				s.logger.Warn("Invalid Boss Static Threshold input, setting to default", slog.Int("default", 65))
			}
		}

		// Mosaic specific options
		if cfg.Character.Class == "mosaic" {
			cfg.Character.MosaicSin.UseTigerStrike = r.Form.Has("mosaicUseTigerStrike")
			cfg.Character.MosaicSin.UseCobraStrike = r.Form.Has("mosaicUseCobraStrike")
			cfg.Character.MosaicSin.UseClawsOfThunder = r.Form.Has("mosaicUseClawsOfThunder")
			cfg.Character.MosaicSin.UseBladesOfIce = r.Form.Has("mosaicUseBladesOfIce")
			cfg.Character.MosaicSin.UseFistsOfFire = r.Form.Has("mosaicUseFistsOfFire")
		}

		// Blizzard Sorc specific options
		if cfg.Character.Class == "sorceress" {
			cfg.Character.BlizzardSorceress.UseMoatTrick = r.Form.Has("blizzardUseMoatTrick")
			cfg.Character.BlizzardSorceress.UseStaticOnMephisto = r.Form.Has("blizzardUseStaticOnMephisto")
			cfg.Character.BlizzardSorceress.UseBlizzardPackets = r.Form.Has("blizzardUseBlizzardPackets")
		}

		// Sorceress Leveling specific options
		if cfg.Character.Class == "sorceress_leveling" {
			cfg.Character.SorceressLeveling.UseMoatTrick = r.Form.Has("levelingUseMoatTrick")
			cfg.Character.SorceressLeveling.UseStaticOnMephisto = r.Form.Has("levelingUseStaticOnMephisto")
			cfg.Character.SorceressLeveling.UseBlizzardPackets = r.Form.Has("levelingUseBlizzardPackets")
			cfg.Character.SorceressLeveling.UsePacketLearning = r.Form.Has("levelingUsePacketLearning")
		}

		// Assassin Leveling specific options
		if cfg.Character.Class == "assassin" {
			cfg.Character.AssassinLeveling.UsePacketLearning = r.Form.Has("usePacketLearning")
		}

		// Amazon Leveling specific options
		if cfg.Character.Class == "amazon_leveling" {
			cfg.Character.AmazonLeveling.UsePacketLearning = r.Form.Has("usePacketLearning")
		}

		// Druid Leveling specific options
		if cfg.Character.Class == "druid_leveling" {
			cfg.Character.DruidLeveling.UsePacketLearning = r.Form.Has("usePacketLearning")
		}

		// Necromancer Leveling specific options
		if cfg.Character.Class == "necromancer" {
			cfg.Character.NecromancerLeveling.UsePacketLearning = r.Form.Has("usePacketLearning")
		}

		// Paladin Leveling specific options
		if cfg.Character.Class == "paladin" {
			cfg.Character.PaladinLeveling.UsePacketLearning = r.Form.Has("usePacketLearning")
		}

		// Nova Sorceress specific options
		if cfg.Character.Class == "nova" {
			cfg.Character.NovaSorceress.AggressiveNovaPositioning = r.Form.Has("aggressiveNovaPositioning")
			if v := r.FormValue("aggressiveSkipMinNormals"); v != "" {
				cfg.Character.NovaSorceress.AggressiveSkipMinNormals, _ = strconv.Atoi(v)
			}
		}

		// Javazon specific options
		if cfg.Character.Class == "javazon" {
			cfg.Character.Javazon.DensityKillerEnabled = r.Form.Has("javazonDensityKillerEnabled")
			if v := r.Form.Get("javazonDensityKillerIgnoreWhitesBelow"); v != "" {
				if i, err := strconv.Atoi(v); err == nil {
					cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow = i
				} else {
					cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow = 4
				}
			} else if cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow == 0 {
				cfg.Character.Javazon.DensityKillerIgnoreWhitesBelow = 4
			}
			if v := r.Form.Get("javazonDensityKillerForceRefillBelowPercent"); v != "" {
				if i, err := strconv.Atoi(v); err == nil {
					if i < 1 {
						i = 1
					}
					if i > 100 {
						i = 100
					}
					cfg.Character.Javazon.DensityKillerForceRefillBelowPercent = i
				} else {
					cfg.Character.Javazon.DensityKillerForceRefillBelowPercent = 50
				}
			} else if cfg.Character.Javazon.DensityKillerForceRefillBelowPercent == 0 {
				cfg.Character.Javazon.DensityKillerForceRefillBelowPercent = 50
			}
		}

		for y, row := range cfg.Inventory.InventoryLock {
			for x := range row {
				if r.Form.Has(fmt.Sprintf("inventoryLock[%d][%d]", y, x)) {
					cfg.Inventory.InventoryLock[y][x] = 0
				} else {
					cfg.Inventory.InventoryLock[y][x] = 1
				}
			}
		}

		copy(cfg.Inventory.BeltColumns[:], r.Form["inventoryBeltColumns[]"])

		cfg.Inventory.HealingPotionCount, _ = strconv.Atoi(r.Form.Get("healingPotionCount"))
		cfg.Inventory.ManaPotionCount, _ = strconv.Atoi(r.Form.Get("manaPotionCount"))
		cfg.Inventory.RejuvPotionCount, _ = strconv.Atoi(r.Form.Get("rejuvPotionCount"))

		// Game
		cfg.Game.CreateLobbyGames = r.Form.Has("createLobbyGames")
		cfg.Game.MinGoldPickupThreshold, _ = strconv.Atoi(r.Form.Get("gameMinGoldPickupThreshold"))
		cfg.UseCentralizedPickit = r.Form.Has("useCentralizedPickit")
		cfg.Game.UseCainIdentify = r.Form.Has("useCainIdentify")
		cfg.Game.DisableIdentifyTome = r.PostFormValue("game.disableIdentifyTome") == "on"
		cfg.Game.InteractWithShrines = r.Form.Has("interactWithShrines")
		cfg.Game.InteractWithChests = r.Form.Has("interactWithChests")
		cfg.Game.InteractWithSuperChests = r.Form.Has("interactWithSuperChests")
		cfg.Game.StopLevelingAt, _ = strconv.Atoi(r.Form.Get("stopLevelingAt"))
		cfg.Game.GameVersion = config.NormalizeGameVersion(r.Form.Get("gameVersion"))
		cfg.Game.IsNonLadderChar = r.Form.Has("isNonLadderChar")
		cfg.Game.IsHardCoreChar = r.Form.Has("isHardCoreChar")

		if v := r.Form.Get("maxGameLength"); v != "" {
			cfg.MaxGameLength, _ = strconv.Atoi(v)
		}

		// Packet Casting
		cfg.PacketCasting.UseForEntranceInteraction = r.Form.Has("packetCastingUseForEntranceInteraction")
		cfg.PacketCasting.UseForItemPickup = r.Form.Has("packetCastingUseForItemPickup")
		cfg.PacketCasting.UseForTpInteraction = r.Form.Has("packetCastingUseForTpInteraction")
		cfg.PacketCasting.UseForTeleport = r.Form.Has("packetCastingUseForTeleport")
		cfg.PacketCasting.UseForEntitySkills = r.Form.Has("packetCastingUseForEntitySkills")
		cfg.PacketCasting.UseForSkillSelection = r.Form.Has("packetCastingUseForSkillSelection")
		cfg.PacketCasting.UseForNPCInteraction = r.Form.Has("packetCastingUseForNPCInteraction")
		cfg.PacketCasting.UseForWeaponSwap = r.Form.Has("packetCastingUseForWeaponSwap")
		cfg.PacketCasting.UseForMovement = r.Form.Has("packetCastingUseForMovement")
		cfg.PacketCasting.UseForBuySell = r.Form.Has("packetCastingUseForBuySell")
		cfg.PacketCasting.UseForCubeTransmute = r.Form.Has("packetCastingUseForCubeTransmute")
		cfg.PacketCasting.UseForGamble = r.Form.Has("packetCastingUseForGamble")
		cfg.PacketCasting.UseForRepair = r.Form.Has("packetCastingUseForRepair")
		cfg.PacketCasting.UseForIdentify = r.Form.Has("packetCastingUseForIdentify")
		cfg.PacketCasting.UseForPotionUse = r.Form.Has("packetCastingUseForPotionUse")
		cfg.PacketCasting.UseForStashManagement = r.Form.Has("packetCastingUseForStashManagement")
		cfg.PacketCasting.UseForInventoryManagement = r.Form.Has("packetCastingUseForInventoryManagement")
		cfg.Game.Difficulty = difficulty.Difficulty(r.Form.Get("gameDifficulty"))
		cfg.Game.RandomizeRuns = r.Form.Has("gameRandomizeRuns")

		// Runs specific config
		enabledRuns := make([]config.Run, 0)

		// we don't like errors, so we ignore them
		json.Unmarshal([]byte(r.FormValue("gameRuns")), &enabledRuns)
		cfg.Game.Runs = enabledRuns

		s.applyShoppingFromForm(r.Form, cfg)

		cfg.Game.Cows.OpenChests = r.Form.Has("gameCowsOpenChests")

		cfg.Game.Pit.MoveThroughBlackMarsh = r.Form.Has("gamePitMoveThroughBlackMarsh")
		cfg.Game.Pit.OpenChests = r.Form.Has("gamePitOpenChests")
		cfg.Game.Pit.FocusOnElitePacks = r.Form.Has("gamePitFocusOnElitePacks")
		cfg.Game.Pit.OnlyClearLevel2 = r.Form.Has("gamePitOnlyClearLevel2")

		cfg.Game.Andariel.ClearRoom = r.Form.Has("gameAndarielClearRoom")
		cfg.Game.Andariel.UseAntidotes = r.Form.Has("gameAndarielUseAntidotes")

		cfg.Game.Countess.ClearFloors = r.Form.Has("gameCountessClearFloors")

		cfg.Game.Pindleskin.SkipOnImmunities = []stat.Resist{}
		for _, i := range r.Form["gamePindleskinSkipOnImmunities[]"] {
			cfg.Game.Pindleskin.SkipOnImmunities = append(cfg.Game.Pindleskin.SkipOnImmunities, stat.Resist(i))
		}

		cfg.Game.ArcaneSanctuary.OpenChests = r.Form.Has("gameArcaneSanctuaryOpenChests")
		cfg.Game.ArcaneSanctuary.FocusOnElitePacks = r.Form.Has("gameArcaneSanctuaryFocusOnElitePacks")

		cfg.Game.StonyTomb.OpenChests = r.Form.Has("gameStonytombOpenChests")
		cfg.Game.StonyTomb.FocusOnElitePacks = r.Form.Has("gameStonytombFocusOnElitePacks")

		cfg.Game.AncientTunnels.OpenChests = r.Form.Has("gameAncientTunnelsOpenChests")
		cfg.Game.AncientTunnels.FocusOnElitePacks = r.Form.Has("gameAncientTunnelsFocusOnElitePacks")

		cfg.Game.Duriel.UseThawing = r.Form.Has("gameDurielUseThawing")

		cfg.Game.Mausoleum.OpenChests = r.Form.Has("gameMausoleumOpenChests")
		cfg.Game.Mausoleum.FocusOnElitePacks = r.Form.Has("gameMausoleumFocusOnElitePacks")

		cfg.Game.DrifterCavern.OpenChests = r.Form.Has("gameDrifterCavernOpenChests")
		cfg.Game.DrifterCavern.FocusOnElitePacks = r.Form.Has("gameDrifterCavernFocusOnElitePacks")

		cfg.Game.SpiderCavern.OpenChests = r.Form.Has("gameSpiderCavernOpenChests")
		cfg.Game.SpiderCavern.FocusOnElitePacks = r.Form.Has("gameSpiderCavernFocusOnElitePacks")

		cfg.Game.ArachnidLair.OpenChests = r.Form.Has("gameArachnidLairOpenChests")
		cfg.Game.ArachnidLair.FocusOnElitePacks = r.Form.Has("gameArachnidLairFocusOnElitePacks")

		cfg.Game.Mephisto.KillCouncilMembers = r.Form.Has("gameMephistoKillCouncilMembers")
		cfg.Game.Mephisto.OpenChests = r.Form.Has("gameMephistoOpenChests")
		cfg.Game.Mephisto.ExitToA4 = r.Form.Has("gameMephistoExitToA4")

		cfg.Game.Tristram.ClearPortal = r.Form.Has("gameTristramClearPortal")
		cfg.Game.Tristram.FocusOnElitePacks = r.Form.Has("gameTristramFocusOnElitePacks")
		cfg.Game.Tristram.OnlyFarmRejuvs = r.Form.Has("gameTristramOnlyFarmRejuvs")

		cfg.Game.Nihlathak.ClearArea = r.Form.Has("gameNihlathakClearArea")
		cfg.Game.Summoner.KillFireEye = r.Form.Has("gameSummonerKillFireEye")

		cfg.Game.Baal.KillBaal = r.Form.Has("gameBaalKillBaal")
		cfg.Game.Baal.DollQuit = r.Form.Has("gameBaalDollQuit")
		cfg.Game.Baal.SoulQuit = r.Form.Has("gameBaalSoulQuit")
		cfg.Game.Baal.ClearFloors = r.Form.Has("gameBaalClearFloors")
		cfg.Game.Baal.OnlyElites = r.Form.Has("gameBaalOnlyElites")

		cfg.Game.Eldritch.KillShenk = r.Form.Has("gameEldritchKillShenk")

		cfg.Game.LowerKurastChest.OpenRacks = r.Form.Has("gameLowerKurastChestOpenRacks")

		cfg.Game.Diablo.StartFromStar = r.Form.Has("gameDiabloStartFromStar")
		cfg.Game.Diablo.KillDiablo = r.Form.Has("gameDiabloKillDiablo")
		cfg.Game.Diablo.FocusOnElitePacks = r.Form.Has("gameDiabloFocusOnElitePacks")
		cfg.Game.Diablo.DisableItemPickupDuringBosses = r.Form.Has("gameDiabloDisableItemPickupDuringBosses")
		cfg.Game.Diablo.AttackFromDistance = s.getIntFromForm(r, "gameLevelingHellRequiredFireRes", 0, 25, 0)
		cfg.Game.Leveling.EnsurePointsAllocation = r.Form.Has("gameLevelingEnsurePointsAllocation")
		cfg.Game.Leveling.EnsureKeyBinding = r.Form.Has("gameLevelingEnsureKeyBinding")
		cfg.Game.Leveling.AutoEquip = r.Form.Has("gameLevelingAutoEquip")
		cfg.Game.Leveling.AutoEquipFromSharedStash = r.Form.Has("gameLevelingAutoEquipFromSharedStash")
		cfg.Game.Leveling.NightmareRequiredLevel = s.getIntFromForm(r, "gameLevelingNightmareRequiredLevel", 1, 99, 41)
		cfg.Game.Leveling.HellRequiredLevel = s.getIntFromForm(r, "gameLevelingHellRequiredLevel", 1, 99, 70)
		cfg.Game.Leveling.HellRequiredFireRes = s.getIntFromForm(r, "gameLevelingHellRequiredFireRes", -100, 75, 15)
		cfg.Game.Leveling.HellRequiredLightRes = s.getIntFromForm(r, "gameLevelingHellRequiredLightRes", -100, 75, -10)

		cfg.Game.LevelingSequence.SequenceFile = r.Form.Get("gameLevelingSequenceFile")

		// Quests options for Act 1
		cfg.Game.Quests.ClearDen = r.Form.Has("gameQuestsClearDen")
		cfg.Game.Quests.RescueCain = r.Form.Has("gameQuestsRescueCain")
		cfg.Game.Quests.RetrieveHammer = r.Form.Has("gameQuestsRetrieveHammer")
		// Quests options for Act 2
		cfg.Game.Quests.KillRadament = r.Form.Has("gameQuestsKillRadament")
		cfg.Game.Quests.GetCube = r.Form.Has("gameQuestsGetCube")
		// Quests options for Act 3
		cfg.Game.Quests.RetrieveBook = r.Form.Has("gameQuestsRetrieveBook")
		// Quests options for Act 4
		cfg.Game.Quests.KillIzual = r.Form.Has("gameQuestsKillIzual")
		// Quests options for Act 5
		cfg.Game.Quests.KillShenk = r.Form.Has("gameQuestsKillShenk")
		cfg.Game.Quests.RescueAnya = r.Form.Has("gameQuestsRescueAnya")
		cfg.Game.Quests.KillAncients = r.Form.Has("gameQuestsKillAncients")

		cfg.Game.ActRuns.Act1.FocusOnElitePacks = r.Form.Has("gameActRunsAct1FocusOnElitePacks")
		cfg.Game.ActRuns.Act1.OpenChests = r.Form.Has("gameActRunsAct1OpenChests")
		cfg.Game.ActRuns.Act1.UseWorldStoneShard = r.Form.Has("gameActRunsAct1UseWorldStoneShard")
		cfg.Game.ActRuns.Act1.PauseAfterRun = r.Form.Has("gameActRunsAct1PauseAfterRun")

		cfg.Game.ActRuns.Act2.FocusOnElitePacks = r.Form.Has("gameActRunsAct2FocusOnElitePacks")
		cfg.Game.ActRuns.Act2.OpenChests = r.Form.Has("gameActRunsAct2OpenChests")
		cfg.Game.ActRuns.Act2.UseWorldStoneShard = r.Form.Has("gameActRunsAct2UseWorldStoneShard")
		cfg.Game.ActRuns.Act2.PauseAfterRun = r.Form.Has("gameActRunsAct2PauseAfterRun")

		cfg.Game.ActRuns.Act3.FocusOnElitePacks = r.Form.Has("gameActRunsAct3FocusOnElitePacks")
		cfg.Game.ActRuns.Act3.OpenChests = r.Form.Has("gameActRunsAct3OpenChests")
		cfg.Game.ActRuns.Act3.UseWorldStoneShard = r.Form.Has("gameActRunsAct3UseWorldStoneShard")
		cfg.Game.ActRuns.Act3.PauseAfterRun = r.Form.Has("gameActRunsAct3PauseAfterRun")

		cfg.Game.ActRuns.Act4.FocusOnElitePacks = r.Form.Has("gameActRunsAct4FocusOnElitePacks")
		cfg.Game.ActRuns.Act4.OpenChests = r.Form.Has("gameActRunsAct4OpenChests")
		cfg.Game.ActRuns.Act4.UseWorldStoneShard = r.Form.Has("gameActRunsAct4UseWorldStoneShard")
		cfg.Game.ActRuns.Act4.PauseAfterRun = r.Form.Has("gameActRunsAct4PauseAfterRun")

		cfg.Game.ActRuns.Act5.FocusOnElitePacks = r.Form.Has("gameActRunsAct5FocusOnElitePacks")
		cfg.Game.ActRuns.Act5.OpenChests = r.Form.Has("gameActRunsAct5OpenChests")
		cfg.Game.ActRuns.Act5.UseWorldStoneShard = r.Form.Has("gameActRunsAct5UseWorldStoneShard")
		cfg.Game.ActRuns.Act5.PauseAfterRun = r.Form.Has("gameActRunsAct5PauseAfterRun")

		cfg.Game.TerrorZone.FocusOnElitePacks = r.Form.Has("gameTerrorZoneFocusOnElitePacks")
		cfg.Game.TerrorZone.SkipOtherRuns = r.Form.Has("gameTerrorZoneSkipOtherRuns")
		cfg.Game.TerrorZone.OpenChests = r.Form.Has("gameTerrorZoneOpenChests")

		cfg.Game.TerrorZone.SkipOnImmunities = []stat.Resist{}
		for _, i := range r.Form["gameTerrorZoneSkipOnImmunities[]"] {
			cfg.Game.TerrorZone.SkipOnImmunities = append(cfg.Game.TerrorZone.SkipOnImmunities, stat.Resist(i))
		}

		tzAreas := make([]area.ID, 0)
		for _, a := range r.Form["gameTerrorZoneAreas[]"] {
			ID, _ := strconv.Atoi(a)
			tzAreas = append(tzAreas, area.ID(ID))
		}
		cfg.Game.TerrorZone.Areas = tzAreas

		// Utility
		if parkingActStr := r.Form.Get("gameUtilityParkingAct"); parkingActStr != "" {
			if parkingAct, err := strconv.Atoi(parkingActStr); err == nil {
				cfg.Game.Utility.ParkingAct = parkingAct
			}
		}

		// Gambling
		cfg.Gambling.Enabled = r.Form.Has("gamblingEnabled")
		if raw := strings.TrimSpace(r.Form.Get("gamblingItems")); raw != "" {
			parts := strings.Split(raw, ",")
			items := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					items = append(items, p)
				}
			}
			cfg.Gambling.Items = items
		} else {
			cfg.Gambling.Items = []string{}
		}

		// Cube Recipes
		cfg.CubeRecipes.Enabled = r.Form.Has("enableCubeRecipes")
		enabledRecipes := r.Form["enabledRecipes"]
		cfg.CubeRecipes.EnabledRecipes = enabledRecipes
		cfg.CubeRecipes.SkipPerfectAmethysts = r.Form.Has("skipPerfectAmethysts")
		cfg.CubeRecipes.SkipPerfectRubies = r.Form.Has("skipPerfectRubies")
		// New: parse jewelsToKeep
		if v := r.Form.Get("jewelsToKeep"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				cfg.CubeRecipes.JewelsToKeep = n
			} else {
				cfg.CubeRecipes.JewelsToKeep = 1 // sensible default
			}
		}
		// Companion config
		cfg.Companion.Enabled = r.Form.Has("companionEnabled")
		cfg.Companion.Leader = r.Form.Has("companionLeader")
		cfg.Companion.LeaderName = r.Form.Get("companionLeaderName")
		cfg.Companion.GameNameTemplate = r.Form.Get("companionGameNameTemplate")
		cfg.Companion.GamePassword = r.Form.Get("companionGamePassword")
		cfg.Companion.WaitForParty = r.Form.Has("companionWaitForParty")
		if v := r.Form.Get("companionPartyWaitTimeout"); v != "" {
			cfg.Companion.PartyWaitTimeout, _ = strconv.Atoi(v)
		}
		cfg.Companion.OpenTPForPlayer = r.Form.Has("companionOpenTPForPlayer")
		cfg.Companion.BonusRuns = r.Form.Has("companionBonusRuns")
		cfg.Companion.BonusRunsList = r.Form["companionBonusRunsList"]
		cfg.Companion.RandomGameNames = r.Form.Has("companionRandomGameNames")
		cfg.Companion.LeaderPriorityRuns = r.Form.Has("companionLeaderPriorityRuns")

		// Back to town config
		cfg.BackToTown.NoHpPotions = r.Form.Has("noHpPotions")
		cfg.BackToTown.NoMpPotions = r.Form.Has("noMpPotions")
		cfg.BackToTown.MercDied = r.Form.Has("mercDied")
		cfg.BackToTown.EquipmentBroken = r.Form.Has("equipmentBroken")

		// Scheduler
		cfg.Scheduler.Enabled = r.Form.Has("schedulerEnabled")
		cfg.Scheduler.Mode = r.Form.Get("schedulerMode")
		if cfg.Scheduler.Mode == "" {
			cfg.Scheduler.Mode = "timeSlots"
		}

		// Global variance for time slots mode
		if v := r.Form.Get("globalVarianceMin"); v != "" {
			cfg.Scheduler.GlobalVarianceMin, _ = strconv.Atoi(v)
		}

		// Reset scheduler days if we are updating them
		if len(cfg.Scheduler.Days) != 7 {
			cfg.Scheduler.Days = make([]config.Day, 7)
		}

		// Parse time slots mode data
		for day := 0; day < 7; day++ {
			starts := r.Form[fmt.Sprintf("scheduler[%d][start][]", day)]
			ends := r.Form[fmt.Sprintf("scheduler[%d][end][]", day)]
			startVars := r.Form[fmt.Sprintf("scheduler[%d][startVar][]", day)]
			endVars := r.Form[fmt.Sprintf("scheduler[%d][endVar][]", day)]

			cfg.Scheduler.Days[day].DayOfWeek = day
			cfg.Scheduler.Days[day].TimeRanges = make([]config.TimeRange, 0)

			for i := 0; i < len(starts); i++ {
				start, err := time.Parse("15:04", starts[i])
				if err != nil {
					continue
				}
				end, err := time.Parse("15:04", ends[i])
				if err != nil {
					continue
				}

				var startVar, endVar int
				if i < len(startVars) {
					startVar, _ = strconv.Atoi(startVars[i])
				}
				if i < len(endVars) {
					endVar, _ = strconv.Atoi(endVars[i])
				}

				cfg.Scheduler.Days[day].TimeRanges = append(cfg.Scheduler.Days[day].TimeRanges, config.TimeRange{
					Start:            start,
					End:              end,
					StartVarianceMin: startVar,
					EndVarianceMin:   endVar,
				})
			}
		}

		// Parse duration mode data
		cfg.Scheduler.Duration.WakeUpTime = r.Form.Get("durationWakeUpTime")
		if v := r.Form.Get("durationWakeUpVariance"); v != "" {
			cfg.Scheduler.Duration.WakeUpVariance, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationPlayHours"); v != "" {
			cfg.Scheduler.Duration.PlayHours, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationPlayHoursVariance"); v != "" {
			cfg.Scheduler.Duration.PlayHoursVariance, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationMealBreakCount"); v != "" {
			cfg.Scheduler.Duration.MealBreakCount, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationMealBreakDuration"); v != "" {
			cfg.Scheduler.Duration.MealBreakDuration, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationMealBreakVariance"); v != "" {
			cfg.Scheduler.Duration.MealBreakVariance, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationShortBreakCount"); v != "" {
			cfg.Scheduler.Duration.ShortBreakCount, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationShortBreakDuration"); v != "" {
			cfg.Scheduler.Duration.ShortBreakDuration, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationShortBreakVariance"); v != "" {
			cfg.Scheduler.Duration.ShortBreakVariance, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationBreakTimingVariance"); v != "" {
			cfg.Scheduler.Duration.BreakTimingVariance, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationJitterMin"); v != "" {
			cfg.Scheduler.Duration.JitterMin, _ = strconv.Atoi(v)
		}
		if v := r.Form.Get("durationJitterMax"); v != "" {
			cfg.Scheduler.Duration.JitterMax, _ = strconv.Atoi(v)
		}

		// Muling
		cfg.Muling.Enabled = r.FormValue("mulingEnabled") == "on"

		// Validate mule profiles - filter out any deleted mule profiles
		requestedMuleProfiles := r.Form["mulingMuleProfiles[]"]
		validMuleProfiles := []string{}
		allCharacters := config.GetCharacters()
		for _, muleName := range requestedMuleProfiles {
			if muleCfg, exists := allCharacters[muleName]; exists && strings.ToLower(muleCfg.Character.Class) == "mule" {
				validMuleProfiles = append(validMuleProfiles, muleName)
			}
		}
		cfg.Muling.MuleProfiles = validMuleProfiles

		cfg.Muling.ReturnTo = r.FormValue("mulingReturnTo")
		favoriteRuns := sanitizeFavoriteRunSelection(r.Form["runFavoriteRuns"])
		config.App.RunFavoriteRuns = favoriteRuns
		if err := config.SaveAppConfig(config.App); err != nil {
			s.logger.Error("Failed to save run favorites", slog.Any("error", err))
		}
		config.SaveSupervisorConfig(supervisorName, cfg)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	supervisor := r.URL.Query().Get("supervisor")
	cloneSource := ""
	cloneParam := r.URL.Query().Get("clone")
	cfg, _ := config.GetCharacter("template")
	if supervisor != "" {
		if cfgLoaded, ok := config.GetCharacter(supervisor); ok && cfgLoaded != nil {
			cfg = cfgLoaded
		}
		cloneParam = ""
	} else if cloneParam != "" {
		if cfgLoaded, ok := config.GetCharacter(cloneParam); ok && cfgLoaded != nil {
			tmp := *cfgLoaded
			cfg = &tmp
			cloneSource = cloneParam
		}
	}
	skillOptions := buildSkillOptionsForBuild(cfg.Character.Class)

	enabledRuns := make([]string, 0)
	for _, run := range cfg.Game.Runs {
		if run == config.UberIzualRun || run == config.UberDurielRun || run == config.LilithRun {
			continue
		}
		enabledRuns = append(enabledRuns, string(run))
	}
	disabledRuns := make([]string, 0)
	for run := range config.AvailableRuns {
		if run == config.UberIzualRun || run == config.UberDurielRun || run == config.LilithRun {
			continue
		}
		if !slices.Contains(cfg.Game.Runs, run) {
			disabledRuns = append(disabledRuns, string(run))
		}
	}
	sort.Strings(disabledRuns)

	if len(cfg.Scheduler.Days) == 0 {
		cfg.Scheduler.Days = make([]config.Day, 7)
		for i := 0; i < 7; i++ {
			cfg.Scheduler.Days[i] = config.Day{DayOfWeek: i}
		}
	}

	dayNames := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

	// Get list of mule profiles (for farmer's mule dropdown)
	// and farmer profiles (for mule's return character dropdown)
	muleProfiles := []string{}
	farmerProfiles := []string{}
	allCharacters := config.GetCharacters()
	supervisors := make([]string, 0, len(allCharacters))
	for profileName, profileCfg := range allCharacters {
		if profileName == "template" {
			continue
		}
		supervisors = append(supervisors, profileName)
		if strings.ToLower(profileCfg.Character.Class) == "mule" {
			muleProfiles = append(muleProfiles, profileName)
		} else {
			farmerProfiles = append(farmerProfiles, profileName)
		}
	}
	sort.Strings(supervisors)
	sort.Strings(muleProfiles)
	sort.Strings(farmerProfiles)

	// Filter out any invalid mule profiles from the config before rendering
	// This prevents form validation errors when deleted mules are still referenced
	validConfigMuleProfiles := []string{}
	for _, muleName := range cfg.Muling.MuleProfiles {
		if muleCfg, exists := allCharacters[muleName]; exists && strings.ToLower(muleCfg.Character.Class) == "mule" {
			validConfigMuleProfiles = append(validConfigMuleProfiles, muleName)
		}
	}
	cfg.Muling.MuleProfiles = validConfigMuleProfiles

	s.templates.ExecuteTemplate(w, "character_settings.gohtml", CharacterSettings{
		Version:               config.Version,
		Supervisor:            supervisor,
		CloneSource:           cloneSource,
		Config:                cfg,
		SkillOptions:          skillOptions,
		SkillPrereqs:          buildSkillPrereqsForBuild(cfg.Character.Class),
		DayNames:              dayNames,
		EnabledRuns:           enabledRuns,
		DisabledRuns:          disabledRuns,
		TerrorZoneGroups:      buildTZGroups(),
		RecipeList:            config.AvailableRecipes,
		RunewordRecipeList:    availableRunewordRecipesForCharacter(cfg),
		RunFavoriteRuns:       config.App.RunFavoriteRuns,
		AvailableProfiles:     muleProfiles,
		FarmerProfiles:        farmerProfiles,
		LevelingSequenceFiles: sequenceFiles,
		Supervisors:           supervisors,
		ShortBonusRuns:        shortBonusRunNames(),
		Realms:                secrets.Realms(),
	})
}

func shortBonusRunNames() []string {
	names := make([]string, len(config.ShortBonusRuns))
	for i, r := range config.ShortBonusRuns {
		names[i] = string(r)
	}
	return names
}

func (s *HttpServer) listLevelingSequenceFiles() []string {
	if s.sequenceAPI == nil {
		return nil
	}
	files, err := s.sequenceAPI.ListSequenceFiles()
	if err != nil {
		s.logger.Error("failed to list leveling sequences", slog.Any("error", err))
		return nil
	}
	return files
}

// companionJoin handles requests to force a companion to join a game
// partySetLeader sets a supervisor as a party leader.
func (s *HttpServer) partySetLeader(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisor string `json:"supervisor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	cfg, found := config.GetCharacter(req.Supervisor)
	if !found || cfg == nil {
		http.Error(w, "Supervisor not found", http.StatusNotFound)
		return
	}

	cfg.Companion.Enabled = true
	cfg.Companion.Leader = true
	cfg.Companion.WaitForParty = true
	cfg.Companion.OpenTPForPlayer = true
	if err := config.SaveSupervisorConfig(req.Supervisor, cfg); err != nil {
		http.Error(w, "Failed to save config", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// partySetFollower sets a supervisor as a follower of a given leader.
func (s *HttpServer) partySetFollower(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisor string `json:"supervisor"`
		Leader     string `json:"leader"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Supervisor == req.Leader {
		http.Error(w, "Cannot follow yourself", http.StatusBadRequest)
		return
	}

	followerCfg, found := config.GetCharacter(req.Supervisor)
	if !found || followerCfg == nil {
		http.Error(w, "Follower supervisor not found", http.StatusNotFound)
		return
	}

	leaderCfg, found := config.GetCharacter(req.Leader)
	if !found || leaderCfg == nil {
		http.Error(w, "Leader supervisor not found", http.StatusNotFound)
		return
	}

	if !leaderCfg.Companion.Enabled || !leaderCfg.Companion.Leader {
		leaderCfg.Companion.Enabled = true
		leaderCfg.Companion.Leader = true
		leaderCfg.Companion.WaitForParty = true
		if err := config.SaveSupervisorConfig(req.Leader, leaderCfg); err != nil {
			http.Error(w, "Failed to save leader config", http.StatusInternalServerError)
			return
		}
	}

	followerCfg.Companion.Enabled = true
	followerCfg.Companion.Leader = false
	followerCfg.Companion.LeaderName = leaderCfg.CharacterName
	followerCfg.Companion.WaitForParty = true
	if err := config.SaveSupervisorConfig(req.Supervisor, followerCfg); err != nil {
		http.Error(w, "Failed to save follower config", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// partyRemove removes a supervisor from the party.
func (s *HttpServer) partyRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Supervisor string `json:"supervisor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	cfg, found := config.GetCharacter(req.Supervisor)
	if !found || cfg == nil {
		http.Error(w, "Supervisor not found", http.StatusNotFound)
		return
	}

	wasLeader := cfg.Companion.Leader
	leaderCharName := cfg.CharacterName

	cfg.Companion.Enabled = false
	cfg.Companion.Leader = false
	cfg.Companion.LeaderName = ""
	cfg.Companion.WaitForParty = false
	if err := config.SaveSupervisorConfig(req.Supervisor, cfg); err != nil {
		http.Error(w, "Failed to save config", http.StatusInternalServerError)
		return
	}

	if wasLeader {
		for name := range config.GetCharacters() {
			if name == "template" || name == req.Supervisor {
				continue
			}
			followerCfg, ok := config.GetCharacter(name)
			if !ok || followerCfg == nil {
				continue
			}
			if followerCfg.Companion.Enabled && !followerCfg.Companion.Leader &&
				followerCfg.Companion.LeaderName == leaderCharName {
				followerCfg.Companion.Enabled = false
				followerCfg.Companion.Leader = false
				followerCfg.Companion.LeaderName = ""
				followerCfg.Companion.WaitForParty = false
				config.SaveSupervisorConfig(name, followerCfg)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// applyShoppingFromForm parses shopping-specific fields (used in updateConfigFromForm)
func (s *HttpServer) applyShoppingFromForm(values url.Values, cfg *config.CharacterCfg) {
	cfg.Shopping.Enabled = values.Has("shoppingEnabled")

	if v, err := strconv.Atoi(values.Get("shoppingMaxGoldToSpend")); err == nil {
		cfg.Shopping.MaxGoldToSpend = v
	}
	if v, err := strconv.Atoi(values.Get("shoppingMinGoldReserve")); err == nil {
		cfg.Shopping.MinGoldReserve = v
	}
	if v, err := strconv.Atoi(values.Get("shoppingRefreshesPerRun")); err == nil {
		cfg.Shopping.RefreshesPerRun = v
	}

	cfg.Shopping.ShoppingRulesFile = values.Get("shoppingRulesFile")

	if raw := strings.TrimSpace(values.Get("shoppingItemTypes")); raw != "" {
		parts := strings.Split(raw, ",")
		items := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				items = append(items, p)
			}
		}
		cfg.Shopping.ItemTypes = items
	} else {
		cfg.Shopping.ItemTypes = []string{}
	}

	cfg.Shopping.VendorAkara = values.Has("shoppingVendorAkara")
	cfg.Shopping.VendorCharsi = values.Has("shoppingVendorCharsi")
	cfg.Shopping.VendorGheed = values.Has("shoppingVendorGheed")
	cfg.Shopping.VendorFara = values.Has("shoppingVendorFara")
	cfg.Shopping.VendorDrognan = values.Has("shoppingVendorDrognan")
	cfg.Shopping.VendorElzix = values.Has("shoppingVendorElzix")
	cfg.Shopping.VendorOrmus = values.Has("shoppingVendorOrmus")
	cfg.Shopping.VendorMalah = values.Has("shoppingVendorMalah")
	cfg.Shopping.VendorAnya = values.Has("shoppingVendorAnya")
}

func (s *HttpServer) applyRunDetails(values url.Values, cfg *config.CharacterCfg, runs []string) {
	for _, runID := range runs {
		switch runID {
		case "andariel":
			cfg.Game.Andariel.ClearRoom = values.Has("gameAndarielClearRoom")
			cfg.Game.Andariel.UseAntidotes = values.Has("gameAndarielUseAntidotes")
		case "countess":
			cfg.Game.Countess.ClearFloors = values.Has("gameCountessClearFloors")
		case "duriel":
			cfg.Game.Duriel.UseThawing = values.Has("gameDurielUseThawing")
		case "pit":
			cfg.Game.Pit.MoveThroughBlackMarsh = values.Has("gamePitMoveThroughBlackMarsh")
			cfg.Game.Pit.OpenChests = values.Has("gamePitOpenChests")
			cfg.Game.Pit.FocusOnElitePacks = values.Has("gamePitFocusOnElitePacks")
			cfg.Game.Pit.OnlyClearLevel2 = values.Has("gamePitOnlyClearLevel2")
		case "cows":
			cfg.Game.Cows.OpenChests = values.Has("gameCowsOpenChests")
		case "pindleskin":
			if raw, ok := values["gamePindleskinSkipOnImmunities[]"]; ok {
				skips := make([]stat.Resist, 0, len(raw))
				for _, v := range raw {
					if v == "" {
						continue
					}
					skips = append(skips, stat.Resist(v))
				}
				cfg.Game.Pindleskin.SkipOnImmunities = skips
			} else {
				cfg.Game.Pindleskin.SkipOnImmunities = nil
			}
		case "arcane_sanctuary":
			cfg.Game.ArcaneSanctuary.OpenChests = values.Has("gameArcaneSanctuaryOpenChests")
			cfg.Game.ArcaneSanctuary.FocusOnElitePacks = values.Has("gameArcaneSanctuaryFocusOnElitePacks")
		case "stony_tomb":
			cfg.Game.StonyTomb.OpenChests = values.Has("gameStonytombOpenChests")
			cfg.Game.StonyTomb.FocusOnElitePacks = values.Has("gameStonytombFocusOnElitePacks")
		case "mausoleum":
			cfg.Game.Mausoleum.OpenChests = values.Has("gameMausoleumOpenChests")
			cfg.Game.Mausoleum.FocusOnElitePacks = values.Has("gameMausoleumFocusOnElitePacks")
		case "ancient_tunnels":
			cfg.Game.AncientTunnels.OpenChests = values.Has("gameAncientTunnelsOpenChests")
			cfg.Game.AncientTunnels.FocusOnElitePacks = values.Has("gameAncientTunnelsFocusOnElitePacks")
		case "drifter_cavern":
			cfg.Game.DrifterCavern.OpenChests = values.Has("gameDrifterCavernOpenChests")
			cfg.Game.DrifterCavern.FocusOnElitePacks = values.Has("gameDrifterCavernFocusOnElitePacks")
		case "spider_cavern":
			cfg.Game.SpiderCavern.OpenChests = values.Has("gameSpiderCavernOpenChests")
			cfg.Game.SpiderCavern.FocusOnElitePacks = values.Has("gameSpiderCavernFocusOnElitePacks")
		case "arachnid_lair":
			cfg.Game.ArachnidLair.OpenChests = values.Has("gameArachnidLairOpenChests")
			cfg.Game.ArachnidLair.FocusOnElitePacks = values.Has("gameArachnidLairFocusOnElitePacks")
		case "mephisto":
			cfg.Game.Mephisto.KillCouncilMembers = values.Has("gameMephistoKillCouncilMembers")
			cfg.Game.Mephisto.OpenChests = values.Has("gameMephistoOpenChests")
			cfg.Game.Mephisto.ExitToA4 = values.Has("gameMephistoExitToA4")
		case "tristram":
			cfg.Game.Tristram.ClearPortal = values.Has("gameTristramClearPortal")
			cfg.Game.Tristram.FocusOnElitePacks = values.Has("gameTristramFocusOnElitePacks")
			cfg.Game.Tristram.OnlyFarmRejuvs = values.Has("gameTristramOnlyFarmRejuvs")
		case "nihlathak":
			cfg.Game.Nihlathak.ClearArea = values.Has("gameNihlathakClearArea")
		case "summoner":
			cfg.Game.Summoner.KillFireEye = values.Has("gameSummonerKillFireEye")
		case "baal":
			cfg.Game.Baal.KillBaal = values.Has("gameBaalKillBaal")
			cfg.Game.Baal.DollQuit = values.Has("gameBaalDollQuit")
			cfg.Game.Baal.SoulQuit = values.Has("gameBaalSoulQuit")
			cfg.Game.Baal.ClearFloors = values.Has("gameBaalClearFloors")
			cfg.Game.Baal.OnlyElites = values.Has("gameBaalOnlyElites")
		case "eldritch":
			cfg.Game.Eldritch.KillShenk = values.Has("gameEldritchKillShenk")
		case "lower_kurast_chest":
			cfg.Game.LowerKurastChest.OpenRacks = values.Has("gameLowerKurastChestOpenRacks")
		case "diablo":
			cfg.Game.Diablo.KillDiablo = values.Has("gameDiabloKillDiablo")
			cfg.Game.Diablo.DisableItemPickupDuringBosses = values.Has("gameDiabloDisableItemPickupDuringBosses")
			cfg.Game.Diablo.StartFromStar = values.Has("gameDiabloStartFromStar")
			cfg.Game.Diablo.FocusOnElitePacks = values.Has("gameDiabloFocusOnElitePacks")
			if v := values.Get("gameDiabloAttackFromDistance"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					if n < 0 {
						n = 0
					} else if n > 25 {
						n = 25
					}
					cfg.Game.Diablo.AttackFromDistance = n
				}
			}
		case "leveling":
			cfg.Game.Leveling.EnsurePointsAllocation = values.Has("gameLevelingEnsurePointsAllocation")
			cfg.Game.Leveling.EnsureKeyBinding = values.Has("gameLevelingEnsureKeyBinding")
			cfg.Game.Leveling.AutoEquip = values.Has("gameLevelingAutoEquip")
			cfg.Game.Leveling.AutoEquipFromSharedStash = values.Has("gameLevelingAutoEquipFromSharedStash")
			if v := values.Get("gameLevelingNightmareRequiredLevel"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					if n < 0 {
						n = 0
					} else if n > 99 {
						n = 99
					}
					cfg.Game.Leveling.NightmareRequiredLevel = n
				}
			}
			if v := values.Get("gameLevelingHellRequiredLevel"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					if n < 0 {
						n = 0
					} else if n > 99 {
						n = 99
					}
					cfg.Game.Leveling.HellRequiredLevel = n
				}
			}
			if v := values.Get("gameLevelingHellRequiredFireRes"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					if n < -100 {
						n = -100
					} else if n > 75 {
						n = 75
					}
					cfg.Game.Leveling.HellRequiredFireRes = n
				}
			}
			if v := values.Get("gameLevelingHellRequiredLightRes"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					if n < -100 {
						n = -100
					} else if n > 75 {
						n = 75
					}
					cfg.Game.Leveling.HellRequiredLightRes = n
				}
			}
		case "leveling_sequence":
			cfg.Game.LevelingSequence.SequenceFile = values.Get("gameLevelingSequenceFile")
		case "quests":
			cfg.Game.Quests.ClearDen = values.Has("gameQuestsClearDen")
			cfg.Game.Quests.RescueCain = values.Has("gameQuestsRescueCain")
			cfg.Game.Quests.RetrieveHammer = values.Has("gameQuestsRetrieveHammer")
			cfg.Game.Quests.KillRadament = values.Has("gameQuestsKillRadament")
			cfg.Game.Quests.GetCube = values.Has("gameQuestsGetCube")
			cfg.Game.Quests.RetrieveBook = values.Has("gameQuestsRetrieveBook")
			cfg.Game.Quests.KillIzual = values.Has("gameQuestsKillIzual")
			cfg.Game.Quests.KillShenk = values.Has("gameQuestsKillShenk")
			cfg.Game.Quests.RescueAnya = values.Has("gameQuestsRescueAnya")
			cfg.Game.Quests.KillAncients = values.Has("gameQuestsKillAncients")
		case "act1":
			cfg.Game.ActRuns.Act1.FocusOnElitePacks = values.Has("gameActRunsAct1FocusOnElitePacks")
			cfg.Game.ActRuns.Act1.OpenChests = values.Has("gameActRunsAct1OpenChests")
			cfg.Game.ActRuns.Act1.UseWorldStoneShard = values.Has("gameActRunsAct1UseWorldStoneShard")
			cfg.Game.ActRuns.Act1.PauseAfterRun = values.Has("gameActRunsAct1PauseAfterRun")
		case "act2":
			cfg.Game.ActRuns.Act2.FocusOnElitePacks = values.Has("gameActRunsAct2FocusOnElitePacks")
			cfg.Game.ActRuns.Act2.OpenChests = values.Has("gameActRunsAct2OpenChests")
			cfg.Game.ActRuns.Act2.UseWorldStoneShard = values.Has("gameActRunsAct2UseWorldStoneShard")
			cfg.Game.ActRuns.Act2.PauseAfterRun = values.Has("gameActRunsAct2PauseAfterRun")
		case "act3":
			cfg.Game.ActRuns.Act3.FocusOnElitePacks = values.Has("gameActRunsAct3FocusOnElitePacks")
			cfg.Game.ActRuns.Act3.OpenChests = values.Has("gameActRunsAct3OpenChests")
			cfg.Game.ActRuns.Act3.UseWorldStoneShard = values.Has("gameActRunsAct3UseWorldStoneShard")
			cfg.Game.ActRuns.Act3.PauseAfterRun = values.Has("gameActRunsAct3PauseAfterRun")
		case "act4":
			cfg.Game.ActRuns.Act4.FocusOnElitePacks = values.Has("gameActRunsAct4FocusOnElitePacks")
			cfg.Game.ActRuns.Act4.OpenChests = values.Has("gameActRunsAct4OpenChests")
			cfg.Game.ActRuns.Act4.UseWorldStoneShard = values.Has("gameActRunsAct4UseWorldStoneShard")
			cfg.Game.ActRuns.Act4.PauseAfterRun = values.Has("gameActRunsAct4PauseAfterRun")
		case "act5":
			cfg.Game.ActRuns.Act5.FocusOnElitePacks = values.Has("gameActRunsAct5FocusOnElitePacks")
			cfg.Game.ActRuns.Act5.OpenChests = values.Has("gameActRunsAct5OpenChests")
			cfg.Game.ActRuns.Act5.UseWorldStoneShard = values.Has("gameActRunsAct5UseWorldStoneShard")
			cfg.Game.ActRuns.Act5.PauseAfterRun = values.Has("gameActRunsAct5PauseAfterRun")
		case "terror_zone":
			cfg.Game.TerrorZone.FocusOnElitePacks = values.Has("gameTerrorZoneFocusOnElitePacks")
			cfg.Game.TerrorZone.SkipOtherRuns = values.Has("gameTerrorZoneSkipOtherRuns")
			cfg.Game.TerrorZone.OpenChests = values.Has("gameTerrorZoneOpenChests")

			if raw, ok := values["gameTerrorZoneSkipOnImmunities[]"]; ok {
				skips := make([]stat.Resist, 0, len(raw))
				for _, v := range raw {
					if v == "" {
						continue
					}
					skips = append(skips, stat.Resist(v))
				}
				cfg.Game.TerrorZone.SkipOnImmunities = skips
			} else {
				cfg.Game.TerrorZone.SkipOnImmunities = nil
			}

			if raw, ok := values["gameTerrorZoneAreas[]"]; ok {
				areas := make([]area.ID, 0, len(raw))
				for _, v := range raw {
					if v == "" {
						continue
					}
					if id, err := strconv.Atoi(v); err == nil {
						areas = append(areas, area.ID(id))
					}
				}
				cfg.Game.TerrorZone.Areas = areas
			} else {
				cfg.Game.TerrorZone.Areas = nil
			}
		case "utility":
			if v := values.Get("gameUtilityParkingAct"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					cfg.Game.Utility.ParkingAct = n
				}
			}
		case "shopping":
			// Handled in applyShoppingFromForm, repeated here just for run detail completeness if needed
		}
	}
}

func (s *HttpServer) bulkApplyCharacterSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SourceSupervisor  string              `json:"sourceSupervisor"`
		TargetSupervisors []string            `json:"targetSupervisors"`
		Sections          ConfigUpdateOptions `json:"sections"` // Use the shared struct
		RunDetailTargets  []string            `json:"runDetailTargets"`
		Form              map[string][]string `json:"form"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	targets := map[string]struct{}{}
	if req.SourceSupervisor != "" {
		targets[req.SourceSupervisor] = struct{}{}
	}
	for _, t := range req.TargetSupervisors {
		if t == "" {
			continue
		}
		targets[t] = struct{}{}
	}

	if len(targets) == 0 {
		http.Error(w, "no supervisors specified", http.StatusBadRequest)
		return
	}

	// Convert JSON Form map to url.Values
	values := url.Values{}
	for k, arr := range req.Form {
		for _, v := range arr {
			values.Add(k, v)
		}
	}

	// Save source supervisor's form data first so bulk apply uses current form state (not saved state)
	// This fixes UX issue where bulk apply button is next to save, but would apply old saved values
	if req.SourceSupervisor != "" {
		if sourceCfg, found := config.GetCharacter(req.SourceSupervisor); found && sourceCfg != nil {
			if err := s.updateConfigFromForm(values, sourceCfg, req.Sections, req.RunDetailTargets); err == nil {
				if err := config.SaveSupervisorConfig(req.SourceSupervisor, sourceCfg); err != nil {
					s.logger.Warn("failed to save source supervisor config", slog.String("supervisor", req.SourceSupervisor), slog.Any("error", err))
				}
			}
		}
	}

	// Now get the runeword source from the freshly saved config
	var runewordSource *config.CharacterCfg
	if req.Sections.RunewordMaker && req.SourceSupervisor != "" {
		if src, ok := config.GetCharacter(req.SourceSupervisor); ok && src != nil {
			if cloned, err := cloneCharacterCfg(src); err == nil {
				runewordSource = cloned
			} else {
				s.logger.Warn("failed to clone runeword settings", slog.String("supervisor", req.SourceSupervisor), slog.Any("error", err))
			}
		}
	}

	for name := range targets {
		cfg, found := config.GetCharacter(name)
		if !found || cfg == nil {
			continue
		}

		if req.Sections.RunewordMaker && runewordSource != nil {
			applyRunewordSettings(cfg, runewordSource)
		}

		// Ensure Identity, Muling, Shopping are NOT applied by default in Bulk Apply unless specified
		// The client currently sends `ConfigUpdateOptions` which matches the JS struct.
		// Muling/Shopping keys might be missing in JS struct, defaulting to false (which is safe).
		// UpdateAllRunDetails is defaulted to false for Bulk Apply (desired).

		if err := s.updateConfigFromForm(values, cfg, req.Sections, req.RunDetailTargets); err != nil {
			s.logger.Error("failed to apply config", slog.String("supervisor", name), slog.Any("error", err))
			continue
		}

		if err := config.SaveSupervisorConfig(name, cfg); err != nil {
			s.logger.Error("failed to save bulk-applied config", slog.String("supervisor", name), slog.Any("error", err))
			continue
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func (s *HttpServer) resetMuling(w http.ResponseWriter, r *http.Request) {
	characterName := r.URL.Query().Get("characterName")
	if characterName == "" {
		http.Error(w, "Character name is required", http.StatusBadRequest)
		return
	}

	cfg, found := config.GetCharacter(characterName)
	if !found {
		http.Error(w, "Character config not found", http.StatusNotFound)
		return
	}

	s.logger.Info("Resetting muling index for character", "character", characterName)
	cfg.MulingState.CurrentMuleIndex = 0

	err := config.SaveSupervisorConfig(characterName, cfg)
	if err != nil {
		http.Error(w, "Failed to save updated config", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *HttpServer) skillOptionsAPI(w http.ResponseWriter, r *http.Request) {
	build := r.URL.Query().Get("build")
	payload := struct {
		Options  []SkillOption       `json:"options"`
		Prereqs  map[string][]string `json:"prereqs"`
		Resolved string              `json:"resolvedBuild"`
	}{
		Options:  buildSkillOptionsForBuild(build),
		Prereqs:  buildSkillPrereqsForBuild(build),
		Resolved: build,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// openDroplogs opens the droplogs directory in Windows Explorer.
func (s *HttpServer) openDroplogs(w http.ResponseWriter, r *http.Request) {
	base := config.App.LogSaveDirectory
	if base == "" {
		base = "logs"
	}
	dir := filepath.Join(base, "droplogs")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create directory: %v", err), http.StatusInternalServerError)
		return
	}

	// Open folder using Windows Explorer
	cmd := exec.Command("explorer.exe", dir)
	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("failed to open folder: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "dir": dir})
}

// resetDroplogs removes droplog JSONL/HTML files from the droplogs directory.
func (s *HttpServer) resetDroplogs(w http.ResponseWriter, r *http.Request) {
	base := config.App.LogSaveDirectory
	if base == "" {
		base = "logs"
	}
	dir := filepath.Join(base, "droplogs")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create directory: %v", err), http.StatusInternalServerError)
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list directory: %v", err), http.StatusInternalServerError)
		return
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".html") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			removed++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "dir": dir, "removed": removed})
}

func (s *HttpServer) getIntFromForm(r *http.Request, param string, min int, max int, defaultValue int) int {
	result := defaultValue
	paramValue, err := strconv.Atoi(r.Form.Get(param))
	if err != nil {
		s.logger.Warn("Invalid form value, setting to default",
			slog.String("parameter", param),
			slog.String("error", err.Error()),
			slog.Int("default", 0))
	} else {
		result = int(math.Max(math.Min(float64(paramValue), float64(max)), float64(min)))
	}
	return result
}

func buildTZGroups() []TZGroup {
	groups := make(map[string][]area.ID)
	for id, info := range terrorzones.Zones() {
		groupName := info.Group
		if groupName == "" {
			groupName = id.Area().Name
		}
		groups[groupName] = append(groups[groupName], id)
	}

	var result []TZGroup
	for name, ids := range groups {
		zone := terrorzones.Zones()[ids[0]]

		result = append(result, TZGroup{
			Act:           zone.Act,
			Name:          name,
			PrimaryAreaID: int(ids[0]),
			Immunities:    zone.Immunities,
			BossPacks:     zone.BossPack,
			ExpTier:       string(zone.ExpTier),
			LootTier:      string(zone.LootTier),
		})
	}

	slices.SortStableFunc(result, func(a, b TZGroup) int {
		if a.Act != b.Act {
			return cmp.Compare(a.Act, b.Act)
		}
		return cmp.Compare(a.Name, b.Name)
	})

	return result
}

// Updater handlers

func (s *HttpServer) getVersion(w http.ResponseWriter, r *http.Request) {
	version, err := updater.GetCurrentVersionNoClone()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get version: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"commitHash": version.CommitHash,
		"commitDate": formatCommitDate(version.CommitDate),
		"commitMsg":  version.CommitMsg,
		"branch":     version.Branch,
	})
}

func (s *HttpServer) checkUpdates(w http.ResponseWriter, r *http.Request) {
	result, err := updater.CheckForUpdates()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("Failed to check for updates: %v", err),
		})
		return
	}

	commits := make([]map[string]string, 0)
	for _, c := range result.NewCommits {
		commits = append(commits, map[string]string{
			"hash":    c.Hash,
			"date":    c.Date.Format("2006-01-02 15:04:05"),
			"message": c.Message,
		})
	}

	aheadCommits := make([]map[string]string, 0)
	for _, c := range result.AheadCommits {
		aheadCommits = append(aheadCommits, map[string]string{
			"hash":    c.Hash,
			"date":    c.Date.Format("2006-01-02 15:04:05"),
			"message": c.Message,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hasUpdates":    result.HasUpdates,
		"commitsAhead":  result.CommitsAhead,
		"commitsBehind": result.CommitsBehind,
		"aheadCommits":  aheadCommits,
		"newCommits":    commits,
		"currentVersion": map[string]interface{}{
			"commitHash": result.CurrentVersion.CommitHash,
			"commitDate": formatCommitDate(result.CurrentVersion.CommitDate),
			"commitMsg":  result.CurrentVersion.CommitMsg,
			"branch":     result.CurrentVersion.Branch,
		},
	})
}

func (s *HttpServer) getCurrentCommits(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 50 {
			limit = l
		}
	}

	commits, err := updater.GetCurrentCommits(limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get current commits: %v", err), http.StatusInternalServerError)
		return
	}

	payload := make([]map[string]string, 0, len(commits))
	for _, c := range commits {
		payload = append(payload, map[string]string{
			"hash":    c.Hash,
			"date":    c.Date.Format("2006-01-02 15:04:05"),
			"message": c.Message,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

func (s *HttpServer) performUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if any bots are running
	runningCount := 0
	for _, supervisorName := range s.manager.AvailableSupervisors() {
		stats := s.manager.Status(supervisorName)
		// Consider bot running if in Starting, InGame, or Paused state
		if stats.SupervisorStatus == bot.Starting ||
			stats.SupervisorStatus == bot.InGame ||
			stats.SupervisorStatus == bot.Paused {
			runningCount++
		}
	}

	if runningCount > 0 {
		http.Error(w, fmt.Sprintf("Cannot update while %d bot(s) are running. Please stop all bots first.", runningCount), http.StatusConflict)
		return
	}

	if !s.updater.TryStartOperation("update") {
		http.Error(w, "Updater is already running another operation", http.StatusConflict)
		return
	}

	// Parse auto-restart flag
	autoRestart := r.URL.Query().Get("restart") == "true"
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))

	// Set log callback to broadcast via WebSocket
	s.updater.SetLogCallback(func(message string) {
		s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"updater_log","message":%q}`, message))
	})

	// Start update in background
	go func() {
		defer s.updater.EndOperation()
		var err error
		if mode == "build" {
			backupTag := "build"
			if source == "pr" {
				backupTag = "pr"
			}
			err = s.updater.ExecuteBuild(autoRestart, backupTag)
		} else {
			err = s.updater.ExecuteUpdate(autoRestart)
		}
		if err != nil {
			s.logger.Error("Update failed", slog.Any("error", err))
			s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"updater_error","error":%q}`, err.Error()))
		} else {
			s.wsServer.broadcast <- []byte(`{"type":"updater_complete"}`)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "update started",
	})
}

func (s *HttpServer) getUpdaterStatus(w http.ResponseWriter, r *http.Request) {
	status := s.updater.GetStatus()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *HttpServer) getBackups(w http.ResponseWriter, r *http.Request) {
	// Get backup versions (limit to 5 most recent)
	backups, err := updater.GetBackupVersions(5)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get backup versions: %v", err), http.StatusInternalServerError)
		return
	}

	// Get current executable info
	currentExe, _ := updater.GetCurrentExecutable()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"backups": backups,
		"current": currentExe,
	})
}

func (s *HttpServer) performRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if any bots are running
	runningCount := 0
	for _, supervisorName := range s.manager.AvailableSupervisors() {
		stats := s.manager.Status(supervisorName)
		if stats.SupervisorStatus == bot.Starting ||
			stats.SupervisorStatus == bot.InGame ||
			stats.SupervisorStatus == bot.Paused {
			runningCount++
		}
	}

	if runningCount > 0 {
		http.Error(w, fmt.Sprintf("Cannot rollback while %d bot(s) are running. Please stop all bots first.", runningCount), http.StatusConflict)
		return
	}

	// Get backup file path from request
	var request struct {
		BackupPath string `json:"backupPath"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if request.BackupPath == "" {
		http.Error(w, "backupPath is required", http.StatusBadRequest)
		return
	}

	// Confirm the file exists
	if _, err := os.Stat(request.BackupPath); os.IsNotExist(err) {
		http.Error(w, "Backup file not found", http.StatusNotFound)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		http.Error(w, "Failed to resolve install directory", http.StatusInternalServerError)
		return
	}
	absExe, err := filepath.Abs(exePath)
	if err != nil {
		http.Error(w, "Failed to resolve install directory", http.StatusInternalServerError)
		return
	}
	installDir := filepath.Dir(absExe)
	oldVersionsDir := filepath.Join(installDir, "old_versions")
	absOldVersions, err := filepath.Abs(oldVersionsDir)
	if err != nil {
		http.Error(w, "Failed to resolve backup directory", http.StatusInternalServerError)
		return
	}
	absBackup, err := filepath.Abs(request.BackupPath)
	if err != nil {
		http.Error(w, "Invalid backupPath", http.StatusBadRequest)
		return
	}
	base := strings.TrimRight(absOldVersions, string(os.PathSeparator)) + string(os.PathSeparator)
	if !strings.HasPrefix(strings.ToLower(absBackup), strings.ToLower(base)) {
		http.Error(w, "backupPath must be inside old_versions", http.StatusBadRequest)
		return
	}

	if !s.updater.TryStartOperation("rollback") {
		http.Error(w, "Updater is already running another operation", http.StatusConflict)
		return
	}

	// Set log callback to broadcast via WebSocket
	s.updater.SetLogCallback(func(message string) {
		s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"rollback_log","message":%q}`, message))
	})

	// Perform rollback in background
	go func() {
		defer s.updater.EndOperation()
		err := s.updater.RollbackToVersion(request.BackupPath)
		if err != nil {
			s.logger.Error("Rollback failed", slog.Any("error", err))
			s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"rollback_error","error":%q}`, err.Error()))
		}
		// Note: If successful, the application will restart, so no completion message is sent
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "rollback started",
	})
}

func (s *HttpServer) getUpstreamPRs(w http.ResponseWriter, r *http.Request) {
	// Get query parameters
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}

	limit := 30
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	prs, err := updater.GetUpstreamPRs(state, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch PRs: %v", err), http.StatusInternalServerError)
		return
	}

	applied, err := updater.LoadAppliedPRs()
	if err != nil {
		s.logger.Warn("Failed to load applied PRs", slog.Any("error", err))
	} else {
		for i := range prs {
			if info, ok := applied[prs[i].Number]; ok {
				prs[i].Applied = true
				prs[i].CanRevert = len(info.Commits) > 0
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(prs)
}

func (s *HttpServer) cherryPickPRs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if any bots are running
	runningCount := 0
	for _, supervisorName := range s.manager.AvailableSupervisors() {
		stats := s.manager.Status(supervisorName)
		if stats.SupervisorStatus == bot.Starting ||
			stats.SupervisorStatus == bot.InGame ||
			stats.SupervisorStatus == bot.Paused {
			runningCount++
		}
	}

	if runningCount > 0 {
		http.Error(w, fmt.Sprintf("Cannot cherry-pick while %d bot(s) are running. Please stop all bots first.", runningCount), http.StatusConflict)
		return
	}

	// Parse request body
	var request struct {
		PRNumbers []int `json:"prNumbers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(request.PRNumbers) == 0 {
		http.Error(w, "prNumbers is required", http.StatusBadRequest)
		return
	}

	if !s.updater.TryStartOperation("cherry-pick") {
		http.Error(w, "Updater is already running another operation", http.StatusConflict)
		return
	}

	// Set log callback to broadcast via WebSocket
	s.updater.SetLogCallback(func(message string) {
		s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"cherrypick_log","message":%q}`, message))
	})

	// Perform cherry-pick in background
	go func() {
		defer s.updater.EndOperation()
		results, err := s.updater.CherryPickMultiplePRs(request.PRNumbers, func(message string) {
			s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"cherrypick_log","message":%q}`, message))
		})

		if err != nil {
			s.logger.Error("Cherry-pick failed", slog.Any("error", err))
			s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"cherrypick_error","error":%q}`, err.Error()))
			return
		}

		// Send results
		resultsJSON, _ := json.Marshal(results)
		s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"cherrypick_complete","results":%s}`, resultsJSON))
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "cherry-pick started",
	})
}

func (s *HttpServer) revertPR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if any bots are running
	runningCount := 0
	for _, supervisorName := range s.manager.AvailableSupervisors() {
		stats := s.manager.Status(supervisorName)
		if stats.SupervisorStatus == bot.Starting ||
			stats.SupervisorStatus == bot.InGame ||
			stats.SupervisorStatus == bot.Paused {
			runningCount++
		}
	}

	if runningCount > 0 {
		http.Error(w, fmt.Sprintf("Cannot revert while %d bot(s) are running. Please stop all bots first.", runningCount), http.StatusConflict)
		return
	}

	var request struct {
		PRNumber int `json:"prNumber"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if request.PRNumber <= 0 {
		http.Error(w, "prNumber is required", http.StatusBadRequest)
		return
	}

	if !s.updater.TryStartOperation("revert") {
		http.Error(w, "Updater is already running another operation", http.StatusConflict)
		return
	}

	progressCallback := func(message string) {
		s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"revert_log","message":%q}`, message))
	}

	go func(prNumber int) {
		defer s.updater.EndOperation()
		result, err := s.updater.RevertPR(prNumber, progressCallback)
		if err != nil {
			s.logger.Error("Revert failed", slog.Any("error", err))
			s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"revert_error","error":%q}`, err.Error()))
			return
		}
		if result != nil && !result.Success {
			errMsg := result.Error
			if errMsg == "" {
				errMsg = fmt.Sprintf("Revert failed for PR #%d", prNumber)
			}
			s.logger.Error("Revert failed", slog.String("reason", errMsg))
			s.wsServer.broadcast <- []byte(fmt.Sprintf(`{"type":"revert_error","error":%q}`, errMsg))
			return
		}
		s.wsServer.broadcast <- []byte(`{"type":"revert_complete"}`)
	}(request.PRNumber)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "revert started",
	})
}

func (s *HttpServer) generateBattleNetToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Realm    string `json:"realm"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.logger.Error("Failed to decode request", slog.Any("error", err))
		http.Error(w, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate input
	if req.Username == "" || req.Password == "" {
		http.Error(w, "Username and password are required", http.StatusBadRequest)
		return
	}

	s.logger.Info("Generating Battle.net token",
		slog.String("username", req.Username),
		slog.String("realm", req.Realm))

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")

	sendLine := func(line string) {
		if line == "" {
			return
		}
		fmt.Fprintln(w, line)
		flusher.Flush()
	}

	token, err := game.GetBattleNetTokenWithDebugContext(r.Context(), req.Username, req.Password, req.Realm, sendLine)
	if err != nil {
		s.logger.Error("Failed to generate Battle.net token",
			slog.String("username", req.Username),
			slog.Any("error", err))
		sendLine("ERROR: " + err.Error())
		return
	}

	s.logger.Info("Battle.net token generated successfully",
		slog.String("username", req.Username))

	sendLine("TOKEN: " + token)
}

// debugSendPacket sends an arbitrary packet (hex bytes) to D2R via the running
// supervisor's PacketSender. Used for live experimentation.
//
// Usage:
//   GET /debug/sendpacket?character=Blizzard&hex=60
//   GET /debug/sendpacket?character=Blizzard&hex=0C0A001500
//
// Returns JSON with: ok, elapsed_ms, error (if any), bytes_sent, opcode.
func (s *HttpServer) debugSendPacket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugSendPacket PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	hexStr := r.URL.Query().Get("hex")

	if character == "" || hexStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character or hex parameter"}`)
		return
	}

	// Strip whitespace and 0x prefixes
	hexStr = strings.ReplaceAll(hexStr, " ", "")
	hexStr = strings.ReplaceAll(hexStr, "0x", "")

	pkt, err := hex.DecodeString(hexStr)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid hex: %s"}`, err.Error())
		return
	}

	if len(pkt) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"empty packet"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.PacketSender == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor or packet sender for character %s"}`, character)
		return
	}

	// path selection: ?path=game (default) | ui | dual
	// Legacy: ?ui=true aliases to path=ui.
	pathParam := strings.ToLower(r.URL.Query().Get("path"))
	if pathParam == "" {
		if r.URL.Query().Get("ui") == "true" || r.URL.Query().Get("ui") == "1" {
			pathParam = "ui"
		} else {
			pathParam = "game"
		}
	}

	s.logger.Info("DEBUG: sending raw packet",
		slog.String("character", character),
		slog.String("hex", hex.EncodeToString(pkt)),
		slog.Int("len", len(pkt)),
		slog.String("opcode", fmt.Sprintf("0x%02X", pkt[0])),
		slog.String("path", pathParam))

	start := time.Now()
	var sendErr error
	switch pathParam {
	case "ui":
		sendErr = ctx.PacketSender.SendUIPacket(pkt)
	case "dual":
		sendErr = ctx.PacketSender.SendDualPacket(pkt)
	case "dualwrap":
		sendErr = ctx.GameReader.Process.SendPacketViaDualWrap(pkt)
	case "dualwrap-gt":
		pres := ctx.MemoryInjector.GetPresenter()
		if pres == nil {
			sendErr = fmt.Errorf("no presenter (rmod.dll not injected)")
		} else {
			sendErr = pres.SendDualPacketGT(pkt)
		}
	case "game", "":
		sendErr = ctx.PacketSender.SendPacket(pkt)
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid path: %s (use game/ui/dual)"}`, pathParam)
		return
	}
	elapsed := time.Since(start)

	if sendErr != nil {
		s.logger.Warn("DEBUG: packet send failed",
			slog.String("error", sendErr.Error()),
			slog.Duration("elapsed", elapsed))
		fmt.Fprintf(w, `{"ok":false,"opcode":"0x%02X","bytes_sent":%d,"elapsed_ms":%d,"error":%q}`,
			pkt[0], len(pkt), elapsed.Milliseconds(), sendErr.Error())
		return
	}

	s.logger.Info("DEBUG: packet sent OK",
		slog.Duration("elapsed", elapsed))
	fmt.Fprintf(w, `{"ok":true,"opcode":"0x%02X","bytes_sent":%d,"elapsed_ms":%d}`,
		pkt[0], len(pkt), elapsed.Milliseconds())
}

// debugPressKey simulates pressing a keybinding via the bot's HID layer.
// Used to trigger in-game actions (weapon swap, show items, open panels) without
// requiring the user to physically press a key.
//
// Usage:
//   GET /debug/presskey?character=Blizzard&bind=SwapWeapons
//   GET /debug/presskey?character=Blizzard&bind=ShowItems
//   GET /debug/presskey?character=Blizzard&bind=ForceMove
func (s *HttpServer) debugPressKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	bind := r.URL.Query().Get("bind")
	if character == "" || bind == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character or bind param"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}

	// Map bind name → KeyBinding from ctx.Data.KeyBindings
	ctx.RefreshGameData()
	kb := ctx.Data.KeyBindings
	var binding data.KeyBinding
	var found bool
	switch bind {
	case "SwapWeapons":
		binding, found = kb.SwapWeapons, true
	case "ShowItems":
		binding, found = kb.ShowItems, true
	case "ForceMove":
		binding, found = kb.ForceMove, true
	case "ShowBelt":
		binding, found = kb.ShowBelt, true
	case "Inventory":
		binding, found = kb.Inventory, true
	case "CharacterScreen":
		binding, found = kb.CharacterScreen, true
	case "SkillTree":
		binding, found = kb.SkillTree, true
	case "QuestLog":
		binding, found = kb.QuestLog, true
	case "MercenaryScreen":
		binding, found = kb.MercenaryScreen, true
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"unknown bind: %s"}`, bind)
		return
	}
	if !found || binding.Key1[0] == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"bind %s has no key assigned"}`, bind)
		return
	}

	ctx.HID.PressKeyBinding(binding)
	fmt.Fprintf(w, `{"ok":true,"bind":%q,"key":"0x%X"}`, bind, binding.Key1[0])
}

// debugPressRawKey sends a raw virtual key code via HID.PressKey.
// Unlike /debug/presskey which resolves keybinding names, this takes a raw VK code.
// Usage: /debug/pressrawkey?vk=0x0D (Enter) /debug/pressrawkey?vk=0x1B (Escape)
func (s *HttpServer) debugPressRawKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	vkStr := r.URL.Query().Get("vk")
	if vkStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing vk param (hex VK code, e.g. 0x0D for Enter)"}`)
		return
	}
	vk, err := strconv.ParseUint(vkStr, 0, 32)
	if err != nil || vk == 0 || vk > 0xFF {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid vk: %s"}`, vkStr)
		return
	}

	// Find ANY running supervisor's HID, or the first available one
	for _, name := range s.manager.AvailableSupervisors() {
		ctx := s.manager.GetContext(name)
		if ctx != nil && ctx.HID != nil {
			ctx.HID.PressKey(byte(vk))
			fmt.Fprintf(w, `{"ok":true,"vk":"0x%X"}`, vk)
			return
		}
	}

	// No supervisor — use raw SendInput via windows API
	fmt.Fprintf(w, `{"error":"no supervisor with HID available"}`)
}

// debugWalkPacket: zero-HID movement via CursorPos + GetKeyState override.
// No SendInput, no PostMessage — pure memory write.
// Usage: /debug/walkpacket?character=Blizzard&x=400&y=300
func (s *HttpServer) debugWalkPacket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	xStr := r.URL.Query().Get("x")
	yStr := r.URL.Query().Get("y")
	if xStr == "" || yStr == "" {
		fmt.Fprintf(w, `{"error":"missing x or y"}`)
		return
	}
	x, _ := strconv.ParseInt(xStr, 10, 32)
	y, _ := strconv.ParseInt(yStr, 10, 32)

	c := s.manager.GetContext(character)
	if c == nil || c.HID == nil || c.MemoryInjector == nil {
		fmt.Fprintf(w, `{"error":"no context"}`)
		return
	}

	fmVK := c.Data.KeyBindings.ForceMove.Key1[0]
	if fmVK == 0 {
		fmVK = 0x45
	}

	screenX := c.GameReader.WindowLeftX + int(x)
	screenY := c.GameReader.WindowTopY + int(y)
	c.MemoryInjector.CursorPos(screenX, screenY)

	c.MemoryInjector.OverrideGetKeyState(fmVK)

	lParam := uintptr(int(x) | (int(y) << 16))
	win.SendMessage(c.GameReader.HWND, win.WM_LBUTTONDOWN, 1, lParam)
	time.Sleep(80 * time.Millisecond)
	win.SendMessage(c.GameReader.HWND, win.WM_LBUTTONUP, 0, lParam)
	time.Sleep(50 * time.Millisecond)

	c.MemoryInjector.RestoreGetKeyState()

	fmt.Fprintf(w, `{"ok":true,"x":%d,"y":%d,"vk":"0x%X","screen_x":%d,"screen_y":%d}`, x, y, fmVK, screenX, screenY)
}

// debugClickWorld converts game-world coordinates to screen coords using the
// bot's own ui.GameCoordsToScreenCords (which knows the live GameAreaSize and
// player position) and HID-clicks at the result. Saves the caller from having
// to mirror the isometric formula and guess at game-area dimensions.
func (s *HttpServer) debugClickWorld(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	xStr := r.URL.Query().Get("x")
	yStr := r.URL.Query().Get("y")
	btnStr := r.URL.Query().Get("btn")
	if character == "" || xStr == "" || yStr == "" {
		fmt.Fprintf(w, `{"error":"missing character/x/y"}`)
		return
	}
	wx, _ := strconv.ParseInt(xStr, 10, 32)
	wy, _ := strconv.ParseInt(yStr, 10, 32)
	c := s.manager.GetContext(character)
	if c == nil || c.HID == nil {
		fmt.Fprintf(w, `{"error":"no context"}`)
		return
	}
	// ui.GameCoordsToScreenCords reads ctx via context.Get() which is keyed by
	// goroutine ID. Attach this HTTP-handler goroutine so the lookup works.
	c.AttachRoutine(ctx.PriorityNormal)
	defer c.Detach()
	sx, sy := ui.GameCoordsToScreenCords(int(wx), int(wy))
	btn := game.LeftButton
	if btnStr == "right" || btnStr == "r" {
		btn = game.RightButton
	}
	c.HID.Click(btn, sx, sy)
	fmt.Fprintf(w, `{"ok":true,"world":{"x":%d,"y":%d},"screen":{"x":%d,"y":%d},"player":{"x":%d,"y":%d},"area_size":{"x":%d,"y":%d}}`,
		wx, wy, sx, sy, c.Data.PlayerUnit.Position.X, c.Data.PlayerUnit.Position.Y,
		c.GameReader.GameAreaSizeX, c.GameReader.GameAreaSizeY)
}

// debugMoveToCoords walks the player to the given world coordinates using the
// bot's own action.MoveToCoords (pathfinder + proper screen-coord conversion).
// Blocks until movement completes or times out. Usage:
//   /debug/movetocoords?character=Blizzard&x=4466&y=4629
func (s *HttpServer) debugMoveToCoords(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	xStr := r.URL.Query().Get("x")
	yStr := r.URL.Query().Get("y")
	if character == "" || xStr == "" || yStr == "" {
		fmt.Fprintf(w, `{"error":"missing character/x/y"}`)
		return
	}
	wx, _ := strconv.ParseInt(xStr, 10, 32)
	wy, _ := strconv.ParseInt(yStr, 10, 32)
	c := s.manager.GetContext(character)
	if c == nil {
		fmt.Fprintf(w, `{"error":"no context"}`)
		return
	}
	c.AttachRoutine(ctx.PriorityNormal)
	defer c.Detach()
	c.RefreshGameData()
	err := action.MoveToCoords(data.Position{X: int(wx), Y: int(wy)})
	c.RefreshGameData()
	p := c.Data.PlayerUnit.Position
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q,"player":{"x":%d,"y":%d}}`, err.Error(), p.X, p.Y)
		return
	}
	fmt.Fprintf(w, `{"ok":true,"player":{"x":%d,"y":%d}}`, p.X, p.Y)
}

// debugTestStashPacket runs the stash test:
//   - HID walk to bank (legacy/iso click via ForceMove key)
//   - Packet 0x41 to open stash
//   - Packet 0x54 to move Jewel inv → stash
//   - Packet 0x54 to move Jewel stash → inv
//   - Inventory verification at each step
//
// HID is used for movement only (per user request — packet 0x03 walk works
// but pathfinding setup is brittle in Claude mode). All item operations are
// pure packet.
//
// Usage: /debug/test-stash-packet?character=Blizzard
func (s *HttpServer) debugTestStashPacket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	if character == "" {
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}
	c := s.manager.GetContext(character)
	if c == nil {
		fmt.Fprintf(w, `{"error":"no context"}`)
		return
	}
	if c.PacketSender == nil {
		fmt.Fprintf(w, `{"error":"no packet sender"}`)
		return
	}
	c.AttachRoutine(ctx.PriorityNormal)
	defer c.Detach()
	c.RefreshGameData()

	results := []string{}
	add := func(s string) { results = append(results, s) }

	add(fmt.Sprintf(`"step0_pos":{"x":%d,"y":%d}`,
		c.Data.PlayerUnit.Position.X, c.Data.PlayerUnit.Position.Y))

	// 1. HID walk to bank (4466, 4629) via iterative iso clicks with ForceMove.
	// Each step: compute screen coord from current pos, click + ForceMove,
	// refresh, check distance. Bank is ~7 tiles NW of spawn.
	bank := data.Position{X: 4466, Y: 4629}
	fmVK := byte(0x45) // E (default ForceMove)
	if c.Data.KeyBindings.ForceMove.Key1[0] != 0 {
		fmVK = c.Data.KeyBindings.ForceMove.Key1[0]
	}
	walkSteps := 0
	for walkSteps < 8 {
		c.RefreshGameData()
		px, py := c.Data.PlayerUnit.Position.X, c.Data.PlayerUnit.Position.Y
		dx, dy := bank.X-px, bank.Y-py
		dist := dx*dx + dy*dy
		if dist <= 4 { // within 2 tiles → close enough for 0x41
			break
		}
		// Use bot's PROVEN movement primitive: MovePointer (move cursor to
		// target screen coord) + PressKeyBinding(ForceMove). This is what
		// pather.MoveCharacter falls back to when packet ForceClick is
		// unavailable (see pather/utils.go line 714-715). Pressing the
		// ForceMove key (E by default) with cursor at target triggers a
		// single walk step in that direction. NO left click needed.
		sx, sy := ui.GameCoordsToScreenCords(bank.X, bank.Y)
		// Clamp to game area (bot's formula sometimes produces off-screen
		// coords when target is far away, but D2R clamps internally).
		if sx < 50 {
			sx = 50
		}
		if sx > c.GameReader.GameAreaSizeX-50 {
			sx = c.GameReader.GameAreaSizeX - 50
		}
		if sy < 50 {
			sy = 50
		}
		if sy > c.GameReader.GameAreaSizeY-50 {
			sy = c.GameReader.GameAreaSizeY - 50
		}
		// Use bot's PRIMARY movement primitive: PacketSender.ForceClick.
		// This is what pather.MoveCharacter calls FIRST (utils.go line 709).
		// It hooks into the in-process Phase 8C cursor trampoline + posts
		// a ForceMove key. Falls back to HID MovePointer + PressKeyBinding
		// if packet path fails.
		_ = fmVK
		if err := c.PacketSender.ForceClick(int32(sx), int32(sy)); err != nil {
			c.HID.MovePointer(sx, sy)
			c.HID.PressKeyBinding(c.Data.KeyBindings.ForceMove)
		}
		time.Sleep(400 * time.Millisecond)
		walkSteps++
	}
	c.RefreshGameData()
	add(fmt.Sprintf(`"step1_walk_steps":%d,"step1_pos":{"x":%d,"y":%d}`,
		walkSteps, c.Data.PlayerUnit.Position.X, c.Data.PlayerUnit.Position.Y))

	// 2. Open stash via packet 0x41 (Action=0, bank object GID=0x11).
	openPkt := []byte{0x41, 0x11, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF}
	if err := c.PacketSender.SendPacket(openPkt); err != nil {
		add(fmt.Sprintf(`"step2_open_err":%q`, err.Error()))
	}
	time.Sleep(800 * time.Millisecond)
	c.RefreshGameData()
	add(fmt.Sprintf(`"step2_stash_open":%t`, c.Data.OpenMenus.Stash))

	// 3. Find Jewel in inventory
	var jewel *data.Item
	for i, it := range c.Data.Inventory.AllItems {
		if it.Name == "Jewel" && string(it.Location.LocationType) == "inventory" {
			jewel = &c.Data.Inventory.AllItems[i]
			break
		}
	}
	if jewel == nil {
		add(`"step3_jewel":"NOT_FOUND"`)
		fmt.Fprintf(w, "{%s}", strings.Join(results, ","))
		return
	}
	jewelGID := uint32(jewel.UnitID)
	srcCol := uint8(jewel.Position.X)
	srcRow := uint8(jewel.Position.Y)
	add(fmt.Sprintf(`"step3_jewel":{"gid":"0x%X","src_col":%d,"src_row":%d}`,
		jewelGID, srcCol, srcRow))

	// 4. Send 0x19 (OpItemMoveFrom = inv → stash) via SendDualPacket.
	// Live-captured format, 21 bytes (sec_stash.log 2026-04-07).
	if err := c.PacketSender.ItemToStash(jewel.UnitID, 6, 9); err != nil {
		add(fmt.Sprintf(`"step4_inv2stash_send_err":%q`, err.Error()))
	} else {
		add(`"step4_inv2stash_sent":true`)
	}
	_ = srcCol
	_ = srcRow
	_ = jewelGID
	time.Sleep(2 * time.Second)
	c.RefreshGameData()

	// 5. Verify: is Jewel now in stash?
	jewelLoc := "MISSING"
	jewelX, jewelY := 0, 0
	for _, it := range c.Data.Inventory.AllItems {
		if it.Name == "Jewel" {
			jewelLoc = string(it.Location.LocationType)
			jewelX = it.Position.X
			jewelY = it.Position.Y
			break
		}
	}
	add(fmt.Sprintf(`"step5_jewel":{"loc":%q,"x":%d,"y":%d}`, jewelLoc, jewelX, jewelY))

	// 6. Reverse: stash → inv (only if jewel is in stash)
	if jewelLoc == "stash" {
		if err := c.PacketSender.ItemFromStash(jewel.UnitID, uint8(jewel.Position.X), uint8(jewel.Position.Y)); err != nil {
			add(fmt.Sprintf(`"step6_stash2inv_send_err":%q`, err.Error()))
		} else {
			add(`"step6_stash2inv_sent":true`)
		}
		time.Sleep(2 * time.Second)
		c.RefreshGameData()
		jewelLocFinal := "MISSING"
		fx, fy := 0, 0
		for _, it := range c.Data.Inventory.AllItems {
			if it.Name == "Jewel" {
				jewelLocFinal = string(it.Location.LocationType)
				fx = it.Position.X
				fy = it.Position.Y
				break
			}
		}
		add(fmt.Sprintf(`"step7_jewel":{"loc":%q,"x":%d,"y":%d}`, jewelLocFinal, fx, fy))
	}

	fmt.Fprintf(w, "{%s}", strings.Join(results, ","))
}

// debugClickItem finds an inventory/stash item by GID and HID-clicks at its
// screen position, optionally with a modifier (ctrl=move-to-stash/inv, shift=
// stack split). Bot owns the screen-coord math so the caller doesn't have to
// guess GameAreaSize / WindowOffset.
func (s *HttpServer) debugClickItem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	gidStr := r.URL.Query().Get("itemGID")
	modifier := r.URL.Query().Get("modifier")
	btnStr := r.URL.Query().Get("btn")
	if character == "" || gidStr == "" {
		fmt.Fprintf(w, `{"error":"missing character or itemGID"}`)
		return
	}
	gid, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(gidStr, "0x"), "0X"), 16, 64)
	if err != nil {
		// Try decimal
		gid, err = strconv.ParseUint(gidStr, 10, 64)
		if err != nil {
			fmt.Fprintf(w, `{"error":"bad itemGID"}`)
			return
		}
	}
	c := s.manager.GetContext(character)
	if c == nil || c.HID == nil {
		fmt.Fprintf(w, `{"error":"no context"}`)
		return
	}
	c.AttachRoutine(ctx.PriorityNormal)
	defer c.Detach()
	c.RefreshGameData()
	var found *data.Item
	for _, it := range c.Data.Inventory.AllItems {
		if uint64(it.UnitID) == gid {
			found = &it
			break
		}
	}
	if found == nil {
		fmt.Fprintf(w, `{"error":"item gid 0x%X not in inventory","searched":%d}`, gid, len(c.Data.Inventory.AllItems))
		return
	}
	pos := ui.GetScreenCoordsForItem(*found)
	btn := game.LeftButton
	if btnStr == "right" || btnStr == "r" {
		btn = game.RightButton
	}
	switch modifier {
	case "ctrl", "control":
		c.HID.ClickWithModifier(btn, pos.X, pos.Y, game.CtrlKey)
	case "shift":
		c.HID.ClickWithModifier(btn, pos.X, pos.Y, game.ShiftKey)
	default:
		c.HID.Click(btn, pos.X, pos.Y)
	}
	fmt.Fprintf(w, `{"ok":true,"item":{"name":%q,"gid":"0x%X","loc":%q,"grid":{"x":%d,"y":%d}},"screen":{"x":%d,"y":%d},"modifier":%q,"btn":%q}`,
		found.Name, gid, found.Location.LocationType, found.Position.X, found.Position.Y,
		pos.X, pos.Y, modifier, btnStr)
}

func (s *HttpServer) debugHIDClick(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	xStr := r.URL.Query().Get("x")
	yStr := r.URL.Query().Get("y")
	btnStr := r.URL.Query().Get("btn")
	if xStr == "" || yStr == "" {
		fmt.Fprintf(w, `{"error":"missing x or y"}`)
		return
	}
	x, _ := strconv.ParseInt(xStr, 10, 32)
	y, _ := strconv.ParseInt(yStr, 10, 32)

	var c *ctx.Context
	if character != "" {
		c = s.manager.GetContext(character)
	} else {
		for _, name := range s.manager.AvailableSupervisors() {
			c = s.manager.GetContext(name)
			if c != nil {
				break
			}
		}
	}
	if c == nil || c.HID == nil {
		fmt.Fprintf(w, `{"error":"no HID"}`)
		return
	}
	btn := game.LeftButton
	if btnStr == "right" || btnStr == "r" {
		btn = game.RightButton
	}
	c.HID.Click(btn, int(x), int(y))
	fmt.Fprintf(w, `{"ok":true,"x":%d,"y":%d}`, x, y)
}

func (s *HttpServer) debugSendUIPacketAPC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	hexStr := strings.ReplaceAll(strings.ReplaceAll(r.URL.Query().Get("hex"), " ", ""), "0x", "")
	if character == "" || hexStr == "" {
		fmt.Fprintf(w, `{"error":"missing character or hex"}`)
		return
	}
	pkt, err := hex.DecodeString(hexStr)
	if err != nil {
		fmt.Fprintf(w, `{"error":"bad hex"}`)
		return
	}
	c := s.manager.GetContext(character)
	if c == nil || c.GameReader == nil {
		fmt.Fprintf(w, `{"error":"no context"}`)
		return
	}
	const uiNetManRVA uintptr = 0x19ED860
	uiGlobal := c.GameReader.Process.ModuleBaseAddress() + uiNetManRVA
	start := time.Now()
	sendErr := c.GameReader.Process.SendUIPacketViaMainThread(pkt, uiGlobal)
	elapsed := time.Since(start)
	if sendErr != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q,"elapsed_ms":%d}`, sendErr.Error(), elapsed.Milliseconds())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"opcode":"0x%02X","elapsed_ms":%d}`, pkt[0], elapsed.Milliseconds())
}

func (s *HttpServer) debugSetGameTID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	tidStr := r.URL.Query().Get("tid")
	if tidStr == "" {
		fmt.Fprintf(w, `{"error":"missing tid"}`)
		return
	}
	tid, _ := strconv.ParseUint(tidStr, 0, 32)
	c := s.manager.GetContext(character)
	if c == nil || c.MemoryInjector == nil {
		fmt.Fprintf(w, `{"error":"no context"}`)
		return
	}
	pres := c.MemoryInjector.GetPresenter()
	if pres == nil {
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	pres.SetGameThreadID(uint32(tid))
	fmt.Fprintf(w, `{"ok":true,"tid":%d}`, tid)
}

// debugClick fires an in-process click via the Phase 9 path
// (rmod.dll CMD_CLICK → real_click_worker). Used to smoke-test the
// wndproc-bypass walk strategy.
//
// Usage: GET /debug/click?character=Blizzard&x=400&y=300
//        GET /debug/click?character=Blizzard&x=400&y=300&btn=right
//
// btn defaults to "left". The call lands at the same vtable[1] dispatch a
// real wndproc click would have hit — for a left click on a walkable tile
// this means the character walks toward (x, y) in client pixels.
func (s *HttpServer) debugClick(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugClick PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	xStr := r.URL.Query().Get("x")
	yStr := r.URL.Query().Get("y")
	btnStr := r.URL.Query().Get("btn")
	if character == "" || xStr == "" || yStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character, x, or y parameter"}`)
		return
	}

	x, err := strconv.ParseInt(xStr, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid x: %s"}`, err.Error())
		return
	}
	y, err := strconv.ParseInt(yStr, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid y: %s"}`, err.Error())
		return
	}

	var btn byte = 1 // left
	switch btnStr {
	case "", "left", "l", "1":
		btn = 1
	case "right", "r", "4":
		btn = 4
	case "middle", "m", "2":
		btn = 2
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid btn: %s (use left/right/middle)"}`, btnStr)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.PacketSender == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor or packet sender for character %s"}`, character)
		return
	}

	s.logger.Info("DEBUG: in-process click",
		slog.String("character", character),
		slog.Int64("x", x), slog.Int64("y", y),
		slog.String("btn", btnStr))

	start := time.Now()
	clickErr := ctx.PacketSender.ClickAt(int32(x), int32(y), btn)
	elapsed := time.Since(start)

	if clickErr != nil {
		s.logger.Warn("DEBUG: click failed",
			slog.String("error", clickErr.Error()),
			slog.Duration("elapsed", elapsed))
		fmt.Fprintf(w, `{"ok":false,"x":%d,"y":%d,"btn":%q,"elapsed_ms":%d,"error":%q}`,
			x, y, btnStr, elapsed.Milliseconds(), clickErr.Error())
		return
	}

	s.logger.Info("DEBUG: click sent OK", slog.Duration("elapsed", elapsed))
	fmt.Fprintf(w, `{"ok":true,"x":%d,"y":%d,"btn":%q,"elapsed_ms":%d}`,
		x, y, btnStr, elapsed.Milliseconds())
}

// claudeAttach attaches the bot to a running D2R process in Claude mode.
// Bot initializes all subsystems but runs no workflow — it sits idle waiting
func (s *HttpServer) debugUnlockCursor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	if err := ctx.MemoryInjector.DisableCursorOverride(); err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"message":"cursor unlocked"}`)
}

// for HTTP-driven packet experiments.
//
// Usage: GET /claude-attach?character=Blizzard&pid=1234
// HWND is auto-resolved from PID.
// shutdown gracefully tears down rmod's Present detour + VEHs before
// terminating app.exe. Replaces `taskkill /F /IM app.exe` in the auto_claude
// two-phase flow — without this, Present stays hooked into soon-to-be-freed
// SHM → D2R AVs next frame → Arxan VEH cascade → zombie that requires
// VM reboot.
//
// Query: none required. Iterates every active supervisor context, requests
// UninstallDetour on each's presenter, then os.Exit(0).
func (s *HttpServer) shutdown(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Collect every presenter we can find across supervisors.
	var results []string
	for _, name := range s.manager.AvailableSupervisors() {
		ctx := s.manager.GetContext(name)
		if ctx == nil || ctx.MemoryInjector == nil {
			continue
		}
		pres := ctx.MemoryInjector.GetPresenter()
		if pres == nil {
			continue
		}
		if err := pres.UninstallDetour(); err != nil {
			results = append(results, fmt.Sprintf("%s: %v", name, err))
		} else {
			results = append(results, fmt.Sprintf("%s: ok", name))
		}
	}

	fmt.Fprintf(w, `{"ok":true,"detour_uninstall":%q,"message":"exiting in 500 ms"}`, strings.Join(results, "; "))

	// Give the HTTP response time to flush, then exit cleanly. os.Exit bypasses
	// Go panics and deferred cleanup — but by this point rmod has already
	// restored Present, so D2R is safe to outlive us.
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

func (s *HttpServer) claudeAttach(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	character := r.URL.Query().Get("character")
	pidStr := r.URL.Query().Get("pid")
	if character == "" || pidStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character or pid parameter"}`)
		return
	}

	pid, err := strconv.ParseUint(pidStr, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid pid: %s"}`, err.Error())
		return
	}

	// Resolve HWND from PID
	var hwnd win.HWND
	enumCb := func(h win.HWND, _ uintptr) uintptr {
		var processID uint32
		win.GetWindowThreadProcessId(h, &processID)
		if processID == uint32(pid) {
			hwnd = h
			return 0
		}
		return 1
	}
	windows.EnumWindows(syscall.NewCallback(enumCb), nil)
	if hwnd == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"could not find HWND for pid %d"}`, pid)
		return
	}

	s.logger.Info("Claude mode attach requested",
		slog.String("character", character),
		slog.Uint64("pid", pid))

	go func() {
		if err := s.manager.StartClaude(character, uint32(pid), uint32(hwnd)); err != nil {
			s.logger.Error("Claude mode start failed", slog.String("error", err.Error()))
		}
	}()

	fmt.Fprintf(w, `{"ok":true,"character":%q,"pid":%d,"message":"Claude mode starting — wait ~5s for presenter init, then use /debug/sendpacket"}`, character, pid)
}

// debugGameState dumps the current game state for the running supervisor.
// Used to craft packets that depend on player position, area, etc.
func (s *HttpServer) debugGameState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugGameState PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor for character %s"}`, character)
		return
	}

	// Refresh game data before reading — Claude mode does not auto-refresh.
	// Wrap in recover via the outer defer.
	ctx.RefreshGameData()

	d := ctx.Data
	playerUnitAddr := d.PlayerUnit.Address
	var pathAddr uintptr
	if playerUnitAddr != 0 && ctx.GameReader != nil {
		pathAddr = uintptr(ctx.GameReader.Process.ReadUInt(playerUnitAddr+0x38, memory.Uint64))
	}

	// For PlayerUnit, stat Values are stored as raw integers (not the 24.8
	// fixed-point format used by monster/merc stats). So no shift needed.
	// HPPercent() in data.go does simple life/maxLife division which only
	// works if both sides are in the same unit — raw ÷ raw = ratio.
	lifeStat, _ := d.PlayerUnit.FindStat(stat.Life, 0)
	maxLifeStat, _ := d.PlayerUnit.FindStat(stat.MaxLife, 0)
	manaStat, _ := d.PlayerUnit.FindStat(stat.Mana, 0)
	maxManaStat, _ := d.PlayerUnit.FindStat(stat.MaxMana, 0)
	hpCur := lifeStat.Value
	hpMax := maxLifeStat.Value
	mpCur := manaStat.Value
	mpMax := maxManaStat.Value

	resp := map[string]any{
		"player_pos":   map[string]int{"x": d.PlayerUnit.Position.X, "y": d.PlayerUnit.Position.Y},
		"player_gid":   int(d.PlayerUnit.ID),
		"area":         int(d.PlayerUnit.Area),
		"area_name":    d.PlayerUnit.Area.Area().Name,
		"area_origin":  map[string]int{"x": d.AreaOrigin.X, "y": d.AreaOrigin.Y},
		"world_pos":    map[string]int{"x": d.PlayerUnit.Position.X + d.AreaOrigin.X, "y": d.PlayerUnit.Position.Y + d.AreaOrigin.Y},
		"hp_cur":       hpCur,
		"hp_max":       hpMax,
		"hp_percent":   d.PlayerUnit.HPPercent(),
		"mp_cur":       mpCur,
		"mp_max":       mpMax,
		"mp_percent":   d.PlayerUnit.MPPercent(),
		"weapon_slot":  d.ActiveWeaponSlot,
		"in_town":      d.PlayerUnit.Area.IsTown(),
		"can_teleport": d.CanTeleport(),
		"monsters_nearby": len(d.Monsters),
		"objects_nearby":  len(d.Objects),
		"npcs_nearby":     len(d.NPCs),
		"right_skill":    int(d.PlayerUnit.RightSkill),
		"left_skill":     int(d.PlayerUnit.LeftSkill),
		"player_unit_addr": fmt.Sprintf("0x%X", playerUnitAddr),
		"path_addr":        fmt.Sprintf("0x%X", pathAddr),
	}
	json.NewEncoder(w).Encode(resp)
}

// debugNPCs lists nearby NPCs (monsters with type none = town NPCs / interactable units)
// with their UnitID — needed to craft 0x13 NPC interact packets.
func (s *HttpServer) debugNPCs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugNPCs PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor for character %s"}`, character)
		return
	}

	ctx.RefreshGameData()

	type npcInfo struct {
		Name     string `json:"name"`
		UnitID   int    `json:"unit_id"`
		NpcID    int    `json:"npc_id"`
		Position struct {
			X int `json:"x"`
			Y int `json:"y"`
		} `json:"position"`
		Distance int    `json:"distance"`
		Type     string `json:"type"`
	}

	playerPos := ctx.Data.PlayerUnit.Position
	out := []npcInfo{}
	for _, m := range ctx.Data.Monsters {
		dx := m.Position.X - playerPos.X
		dy := m.Position.Y - playerPos.Y
		dist := dx*dx + dy*dy
		info := npcInfo{
			Name:   string(m.Name),
			UnitID: int(m.UnitID),
			NpcID:  int(m.Name),
		}
		info.Position.X = m.Position.X
		info.Position.Y = m.Position.Y
		info.Distance = dist
		info.Type = string(m.Type)
		out = append(out, info)
	}

	json.NewEncoder(w).Encode(map[string]any{
		"player_pos": map[string]int{"x": playerPos.X, "y": playerPos.Y},
		"count":      len(out),
		"npcs":       out,
	})
}

// debugInventory dumps every item the player currently sees (inventory, stash,
// cube, equipped, vendor, ground) with the GID and identification status.
// Crafted to support packet experiments — any builder that needs an item GID
// can be fed from here.
//
// Usage: GET /debug/inventory?character=Blizzard
func (s *HttpServer) debugInventory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugInventory PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor for character %s"}`, character)
		return
	}

	ctx.RefreshGameData()

	type itemInfo struct {
		Name           string `json:"name"`
		IdentifiedName string `json:"identified_name,omitempty"`
		UnitID         int    `json:"gid"`
		HexGID         string `json:"hex_gid"`
		Quality        string `json:"quality"`
		Location       string `json:"location"`
		PosX           int    `json:"x"`
		PosY           int    `json:"y"`
		Identified     bool   `json:"identified"`
		Ethereal       bool   `json:"ethereal"`
		Stack          int    `json:"stack,omitempty"`
	}

	out := []itemInfo{}
	for _, itm := range ctx.Data.Inventory.AllItems {
		out = append(out, itemInfo{
			Name:           string(itm.Name),
			IdentifiedName: itm.IdentifiedName,
			UnitID:         int(itm.UnitID),
			HexGID:         fmt.Sprintf("0x%X", uint32(itm.UnitID)),
			Quality:        itm.Quality.ToString(),
			Location:       string(itm.Location.LocationType),
			PosX:           itm.Position.X,
			PosY:           itm.Position.Y,
			Identified:     itm.Identified,
			Ethereal:       itm.Ethereal,
			Stack:          itm.StackedQuantity,
		})
	}

	json.NewEncoder(w).Encode(map[string]any{
		"count": len(out),
		"items": out,
	})
}

// debugPanels dumps the panel tree ReadAllPanels sees. Used to diagnose why
// IsInCharacterSelectionScreen returns false — if mod tiny renames the root
// panel, the CharacterSelectPanel lookup misses.
//
// Usage: GET /debug/panels?character=Blizzard
func (s *HttpServer) debugPanels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugPanels PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor for character %s"}`, character)
		return
	}

	panels := ctx.GameReader.ReadAllPanels()

	type panelInfo struct {
		Name        string `json:"name"`
		Parent      string `json:"parent"`
		Depth       int    `json:"depth"`
		Enabled     bool   `json:"enabled"`
		Visible     bool   `json:"visible"`
		NumChildren int    `json:"num_children"`
		Extra       string `json:"extra,omitempty"`
	}

	var out []panelInfo
	var walk func(p data.Panel)
	walk = func(p data.Panel) {
		out = append(out, panelInfo{
			Name:        p.PanelName,
			Parent:      p.PanelParent,
			Depth:       p.Depth,
			Enabled:     p.PanelEnabled,
			Visible:     p.PanelVisible,
			NumChildren: p.NumChildren,
			Extra:       p.ExtraText,
		})
		for _, c := range p.PanelChildren {
			walk(c)
		}
	}
	for _, p := range panels {
		walk(p)
	}

	json.NewEncoder(w).Encode(map[string]any{
		"count":  len(out),
		"panels": out,
	})
}

// debugScreenshot captures the current D2R window and writes it as PNG to a fixed
// path on disk. The response includes the file path so external tools (or Claude)
// can read the image directly.
//
// Usage: GET /debug/screenshot?character=Blizzard
//
// Output: build/logs/claude_screenshot.png + JSON {"path":...,"width":...,"height":...}
func (s *HttpServer) debugScreenshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugScreenshot PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor for character %s"}`, character)
		return
	}

	img := ctx.GameReader.Screenshot()
	if img == nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"screenshot returned nil (window not found?)"}`)
		return
	}

	outPath := filepath.Join("logs", "claude_screenshot.png")
	if err := os.MkdirAll("logs", 0755); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"mkdir logs: %s"}`, err.Error())
		return
	}
	f, err := os.Create(outPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"create file: %s"}`, err.Error())
		return
	}
	defer f.Close()

	if err := png.Encode(f, img); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"png encode: %s"}`, err.Error())
		return
	}

	bounds := img.Bounds()
	absPath, _ := filepath.Abs(outPath)
	fmt.Fprintf(w, `{"ok":true,"path":%q,"width":%d,"height":%d}`, absPath, bounds.Dx(), bounds.Dy())
}

// debugReadMem reads N bytes from D2R memory at the given offset (relative to
// module base unless &abs=1 is provided). Returns hex-encoded bytes.
//
// Usage:
//   GET /debug/readmem?character=Blizzard&offset=0x146600&len=32
//   GET /debug/readmem?character=Blizzard&addr=0x7FF712345678&len=64&abs=1
func (s *HttpServer) debugReadMem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugReadMem PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}

	lenStr := r.URL.Query().Get("len")
	length, err := strconv.ParseUint(lenStr, 0, 32)
	if err != nil || length == 0 || length > 4096 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid len (1..4096)"}`)
		return
	}

	abs := r.URL.Query().Get("abs") == "1"
	var addr uintptr
	if abs {
		addrStr := r.URL.Query().Get("addr")
		v, err := strconv.ParseUint(addrStr, 0, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid addr"}`)
			return
		}
		addr = uintptr(v)
	} else {
		offStr := r.URL.Query().Get("offset")
		v, err := strconv.ParseUint(offStr, 0, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid offset"}`)
			return
		}
		base := ctx.GameReader.Process.ModuleBaseAddress()
		addr = base + uintptr(v)
	}

	bytes := ctx.GameReader.Process.ReadBytesFromMemory(addr, uint(length))
	if bytes == nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"read failed"}`)
		return
	}

	fmt.Fprintf(w, `{"ok":true,"addr":"0x%X","len":%d,"hex":%q}`, addr, len(bytes), hex.EncodeToString(bytes))
}

// debugMemDiff snapshots a D2R memory region, waits for `duration_ms`, then
// snapshots again and returns the byte-level diff. Used to capture game-
// initiated packet writes: user issues the curl request, performs an action
// in D2R during the wait window, and the response shows exactly which bytes
// changed in the target buffer. Most useful on the vendor mirror buffer
// (offset 0x1F21330) and other known outgoing-packet staging areas.
//
// Usage:
//   GET /debug/memdiff?character=Blizzard&offset=0x1F21330&len=256&duration_ms=5000
//   GET /debug/memdiff?character=Blizzard&addr=0x7FF7624F1330&len=256&duration_ms=5000&abs=1
//
// Optional `samples=N` takes N intermediate snapshots spaced evenly across
// the window and returns the union of all bytes that ever changed. Default
// samples=2 (pre + post).
func (s *HttpServer) debugMemDiff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugMemDiff PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}

	lenStr := r.URL.Query().Get("len")
	length, err := strconv.ParseUint(lenStr, 0, 32)
	if err != nil || length == 0 || length > 65536 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid len (1..65536)"}`)
		return
	}

	durStr := r.URL.Query().Get("duration_ms")
	durMs, err := strconv.ParseUint(durStr, 0, 32)
	if err != nil || durMs == 0 || durMs > 60000 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid duration_ms (1..60000)"}`)
		return
	}

	samples := uint64(2)
	if s := r.URL.Query().Get("samples"); s != "" {
		v, err := strconv.ParseUint(s, 0, 32)
		if err == nil && v >= 2 && v <= 500 {
			samples = v
		}
	}

	abs := r.URL.Query().Get("abs") == "1"
	var addr uintptr
	if abs {
		addrStr := r.URL.Query().Get("addr")
		v, err := strconv.ParseUint(addrStr, 0, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid addr"}`)
			return
		}
		addr = uintptr(v)
	} else {
		offStr := r.URL.Query().Get("offset")
		v, err := strconv.ParseUint(offStr, 0, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid offset"}`)
			return
		}
		base := ctx.GameReader.Process.ModuleBaseAddress()
		addr = base + uintptr(v)
	}

	// Take N snapshots spaced evenly over duration_ms.
	gapMs := durMs / (samples - 1)
	type snap struct {
		TMs  uint64
		Data []byte
	}
	snaps := make([]snap, 0, samples)
	start := time.Now()
	for i := uint64(0); i < samples; i++ {
		b := ctx.GameReader.Process.ReadBytesViaKernel32(addr, uint(length))
		if b == nil {
			b = make([]byte, length)
		}
		snaps = append(snaps, snap{
			TMs:  uint64(time.Since(start).Milliseconds()),
			Data: b,
		})
		if i < samples-1 {
			time.Sleep(time.Duration(gapMs) * time.Millisecond)
		}
	}

	// Diff: for each byte position, collect every distinct value across the
	// snapshot sequence. A "change" entry records which snapshot the value
	// first differs from the initial value.
	type Change struct {
		Offset int      `json:"offset"`
		Values []string `json:"values"` // hex byte per snapshot
	}
	changes := make([]Change, 0)
	initial := snaps[0].Data
	for i := 0; i < int(length); i++ {
		// check if any later snapshot differs from initial
		changed := false
		for j := 1; j < len(snaps); j++ {
			if snaps[j].Data[i] != initial[i] {
				changed = true
				break
			}
		}
		if !changed {
			continue
		}
		seq := make([]string, len(snaps))
		for j, sn := range snaps {
			seq[j] = fmt.Sprintf("%02x", sn.Data[i])
		}
		changes = append(changes, Change{Offset: i, Values: seq})
	}

	// Also return first + last full snapshot for reference.
	type SnapJSON struct {
		TMs uint64 `json:"t_ms"`
		Hex string `json:"hex"`
	}
	snapJSON := make([]SnapJSON, len(snaps))
	for i, sn := range snaps {
		snapJSON[i] = SnapJSON{TMs: sn.TMs, Hex: hex.EncodeToString(sn.Data)}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"addr":        fmt.Sprintf("0x%X", addr),
		"len":         length,
		"duration_ms": durMs,
		"samples":     len(snaps),
		"changes":     changes,
		"snapshots":   snapJSON,
	})
}

// debugWriteMem writes hex-encoded bytes into D2R memory at the given offset
// (relative to module base unless &abs=1). Opens a transient handle with
// VM_WRITE permission. Debug/RE only — can crash D2R if used carelessly.
//
// Usage:
//   GET /debug/writemem?character=Blizzard&offset=0x146600&hex=4889d8
//   GET /debug/writemem?character=Blizzard&addr=0x7FF712345678&hex=DEADBEEF&abs=1
func (s *HttpServer) debugWriteMem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugWriteMem PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}

	hexStr := r.URL.Query().Get("hex")
	data, err := hex.DecodeString(hexStr)
	if err != nil || len(data) == 0 || len(data) > 4096 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid hex (1..4096 bytes)"}`)
		return
	}

	abs := r.URL.Query().Get("abs") == "1"
	var addr uintptr
	if abs {
		addrStr := r.URL.Query().Get("addr")
		v, perr := strconv.ParseUint(addrStr, 0, 64)
		if perr != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid addr"}`)
			return
		}
		addr = uintptr(v)
	} else {
		offStr := r.URL.Query().Get("offset")
		v, perr := strconv.ParseUint(offStr, 0, 64)
		if perr != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid offset"}`)
			return
		}
		base := ctx.GameReader.Process.ModuleBaseAddress()
		addr = base + uintptr(v)
	}

	if werr := ctx.GameReader.Process.WriteBytesToMemory(addr, data); werr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"write failed: %v"}`, werr)
		return
	}

	s.logger.Info("debug writemem",
		slog.String("character", character),
		slog.String("addr", fmt.Sprintf("0x%X", addr)),
		slog.Int("len", len(data)),
		slog.String("hex", hex.EncodeToString(data)),
	)

	fmt.Fprintf(w, `{"ok":true,"addr":"0x%X","len":%d}`, addr, len(data))
}

// debugDumpRange dumps a contiguous range of D2R memory to a file in
// build/dumps/. Used to grab large slices of .text for offline analysis.
//
// Usage:
//   GET /debug/dumprange?character=Blizzard&offset=0x5C000&size=0x800000&out=text.bin
func (s *HttpServer) debugDumpRange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugDumpRange PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}

	offStr := r.URL.Query().Get("offset")
	off, err := strconv.ParseUint(offStr, 0, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid offset"}`)
		return
	}
	sizeStr := r.URL.Query().Get("size")
	size, err := strconv.ParseUint(sizeStr, 0, 64)
	if err != nil || size == 0 || size > 0x4000000 { // up to 64 MB
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid size (1..64MB)"}`)
		return
	}
	out := r.URL.Query().Get("out")
	if out == "" || strings.ContainsAny(out, `<>:"/\|?*`) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid out filename"}`)
		return
	}

	base := ctx.GameReader.Process.ModuleBaseAddress()
	start := base + uintptr(off)

	// Read in 64 KB chunks (RPM is fine for sequential).
	const chunk = uint(0x10000)
	buf := make([]byte, 0, size)
	gaps := 0
	for read := uint64(0); read < size; read += uint64(chunk) {
		take := uint(chunk)
		if uint64(take) > size-read {
			take = uint(size - read)
		}
		b := ctx.GameReader.Process.ReadBytesViaKernel32(start+uintptr(read), take)
		if b == nil {
			gaps++
			b = make([]byte, take)
		}
		buf = append(buf, b...)
	}

	dumpDir := filepath.Join("build", "dumps")
	if err := os.MkdirAll(dumpDir, 0755); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"mkdir failed: %v"}`, err)
		return
	}
	outPath := filepath.Join(dumpDir, out)
	if err := os.WriteFile(outPath, buf, 0644); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"write failed: %v"}`, err)
		return
	}

	s.logger.Info("debug dumprange",
		slog.String("character", character),
		slog.String("start", fmt.Sprintf("0x%X", start)),
		slog.Uint64("size", size),
		slog.Int("gaps", gaps),
		slog.String("out", outPath),
	)

	fmt.Fprintf(w, `{"ok":true,"start":"0x%X","size":%d,"gaps":%d,"out":%q}`, start, size, gaps, outPath)
}

// debugScanMem scans D2R memory for a hex pattern. Pure RPM, no injection,
// no breakpoints — cannot crash the game. Returns up to max matches.
//
// Usage:
//   GET /debug/scanmem?character=Blizzard&pattern=05DEADBEEF&start=0x7FF79D3D0000&size=0x4000000&max=20
//   start defaults to D2R module base; size defaults to 0x4000000 (64 MB).
func (s *HttpServer) debugScanMem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugScanMem PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}

	patternHex := r.URL.Query().Get("pattern")
	pattern, err := hex.DecodeString(patternHex)
	if err != nil || len(pattern) < 2 || len(pattern) > 64 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid pattern (need 2..64 bytes hex)"}`)
		return
	}

	start := ctx.GameReader.Process.ModuleBaseAddress()
	if v := r.URL.Query().Get("start"); v != "" {
		if u, perr := strconv.ParseUint(v, 0, 64); perr == nil {
			start = uintptr(u)
		}
	}
	size := uint64(0x4000000) // 64 MB default
	if v := r.URL.Query().Get("size"); v != "" {
		if u, perr := strconv.ParseUint(v, 0, 64); perr == nil && u > 0 && u <= 0x40000000 {
			size = u
		}
	}
	maxMatches := 20
	if v := r.URL.Query().Get("max"); v != "" {
		if u, perr := strconv.ParseUint(v, 0, 32); perr == nil && u > 0 && u <= 1000 {
			maxMatches = int(u)
		}
	}

	// Scan in 64 KB chunks with 64 B overlap so a pattern straddling chunks
	// is still found. Stop on max matches OR end of range.
	const chunk = uint64(0x10000)
	overlap := uint64(len(pattern) - 1)
	matches := make([]string, 0, maxMatches)
	chunksScanned := 0
	chunksFailed := 0
	for off := uint64(0); off < size && len(matches) < maxMatches; off += chunk {
		readLen := chunk + overlap
		if off+readLen > size {
			readLen = size - off
		}
		bytes := ctx.GameReader.Process.ReadBytesFromMemory(start+uintptr(off), uint(readLen))
		if len(bytes) == 0 {
			chunksFailed++
			continue
		}
		// Detect zero-fill (failed read returns zero-filled buffer per ReadBytesFromMemory contract)
		allZero := true
		for i := 0; i < len(bytes) && i < 256; i++ {
			if bytes[i] != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			chunksFailed++
			continue
		}
		chunksScanned++
		// bytes.Index search
		searchOff := 0
		for searchOff < len(bytes) {
			idx := bytes_indexOf(bytes[searchOff:], pattern)
			if idx < 0 {
				break
			}
			absAddr := uint64(start) + off + uint64(searchOff+idx)
			matches = append(matches, fmt.Sprintf("0x%X", absAddr))
			if len(matches) >= maxMatches {
				break
			}
			searchOff += idx + 1
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":             true,
		"pattern":        patternHex,
		"start":          fmt.Sprintf("0x%X", start),
		"size":           fmt.Sprintf("0x%X", size),
		"chunks_scanned": chunksScanned,
		"chunks_failed":  chunksFailed,
		"matches":        matches,
	})
}

// bytes_indexOf — small wrapper avoiding bytes.Index import collision risk
func bytes_indexOf(haystack, needle []byte) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	first := needle[0]
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i] != first {
			continue
		}
		match := true
		for j := 1; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// debugSniffLog returns the most recent send_fn calls captured by the DLL's
// INT3+VEH sniff hook. Includes all packets — both bot-initiated and game-initiated
// (when the user clicks/swaps weapons/etc).
//
// Usage: GET /debug/snifflog?character=Blizzard
func (s *HttpServer) debugSniffLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("debugSniffLog PANIC", slog.Any("panic", rec))
			fmt.Fprintf(w, `{"error":"panic: %v"}`, rec)
		}
	}()

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}

	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"presenter not initialized"}`)
		return
	}

	entries, total := pres.ReadSniffLog()
	diag := pres.ReadSniffDiag()

	type EntryJSON struct {
		Size   uint32 `json:"size"`
		Opcode string `json:"opcode"`
		Hex    string `json:"hex"`
	}
	out := make([]EntryJSON, 0, len(entries))
	for _, e := range entries {
		op := "(empty)"
		if len(e.Data) > 0 {
			op = fmt.Sprintf("0x%02X", e.Data[0])
		}
		out = append(out, EntryJSON{
			Size:   e.Size,
			Opcode: op,
			Hex:    hex.EncodeToString(e.Data),
		})
	}

	// HWBP install/uninstall reuses the sniff diag slots before any BPs fire.
	// Decode the packed u32 layout from rmod write_hwbp_diag:
	//   bp_fired = total threads enumerated
	//   ss_fired = D2R-matching threads
	//   bp_ours  = last GetLastError captured
	//   last_rip = (last_step << 24) | ((count & 0xFF) << 16) | (extra << 8) | path
	// (was u64 — narrowed to u32 to stop stomping entry slot 0 size field at 0xC20)
	hwbpStep := (diag.LastBadRip >> 24) & 0xFF
	hwbpCount := (diag.LastBadRip >> 16) & 0xFF
	hwbpPath := diag.LastBadRip & 0xFF // 0=install, 1=uninstall, 2=verify
	stepName := map[uint32]string{
		0:    "ok",
		1:    "op1",
		2:    "op2",
		3:    "op3",
		4:    "op4",
		0xFD: "op253",
		0xFE: "op254",
	}[hwbpStep]
	if stepName == "" {
		stepName = fmt.Sprintf("unknown(0x%X)", hwbpStep)
	}
	pathName := map[uint32]string{0: "install", 1: "uninstall", 2: "verify"}[hwbpPath]

	json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"total":   total,
		"count":   len(out),
		"entries": out,
		"diag": map[string]any{
			"bp_fired":     diag.BpFired,
			"bp_ours":      diag.BpOurs,
			"ss_fired":     diag.SsFired,
			"install":      diag.Install,
			"uninstall":    diag.Uninstall,
			"last_bad_rip": fmt.Sprintf("0x%X", diag.LastBadRip),
		},
		"hwbp_diag": map[string]any{
			"total_threads_enum":  diag.BpFired,
			"d2r_threads_matched": diag.SsFired,
			"last_win32_error":    diag.BpOurs,
			"last_failed_step":    stepName,
			"successfully_set":    hwbpCount,
			"path":                pathName,
		},
		"hwbp_worker": map[string]any{
			"tick_count":     diag.HwbpTickCount,
			"alive":          diag.HwbpWorkerAlive,
			"new_armed":      diag.HwbpNewArmed,
			"last_tick_new":  diag.HwbpLastTickNew,
			"last_new_tid":   diag.HwbpLastNewTid,
		},
	})
}

func (s *HttpServer) debugSniffInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	if err := pres.SniffInstall(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"message":"sniff hook installed (INT3 at send_fn)"}`)
}

func (s *HttpServer) debugSniffUninstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	if err := pres.SniffUninstall(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"message":"sniff hook removed"}`)
}

func (s *HttpServer) debugHwbpInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	// Optional ?rva=0xNNNN (VA or RVA) — 0 = rmod uses G_DUAL_SEND_WRAP loaded at init.
	var target uint64
	if s := r.URL.Query().Get("rva"); s != "" {
		v, err := strconv.ParseUint(s, 0, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid rva: %s"}`, err.Error())
			return
		}
		target = v
		// If caller passed an RVA (< 0x10000000) add the D2R base.
		if target < 0x10000000 && ctx.GameReader != nil {
			base := uint64(ctx.GameReader.Process.GetModuleBase())
			if base != 0 {
				target += base
			}
		}
	}
	if err := pres.HwbpInstall(target); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	// Opt-in auto-reenum via ?reenum_ms=N. 0 or missing = disabled (manual
	// reenum only). Rationale: periodic SuspendThread/SetThreadContext on 45+
	// threads raced D2R's Arxan VM (~4000 non-fatal AV/s) and crashed D2R
	// within 15s during first live test.
	reenumMs := uint64(0)
	if s2 := r.URL.Query().Get("reenum_ms"); s2 != "" {
		if v, err := strconv.ParseUint(s2, 0, 64); err == nil {
			reenumMs = v
		}
	}
	if reenumMs > 0 {
		s.startHwbpReenumLoop(character, pres, time.Duration(reenumMs)*time.Millisecond)
	}
	st := pres.HwbpReadStatus()
	fmt.Fprintf(w, `{"ok":true,"target":"0x%X","install_ok":%d,"install_fail":%d,"auto_reenum_ms":%d}`,
		st.Target, st.InstallOk, st.InstallFail, reenumMs)
}

func (s *HttpServer) startHwbpReenumLoop(character string, pres *presenter.Presenter, interval time.Duration) {
	if prev, ok := s.hwbpReenumCancels.LoadAndDelete(character); ok {
		if cancel, ok := prev.(context.CancelFunc); ok {
			cancel()
		}
	}
	if interval <= 0 {
		return
	}
	c, cancel := context.WithCancel(context.Background())
	s.hwbpReenumCancels.Store(character, cancel)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-c.Done():
				return
			case <-t.C:
				_ = pres.HwbpReenum()
			}
		}
	}()
}

func (s *HttpServer) stopHwbpReenumLoop(character string) {
	if prev, ok := s.hwbpReenumCancels.LoadAndDelete(character); ok {
		if cancel, ok := prev.(context.CancelFunc); ok {
			cancel()
		}
	}
}

func (s *HttpServer) debugHwbpVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	if err := pres.HwbpVerify(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"message":"verify run — read /debug/snifflog for hwbp_diag (path=verify, successfully_set=still armed, last_win32_error=zeroed)"}`)
}

// debugDrProbe runs the DR0 persist diagnostic and returns a JSON breakdown.
// Determines whether SetThreadContext on D2R threads PERSISTS DR0 (HWBP path
// open) or whether Arxan reverts it (HWBP dead, need different bypass).
func (s *HttpServer) debugDrProbe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	res, err := pres.DrProbe()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}

	// Compose verdict.
	verdict := "unknown"
	if res.Total == 0 {
		verdict = "no_threads"
	} else if res.Ok > 0 && res.Revert == 0 && res.Err == 0 {
		verdict = "DR0_PERSISTS_HWBP_VIABLE"
	} else if res.Revert > 0 && res.Ok == 0 {
		verdict = "DR0_REVERTED_ARXAN_BLOCKS_HWBP"
	} else if res.Ok > 0 && res.Revert > 0 {
		verdict = "MIXED_some_threads_persist"
	} else if res.Err == res.Total {
		verdict = "all_probes_failed"
	} else {
		verdict = "partial"
	}

	fmt.Fprintf(w, `{"verdict":%q,"status":%d,"total":%d,"ok":%d,"revert":%d,"err":%d,"entries":[`,
		verdict, res.Status, res.Total, res.Ok, res.Revert, res.Err)
	for i, e := range res.Entries {
		if i > 0 {
			fmt.Fprintf(w, ",")
		}
		fmt.Fprintf(w, `{"tid":%d,"step_failed":%d,"last_err":%d,"dr7_orig":%d,"dr0_orig":%d,"dr0_after":%d,"persisted":%t}`,
			e.TID, e.StepFailed, e.LastErr, e.Dr7Orig, e.Dr0Orig, e.Dr0After, e.Persisted)
	}
	fmt.Fprintf(w, `]}`)
}

func (s *HttpServer) debugHwbpUninstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	s.stopHwbpReenumLoop(character)
	if err := pres.HwbpUninstall(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"message":"hardware breakpoint removed"}`)
}

func (s *HttpServer) debugHwbpReenum(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	if err := pres.HwbpReenum(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	st := pres.HwbpReadStatus()
	fmt.Fprintf(w, `{"ok":true,"reenum_new":%d,"reenum_total":%d}`, st.ReenumNew, st.ReenumTotal)
}

func (s *HttpServer) debugHwbpStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	st := pres.HwbpReadStatus()
	fmt.Fprintf(w,
		`{"installed":%d,"target":"0x%X","fires":%d,"ss_total":%d,"last_rip":"0x%X",`+
			`"install_ok":%d,"install_fail":%d,"verify_still":%d,"verify_lost":%d,`+
			`"reenum_new":%d,"reenum_total":%d,"ring_head":%d,"ring_tail":%d,`+
			`"ring_total":%d,"ring_dropped":%d,`+
			`"veh_any":%d,"veh_bp":%d,"veh_av":%d,"veh_other":%d,"veh_last_code":"0x%X",`+
			`"worker_prog":"0x%X","worker_tid":%d,"worker_seen":%d,"worker_ok":%d,"worker_fail":%d,`+
			`"gtc64_hook_count":%d,"gtc64_diag":"0x%X"}`,
		st.Installed, st.Target, st.Fires, st.SsTotal, st.LastRip,
		st.InstallOk, st.InstallFail, st.VerifyStill, st.VerifyLost,
		st.ReenumNew, st.ReenumTotal, st.RingHead, st.RingTail,
		st.RingTotal, st.RingDropped,
		st.VehAny, st.VehBp, st.VehAv, st.VehOther, st.VehLastCode,
		st.WorkerProg, st.WorkerTid, st.WorkerSeen, st.WorkerOk, st.WorkerFail,
		st.Gtc64Count, st.Gtc64Diag)
}

func (s *HttpServer) debugHwbpDrain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	entries := pres.HwbpDrain()
	st := pres.HwbpReadStatus()
	base := uint64(0)
	if ctx.GameReader != nil {
		base = uint64(ctx.GameReader.Process.GetModuleBase())
	}

	fmt.Fprintf(w, `{"count":%d,"ring_total":%d,"ring_dropped":%d,"fires":%d,"base":"0x%X","entries":[`,
		len(entries), st.RingTotal, st.RingDropped, st.Fires, base)
	for i, e := range entries {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		ripRVA := int64(0)
		if base != 0 && e.RIP >= base {
			ripRVA = int64(e.RIP - base)
		}
		fmt.Fprintf(w,
			`{"ts":%d,"tid":%d,"rip":"0x%X","rip_rva":"0x%X","rsp":"0x%X","rbp":"0x%X",`+
				`"rcx":"0x%X","rdx":%d,"r8":"0x%X","r9":"0x%X","callstack":[`,
			e.Ts, e.TID, e.RIP, ripRVA, e.RSP, e.RBP, e.RCX, e.RDX, e.R8, e.R9)
		for j, f := range e.Callstack {
			if j > 0 {
				fmt.Fprint(w, ",")
			}
			frva := int64(0)
			if base != 0 && f >= base && f < base+0x10000000 {
				frva = int64(f - base)
			}
			fmt.Fprintf(w, `{"va":"0x%X","rva":"0x%X"}`, f, frva)
		}
		fmt.Fprintf(w, `],"payload":"`)
		for _, b := range e.Payload {
			fmt.Fprintf(w, "%02X", b)
		}
		fmt.Fprintf(w, `"}`)
	}
	fmt.Fprintf(w, `]}`)
}

// debugCallFn calls an arbitrary function inside D2R via CMD_CALL_FN.
// Usage: /debug/callfn?character=Blizzard&addr=0x7FF760716600&a0=0&a1=0&a2=0&a3=0
func (s *HttpServer) debugCallFn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}

	addrStr := r.URL.Query().Get("addr")
	fnAddr, err := strconv.ParseUint(addrStr, 0, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid addr"}`)
		return
	}

	var args [4]uintptr
	for i := 0; i < 4; i++ {
		s := r.URL.Query().Get(fmt.Sprintf("a%d", i))
		if s != "" {
			v, _ := strconv.ParseUint(s, 0, 64)
			args[i] = uintptr(v)
		}
	}

	ret, err := ctx.GameReader.Process.CallFn(uintptr(fnAddr), args[0], args[1], args[2], args[3])
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"return_value":"0x%X","return_dec":%d}`, ret, ret)
}

// debugCallFnGT calls an arbitrary function inside D2R via CMD_CALL_FN_GT
// (game thread APC). Unlike /debug/callfn which runs on the render thread
// (deadlocks game-logic functions), this queues the call on the game thread.
// Usage: /debug/callfn-gt?character=Blizzard&addr=0x7FF7606D2220&a0=0x7FF7625213B0
func (s *HttpServer) debugCallFnGT(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}

	addrStr := r.URL.Query().Get("addr")
	fnAddr, err := strconv.ParseUint(addrStr, 0, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid addr"}`)
		return
	}

	var args [4]uintptr
	for i := 0; i < 4; i++ {
		s := r.URL.Query().Get(fmt.Sprintf("a%d", i))
		if s != "" {
			v, _ := strconv.ParseUint(s, 0, 64)
			args[i] = uintptr(v)
		}
	}

	ret, err := pres.CallFnGameThread(uintptr(fnAddr), args[0], args[1], args[2], args[3])
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"return_value":"0x%X","return_dec":%d}`, ret, ret)
}

// debugInprocWriteMem writes bytes to D2R memory via CMD_WRITE_MEM (in-process, no cross-process handle).
// Usage: /debug/inproc-writemem?character=Blizzard&addr=0x7FF760ABCDEF&hex=DEADBEEF
func (s *HttpServer) debugInprocWriteMem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}

	addrStr := r.URL.Query().Get("addr")
	addr, err := strconv.ParseUint(addrStr, 0, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid addr"}`)
		return
	}

	hexStr := r.URL.Query().Get("hex")
	data, err := hex.DecodeString(hexStr)
	if err != nil || len(data) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid hex"}`)
		return
	}

	if err := ctx.GameReader.Process.WriteMem(uintptr(addr), data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"addr":"0x%X","bytes_written":%d}`, addr, len(data))
}

// ---------------------------------------------------------------------------
// In-process packet capture (rmod_sniffer.dll ring buffer)
// ---------------------------------------------------------------------------

var activeSniffer *presenter.Sniffer

func (s *HttpServer) getOrOpenSniffer(character string) (*presenter.Sniffer, error) {
	if activeSniffer != nil {
		return activeSniffer, nil
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		return nil, fmt.Errorf("no supervisor for %s", character)
	}
	pid := ctx.GameReader.GetPID()
	// Try open existing, if fails create it ourselves
	sn, err := presenter.OpenSniffer(pid)
	if err != nil {
		sn, err = presenter.CreateSniffer(pid)
		if err != nil {
			return nil, fmt.Errorf("create sniffer: %w", err)
		}
		s.logger.Info("Created sniffer SHM from Go side", slog.Uint64("pid", uint64(pid)))
	}
	activeSniffer = sn
	return sn, nil
}

func (s *HttpServer) debugCaptureStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	sn, err := s.getOrOpenSniffer(character)
	if err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	sn.Enable()
	fmt.Fprintf(w, `{"ok":true,"message":"capture started"}`)
}

func (s *HttpServer) debugCaptureStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if activeSniffer != nil {
		activeSniffer.Disable()
	}
	fmt.Fprintf(w, `{"ok":true,"message":"capture stopped"}`)
}

func (s *HttpServer) debugCaptureStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	sn, err := s.getOrOpenSniffer(character)
	if err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	total, dropped, frame := sn.Stats()
	fmt.Fprintf(w, `{"total":%d,"dropped":%d,"frame":%d,"enabled":%v}`, total, dropped, frame, sn.IsEnabled())
}

func (s *HttpServer) debugCaptureDrain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	sn, err := s.getOrOpenSniffer(character)
	if err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	entries := sn.Drain()
	w.Write([]byte(`{"count":` + fmt.Sprintf("%d", len(entries)) + `,"entries":[`))
	for i, e := range entries {
		if i > 0 {
			w.Write([]byte(","))
		}
		line := presenter.FormatEntry(e)
		fmt.Fprintf(w, `{"buf":%d,"opcode":"0x%02X","len":%d,"tick":%d,"frame":%d,"hex":"%s","summary":%q}`,
			e.BufID, e.Opcode, e.DataLen, e.TickMs, e.FrameNo,
			hex.EncodeToString(e.Data), line)
	}
	w.Write([]byte("]}"))
}

// ---------------------------------------------------------------------------
// PacketTracer (in-process trampoline hook on send_fn + dual_send_wrap)
//
// Goal: identify D2R-internal handlers so we can CALL_FN_GT them from the
// game thread instead of replaying packets ourselves (which crash the item-
// move handler 0x54 etc.).
// ---------------------------------------------------------------------------

var activeTracer *presenter.Tracer

func (s *HttpServer) getOrOpenTracer(character string) (*presenter.Tracer, error) {
	if activeTracer != nil {
		return activeTracer, nil
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		return nil, fmt.Errorf("no supervisor for %s", character)
	}
	pid := ctx.GameReader.GetPID()
	tr, err := presenter.OpenTracer(pid)
	if err != nil {
		tr, err = presenter.CreateTracer(pid)
		if err != nil {
			return nil, fmt.Errorf("create tracer: %w", err)
		}
		s.logger.Info("Created tracer SHM from Go side", slog.Uint64("pid", uint64(pid)))
	}
	activeTracer = tr
	return tr, nil
}

func (s *HttpServer) debugTraceInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pid := ctx.GameReader.GetPID()
	d2rBase := ctx.GameReader.Process.ModuleBaseAddress()
	// Resolve send_fn / dual_send_wrap RVAs via known offsets (same as DLL uses).
	const sendFnRVA uintptr = 0x146600
	const dualWrapRVA uintptr = 0x147110
	sendFn := d2rBase + sendFnRVA
	dual := d2rBase + dualWrapRVA

	tr, err := s.getOrOpenTracer(character)
	if err != nil {
		fmt.Fprintf(w, `{"error":"open tracer: %s"}`, err.Error())
		return
	}
	ext, err := presenter.StartExternalTracer(pid, d2rBase, sendFn, dual, tr)
	if err != nil {
		fmt.Fprintf(w, `{"error":"start external tracer: %s"}`, err.Error())
		return
	}
	_ = ext
	st := tr.Status()
	fmt.Fprintf(w, `{"ok":true,"mode":"external","pid":%d,"d2r_base":"0x%X","send_fn_va":"0x%X","dual_send_wrap_va":"0x%X","installed_flags":%d}`,
		pid, d2rBase, st.SendFnVA, st.DualSendWrapVA, st.InstalledFlags)
}

func (s *HttpServer) debugTraceUninstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	presenter.StopExternalTracer()
	fmt.Fprintf(w, `{"ok":true,"message":"external tracer stopped"}`)
}

func (s *HttpServer) debugTraceStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	tr, err := s.getOrOpenTracer(character)
	if err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	st := tr.Status()
	fmt.Fprintf(w, `{"magic":"0x%08X","enabled":%d,"head":%d,"tail":%d,"total":%d,"dropped":%d,`+
		`"send_fn_va":"0x%X","dual_send_wrap_va":"0x%X","stub_addr":"0x%X",`+
		`"installed_flags":%d,"last_err":%d,"d2r_base":"0x%X","d2r_text_end":"0x%X","game_tid":%d,`+
		`"orig_send_fn":%q,"orig_dual_send_wrap":%q}`,
		st.Magic, st.Enabled, st.Head, st.Tail, st.Total, st.Dropped,
		st.SendFnVA, st.DualSendWrapVA, st.StubAddr,
		st.InstalledFlags, st.LastErrorCode, st.D2RBase, st.D2RTextEnd, st.GameThreadID,
		hex.EncodeToString(st.OrigSendFn), hex.EncodeToString(st.OrigDualSendWrap))
}

// === Capture hook endpoints (inline send_fn hook, zero-miss) ===

func (s *HttpServer) debugCaptureHookInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter (MODE2=1 or CLAUDE_MODE=1 required)"}`)
		return
	}
	if err := pres.CapHookInstall(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"message":"inline hook armed on send_fn — /debug/capture/drain to read"}`)
}

func (s *HttpServer) debugCaptureHookUninstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	if err := pres.CapHookUninstall(); err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"message":"hook removed, 14 bytes restored"}`)
}

func (s *HttpServer) debugCaptureHookStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	st := pres.CapHookStatusRead()
	fmt.Fprintf(w, `{"fires":%d,"ring_head":%d,"ring_tail":%d,"ring_total":%d,"ring_dropped":%d}`,
		st.Fires, st.RingHead, st.RingTail, st.RingTotal, st.RingDropped)
}

func (s *HttpServer) debugCaptureHookDrain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no presenter"}`)
		return
	}
	entries := pres.CapHookDrain()
	st := pres.CapHookStatusRead()
	fmt.Fprintf(w, `{"count":%d,"fires":%d,"dropped":%d,"entries":[`, len(entries), st.Fires, st.RingDropped)
	for i, e := range entries {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `{"ts":%d,"tid":%d,"size":%d,"pkt_ptr":"0x%X","opcode":"0x%02X","hex":"%s"}`,
			e.TsMs, e.TID, e.Size, e.PktPtr,
			func() byte { if len(e.Payload) > 0 { return e.Payload[0] } else { return 0 } }(),
			hex.EncodeToString(e.Payload))
	}
	fmt.Fprintf(w, `]}`)
}

func (s *HttpServer) debugTraceDump(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	tr, err := s.getOrOpenTracer(character)
	if err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	entries := tr.Drain()
	st := tr.Status()
	w.Write([]byte(fmt.Sprintf(`{"d2r_base":"0x%X","count":%d,"entries":[`, st.D2RBase, len(entries))))
	for i, e := range entries {
		if i > 0 {
			w.Write([]byte(","))
		}
		// Build callstack JSON
		var cs strings.Builder
		cs.WriteString("[")
		for j, fr := range e.Callstack {
			if fr == 0 {
				break
			}
			if j > 0 {
				cs.WriteString(",")
			}
			fmt.Fprintf(&cs, `"0x%016X"`, fr)
		}
		cs.WriteString("]")
		hookName := "sp"
		if e.HookID == 1 {
			hookName = "dsw"
		}
		opcode := byte(0)
		if len(e.Payload) > 0 {
			opcode = e.Payload[0]
		}
		fmt.Fprintf(w, `{"ts":%d,"hook":"%s","tid":%d,"opcode":"0x%02X","len":%d,`+
			`"args":["0x%X","0x%X","0x%X","0x%X"],"callstack":%s,"payload":%q,"annot":%q}`,
			e.Timestamp, hookName, e.TID, opcode, e.PayloadLen,
			e.Args[0], e.Args[1], e.Args[2], e.Args[3],
			cs.String(),
			hex.EncodeToString(e.Payload),
			presenter.AnnotatePayload(e.Payload))
	}
	w.Write([]byte("]}"))
}

// debugCrashInfo returns the in-process crash record captured by the rmod.dll
// VEH (if any). Useful for figuring out what really killed D2R after a packet
// experiment — Arxan's TerminateProcess path bypasses Windows Error Reporting,
// so this is the only signal we get without an attached debugger.
//
// Note: SHM is held open by the bot, so this still returns valid data after
// D2R has died as long as the bot process is still running.
func (s *HttpServer) debugCrashInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character"}`)
		return
	}
	var d presenter.CrashDiag
	source := "live"
	ctx := s.manager.GetContext(character)
	if ctx != nil && ctx.MemoryInjector != nil {
		if pres := ctx.MemoryInjector.GetPresenter(); pres != nil {
			d = pres.ReadCrashDiag()
		}
	}
	if !d.Valid {
		// Fall back to the watchdog snapshot — the bot keeps the last VEH
		// record around even after a supervisor restart so we can still see
		// what killed D2R.
		if snap, ok := bot.GetLastCrashDiag("_last"); ok {
			d = snap
			source = "snapshot"
		}
	}
	_ = source // could echo it back if useful
	w.Write([]byte("{"))
	fmt.Fprintf(w, `"valid":%t,"status":"0x%X","count":%d,"fixups":%d,"av":%d,"so":%d,"sbo":%d,"last_code":"0x%08X"`,
		d.Valid, d.Status, d.Count, d.Fixups, d.CountAV, d.CountSO, d.CountSBO, d.LastCode)
	if d.Valid {
		fmt.Fprintf(w, `,"code":"0x%08X","flags":%d,"tid":%d,"fault_type":%d`,
			d.Code, d.Flags, d.TID, d.FaultType)
		fmt.Fprintf(w, `,"rip":"0x%016X","rip_sym":%q,"fault_va":"0x%016X","rsp":"0x%016X"`,
			d.RIP, bot.ResolveAddress(uintptr(d.RIP)), d.FaultVA, d.RSP)
		w.Write([]byte(`,"regs":{`))
		for i, v := range d.Regs {
			if i > 0 {
				w.Write([]byte(","))
			}
			fmt.Fprintf(w, `%q:"0x%016X"`, presenter.RegNames[i], v)
		}
		w.Write([]byte(`}`))
		w.Write([]byte(`,"frames":[`))
		for i, f := range d.Frames {
			if i > 0 {
				w.Write([]byte(","))
			}
			rawHex := ""
			if i < len(d.FrameBytes) && d.FrameBytes[i] != nil {
				rawHex = hex.EncodeToString(d.FrameBytes[i])
			}
			fmt.Fprintf(w, `{"va":"0x%016X","sym":%q,"bytes":%q}`,
				f, bot.ResolveAddress(uintptr(f)), rawHex)
		}
		w.Write([]byte(`]`))
		if len(d.RIPBytes) > 0 {
			fmt.Fprintf(w, `,"rip_bytes":%q,"rip_bytes_pre":%d`,
				hex.EncodeToString(d.RIPBytes), presenter.CrashRIPBytesPre)
		}
	}
	w.Write([]byte("}"))
}

// debugRPMCounter reports the live cross-process NtReadVirtualMemory activity
// against D2R from app.exe (Phase D audit — P1-GID measures how much of
// GetData() still falls through to RPM vs the SnapshotReader).
//
// Query params:
//   character — required
//   reset=1   — zero counters after reporting (useful between audit windows)
//
// Response: JSON with per-path call counts + total RPM bytes + reader source
// (snapshot vs rpm) + D2R handle state.
func (s *HttpServer) debugRPMCounter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor for character %s"}`, character)
		return
	}

	proc := ctx.GameReader.Process
	reads, uints, strs, bufs, bytesTotal := proc.RPMStats()

	fmt.Fprintf(w,
		`{"reader_source":%q,"pid":%d,"handle_open":%v,"rpm":{"read_bytes_calls":%d,"read_uint_calls":%d,"read_string_calls":%d,"read_buffer_calls":%d,"bytes_total":%d}}`,
		ctx.GameReader.ReaderSource(),
		proc.PID(),
		proc.HandleOpen(),
		reads, uints, strs, bufs, bytesTotal,
	)

	if r.URL.Query().Get("reset") == "1" {
		proc.ResetRPMStats()
	}
}

// debugRopScan triggers CMD_ROP_SCAN in rmod over the specified .text range.
// Usage: GET /debug/rop-scan?character=Blizzard[&base=0x7FF6...&len=0x100000]
// If base/len omitted, uses D2R module base + 0x80_0000 (8 MB — conservative
// upper bound for .text). Returns gadget count and ready flag.
func (s *HttpServer) debugRopScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"presenter not initialized (MODE2+CLAUDE_MODE path only)"}`)
		return
	}

	var baseVA uint64
	var lenVal uint64
	if b := r.URL.Query().Get("base"); b != "" {
		fmt.Sscanf(b, "0x%x", &baseVA)
	}
	if baseVA == 0 {
		baseVA = uint64(ctx.GameReader.Process.ModuleBaseAddress())
	}
	if l := r.URL.Query().Get("len"); l != "" {
		fmt.Sscanf(l, "0x%x", &lenVal)
	}
	if lenVal == 0 {
		// 1 MB default — full .text scan (8 MB) inside Present callback
		// hits d3d12's watchdog. For production, scan incrementally in
		// ~1 MB chunks via repeated calls with advancing base.
		lenVal = 0x100000
	}

	count, ready, err := pres.RopScan(baseVA, lenVal)
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q,"base":"0x%X","len":"0x%X"}`, err.Error(), baseVA, lenVal)
		return
	}
	// Also include gadget-kind breakdown so caller can diagnose why
	// subsequent build_memcpy might fail.
	pool, _ := pres.RopPool()
	fmt.Fprintf(w, `{"ok":true,"base":"0x%X","len":"0x%X","gadgets":%d,"ready":%d,`+
		`"pool":{"Unknown":%d,"PopReg":%d,"MovRegMem":%d,"MovMemReg":%d,"RepMovsb":%d,"RepMovsq":%d,"XchgReg":%d,"Ret":%d,"PopRegMask":"0x%X"}}`,
		baseVA, lenVal, count, ready,
		pool.Unknown, pool.PopReg, pool.MovRegMem, pool.MovMemReg,
		pool.RepMovsb, pool.RepMovsq, pool.XchgReg, pool.Ret, pool.PopRegMask)
}

// debugRopRead triggers CMD_ROP_READ — ROP-chain memcpy(src, dst, len). Currently
// gated on rmod side (returns status=2 always). Useful as a liveness probe
// before the trigger encoding is unit-tested and the path un-gated.
// Usage: GET /debug/rop-read?character=Blizzard&src=0xNN&dst=0xNN&len=0xNN
func (s *HttpServer) debugRopRead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"presenter not initialized"}`)
		return
	}
	var src, dst, length uint64
	fmt.Sscanf(r.URL.Query().Get("src"), "0x%x", &src)
	fmt.Sscanf(r.URL.Query().Get("dst"), "0x%x", &dst)
	fmt.Sscanf(r.URL.Query().Get("len"), "0x%x", &length)
	// dst=0 is the rmod sentinel: "use internal SHM scratch buffer".
	// That path is the safest single-read probe because the destination is
	// always writable and we don't depend on knowing any app-side VA.
	if src == 0 || length == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"src/len required (hex, non-zero)"}`)
		return
	}
	status, err := pres.RopRead(src, dst, length)
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"status":%d,"note":"2 = ROP handler gated; unit-test trigger first"}`, status)
}

// debugRopReadBatch dispatches CMD_ROP_READ_BATCH with N entries parsed from
// the URL. Entries is a comma-separated list of "<hex_src>:<dec_len>".
//
// Usage: GET /debug/rop-read-batch?character=Blizzard&entries=0x7ff691f56600:8,0x7ff691f56608:16
func (s *HttpServer) debugRopReadBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"presenter not initialized"}`)
		return
	}
	raw := r.URL.Query().Get("entries")
	if raw == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"entries required: hex_src:dec_len comma-separated"}`)
		return
	}
	var batch []presenter.BatchReadEntry
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		parts := strings.SplitN(tok, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(w, `{"error":"bad entry %q — expect hex_src:dec_len"}`, tok)
			return
		}
		var src uint64
		var length uint32
		if _, err := fmt.Sscanf(parts[0], "0x%x", &src); err != nil || src == 0 {
			fmt.Fprintf(w, `{"error":"entry %q src parse failed"}`, tok)
			return
		}
		if _, err := fmt.Sscanf(parts[1], "%d", &length); err != nil || length == 0 {
			fmt.Fprintf(w, `{"error":"entry %q len parse failed"}`, tok)
			return
		}
		batch = append(batch, presenter.BatchReadEntry{Src: uintptr(src), Len: length})
	}
	started := time.Now()
	out, err := pres.RopReadBatch(batch)
	elapsed := time.Since(started)
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q,"elapsed_ms":%.2f}`, err.Error(), float64(elapsed.Microseconds())/1000)
		return
	}
	// Report first 16 bytes of each entry's read.
	fmt.Fprintf(w, `{"ok":true,"count":%d,"elapsed_ms":%.2f,"entries":[`, len(out), float64(elapsed.Microseconds())/1000)
	for i, buf := range out {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		preview := buf
		if len(preview) > 16 {
			preview = preview[:16]
		}
		fmt.Fprintf(w, `{"len":%d,"first":"%x"}`, len(buf), preview)
	}
	fmt.Fprint(w, "]}")
}

// debugReadTrace dumps the ring buffer of recent ReadBytesFromMemory calls.
// Per Bartek's request: shows each read's (addr, size, source, result,
// latency) so we can pinpoint which specific read first faults when
// ROP_READ / SNAPSHOT_ENABLE is switched on. Off by default — enable via
// /debug/read-trace-enable?on=1 or CLAUDE_READ_TRACE=1 env.
//
// Usage: GET /debug/read-trace?character=Blizzard[&limit=200]
func (s *HttpServer) debugReadTrace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	tr := ctx.GameReader.Process.ReadTrace()
	if tr == nil {
		fmt.Fprintf(w, `{"error":"trace not initialised"}`)
		return
	}
	entries := tr.Dump()
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		var v int
		fmt.Sscanf(l, "%d", &v)
		if v > 0 && v < len(entries) {
			limit = v
		}
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	fmt.Fprintf(w, `{"ok":true,"enabled":%t,"count":%d,"entries":[`, tr.Enabled(), len(entries))
	for i, e := range entries {
		if i > 0 { w.Write([]byte(",")) }
		fmt.Fprintf(w, `{"tick":%d,"t_ms":%d,"addr":"0x%X","size":%d,"src":"%s","result":"%s","lat_ns":%d}`,
			e.Tick, e.Timestamp.UnixMilli(), e.Address, e.Size, e.Source, e.Result, e.LatencyNs)
	}
	fmt.Fprintf(w, `]}`)
}

// debugReadTraceStats returns source-rollup counters without walking the ring.
func (s *HttpServer) debugReadTraceStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	tr := ctx.GameReader.Process.ReadTrace()
	if tr == nil {
		fmt.Fprintf(w, `{"error":"trace not initialised"}`)
		return
	}
	bcalls, bfail, bentries := ctx.GameReader.Process.BatchStats()
	fmt.Fprintf(w, `{"ok":true,"enabled":%t,"counters":%q,"batch":{"calls":%d,"failed":%d,"entries":%d}}`,
		tr.Enabled(), tr.Stats(), bcalls, bfail, bentries)
}

// debugReadTraceEnable toggles the trace on/off at runtime.
// Usage: GET /debug/read-trace-enable?character=Blizzard&on=1
func (s *HttpServer) debugReadTraceEnable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	tr := ctx.GameReader.Process.ReadTrace()
	if tr == nil {
		fmt.Fprintf(w, `{"error":"trace not initialised"}`)
		return
	}
	on := r.URL.Query().Get("on") == "1"
	tr.Enable(on)
	fmt.Fprintf(w, `{"ok":true,"enabled":%t}`, tr.Enabled())
}

// debugRopDbg returns the current OFF_ROP_DBG marker. Useful for post-
// init diagnostics before any scan overwrites it.
func (s *HttpServer) debugRopDbg(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"presenter not initialized"}`)
		return
	}
	dbg, err := pres.RopDbg()
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"dbg":"0x%08X"}`, dbg)
}

// debugRopWorkerHb returns the ROP scan worker's heartbeat counter. Plan B
// diagnostic — the worker bumps it every ~30 ms. Counter stays at 0 if the
// worker thread never spawned; stays at 1 if it got past spawn but never
// entered the loop body; and increments monotonically if actively looping.
// Usage: GET /debug/rop-worker-hb?character=Blizzard
func (s *HttpServer) debugRopWorkerHb(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"presenter not initialized"}`)
		return
	}
	hb, err := pres.RopWorkerHeartbeat()
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q}`, err.Error())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"heartbeat":%d}`, hb)
}

// debugDispatchPing sends CMD_NOP and measures round-trip time. Used to
// isolate "Present detour not dispatching" from "specific handler hanging".
// Expected latency: one Present frame (~16 ms at 60 fps). If this times out,
// D2R isn't rendering or rmod's detour isn't installed. If this succeeds but
// rop-scan times out, the bug is in the scan handler.
// Usage: GET /debug/dispatch-ping?character=Blizzard
func (s *HttpServer) debugDispatchPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}
	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.MemoryInjector == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no supervisor"}`)
		return
	}
	pres := ctx.MemoryInjector.GetPresenter()
	if pres == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"presenter not initialized"}`)
		return
	}
	lat, err := pres.DispatchPing()
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":%q,"latency_ms":%d}`, err.Error(), lat.Milliseconds())
		return
	}
	fmt.Fprintf(w, `{"ok":true,"latency_ms":%d,"latency_us":%d}`, lat.Milliseconds(), lat.Microseconds())
}

// debugHandleAudit returns the D2R handle-open state for the running
// supervisor. Phase D expected state once snapshot is live: handle_open=false
// after CloseHandle(d2r_handle) is wired in. Meanwhile this endpoint
// reports the actual state (proves the bot hasn't closed the handle yet if
// Inventory / WidgetStates still fall back to RPM).
func (s *HttpServer) debugHandleAudit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	character := r.URL.Query().Get("character")
	if character == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"missing character parameter"}`)
		return
	}

	ctx := s.manager.GetContext(character)
	if ctx == nil || ctx.GameReader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"no running supervisor for character %s"}`, character)
		return
	}

	proc := ctx.GameReader.Process
	fmt.Fprintf(w,
		`{"pid":%d,"handle_open":%v,"reader_source":%q}`,
		proc.PID(),
		proc.HandleOpen(),
		ctx.GameReader.ReaderSource(),
	)
}
