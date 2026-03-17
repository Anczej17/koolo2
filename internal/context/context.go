package context

import (
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/drop"
	"github.com/hectorgimenez/koolo/internal/event"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/health"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/utils"
)

var mu sync.Mutex
var botContexts = make(map[uint64]*Status)

type Priority int32

type StopFunc func()

const (
	PriorityHigh       Priority = 0
	PriorityNormal     Priority = 1
	PriorityBackground Priority = 5
	PriorityPause      Priority = 10
	PriorityStop       Priority = 100
)

type Status struct {
	*Context
	Priority Priority
}

type Context struct {
	Name                      string
	executionPriority         atomic.Int32
	CharacterCfg              *config.CharacterCfg
	Data                      *game.Data
	EventListener             *event.Listener
	HID                       *game.HID
	Logger                    *slog.Logger
	Manager                   *game.Manager
	GameReader                *game.MemoryReader
	MemoryInjector            *game.MemoryInjector
	PathFinder                *pather.PathFinder
	BeltManager               *health.BeltManager
	HealthManager             *health.Manager
	Char                      Character
	LastBuffAt                time.Time
	WasInTown                 bool      // Track if we were in town (to detect leaving town)
	BuffInProgress            bool      // Prevent concurrent buff execution for this character
	BestWeaponSlotCache       map[skill.ID]int // Per-character cache: best weapon slot (0=primary, 1=secondary) per buff skill
	WeaponCacheReady          bool             // Per-character flag: true after weapon probe has run
	LastCastAt                time.Time
	ContextDebug              map[Priority]*Debug
	CurrentGame               *CurrentGameHelper
	SkillPointIndex           int // NEW FIELD: Tracks the next skill to consider from the character's SkillPoints() list
	ForceAttack               bool
	StopSupervisorFn          StopFunc
	CleanStopRequested        bool
	RestartWithCharacter      string
	PacketSender              *game.PacketSender
	IsLevelingCharacter       *bool
	ManualModeActive          bool          // Manual play mode: stops after character selection
	LastPortalTick            time.Time     // NEW FIELD: Tracks last portal creation for spam prevention
	IsBossEquipmentActive     bool          // flag for barb leveling
	Drop                      *drop.Manager // Drop: Per-supervisor Drop manager
	IsAllocatingStatsOrSkills atomic.Bool   // Prevents stuck detection during stat/skill allocation
	WaitingForParty           atomic.Bool   // Prevents stuck detection while waiting for party members
	CompletedRuns              []string      // Runs completed in current game (survives bot.Run() reset)
	CompletedGameID            string        // Game name for which CompletedRuns is valid
	completedRunsMu            sync.Mutex
	CurrentRunName             string        // Name of the currently executing run (for failed run tracking)
	AbortBonusRun              atomic.Bool   // Signal long-running actions (gambling) to abort during bonus runs
	FailedToCreateGameAttempts int           // Consecutive lobby game creation failures (survives bot.Run() reset)
	FailedModalDismissAttempts int           // Consecutive modal dismiss failures (survives bot.Run() reset)
}

type Debug struct {
	LastAction string `json:"lastAction"`
	LastStep   string `json:"lastStep"`
}

type CurrentGameHelper struct {
	BlacklistedItems []data.Item
	PickedUpItems    map[int]int
	UnstashableItems map[data.UnitID]bool // Items that failed to stash on all tabs — skip on subsequent Stash() calls this game
	CurrentStashTab  int                  // Tracks which stash tab/page the UI is showing (0 = unknown/closed)
	HasOpenedStash   bool                 // True after the first stash open this game; the first open always lands on personal tab, subsequent opens remember the last position
	AreaCorrection   struct {
		Enabled      bool
		ExpectedArea area.ID
	}
	PickupItems        bool
	IsPickingItems     bool
	FailedMenuAttempts int
	// When this is set, the supervisor will stop and the manager will start a new supervisor for the specified character.
	SwitchToCharacter string
	// Used to store the original character name when muling, so we can switch back.
	OriginalCharacter string
	CurrentMuleIndex  int
	ShouldCheckStash  bool
	StashFull         bool
	mutex sync.Mutex
}

// ResetForNewGame resets per-game fields without replacing the entire struct,
// preserving state that must survive across bot.Run() calls (e.g. bonus run flags).
func (h *CurrentGameHelper) ResetForNewGame() {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.PickupItems = true
	h.IsPickingItems = false
	h.PickedUpItems = make(map[int]int)
	h.BlacklistedItems = []data.Item{}
	h.UnstashableItems = make(map[data.UnitID]bool)
	h.CurrentStashTab = 0
	h.HasOpenedStash = false
	h.AreaCorrection.Enabled = false
	h.AreaCorrection.ExpectedArea = 0
	h.FailedMenuAttempts = 0
	h.ShouldCheckStash = false
	h.StashFull = false
}

func (ctx *Context) StopSupervisor() {
	if ctx.StopSupervisorFn != nil {
		ctx.Logger.Info("Game logic requested supervisor stop.", "source", "context")
		ctx.CleanStopRequested = true // SET THE FLAG
		ctx.StopSupervisorFn()
	} else {
		ctx.Logger.Warn("StopSupervisorFn is not set. Cannot stop supervisor from context.")
	}
}

func NewContext(name string) *Status {
	ctx := &Context{
		Name:              name,
		Data:              &game.Data{},
		// executionPriority defaults to 0 (PriorityHigh); set to Normal after init
		ContextDebug: map[Priority]*Debug{
			PriorityBackground: {},
			PriorityNormal:     {},
			PriorityHigh:       {},
			PriorityPause:      {},
			PriorityStop:       {},
		},
		CurrentGame:      NewGameHelper(),
		SkillPointIndex:  0,
		ForceAttack:      false,
		ManualModeActive: false, // Explicitly initialize to false
	}
	ctx.SwitchPriority(PriorityNormal)
	ctx.Drop = drop.NewManager(name, ctx.Logger)
	ctx.AttachRoutine(PriorityNormal)

	// Initialize ping getter for adaptive delays (avoids import cycle)
	utils.SetPingGetter(func() int {
		if ctx.Data != nil && ctx.Data.Game.Ping > 0 {
			return ctx.Data.Game.Ping
		}
		return 50 // Safe default
	})

	return Get()
}

func NewGameHelper() *CurrentGameHelper {
	return &CurrentGameHelper{
		PickupItems:                true,
		PickedUpItems:              make(map[int]int),
		BlacklistedItems:           []data.Item{},
		UnstashableItems:           make(map[data.UnitID]bool),
	}
}

func Get() *Status {
	mu.Lock()
	defer mu.Unlock()
	return botContexts[getGoroutineID()]
}

func (s *Status) SetLastAction(actionName string) {
	s.Context.ContextDebug[s.Priority].LastAction = actionName
}

func (s *Status) SetLastStep(stepName string) {
	s.Context.ContextDebug[s.Priority].LastStep = stepName
}

func getGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	stackTrace := string(buf[:n])
	fields := strings.Fields(stackTrace)
	id, _ := strconv.ParseUint(fields[1], 10, 64)

	return id
}

func (ctx *Context) RefreshGameData() {
	*ctx.Data = ctx.GameReader.GetData()
	if ctx.IsLevelingCharacter == nil {
		_, isLevelingCharacter := ctx.Char.(LevelingCharacter)
		ctx.IsLevelingCharacter = &isLevelingCharacter
	}
	ctx.Data.IsLevelingCharacter = *ctx.IsLevelingCharacter
}

func (ctx *Context) RefreshInventory() {
	ctx.Data.Inventory = ctx.GameReader.GetInventory()
}

func (ctx *Context) Detach() {
	mu.Lock()
	defer mu.Unlock()
	delete(botContexts, getGoroutineID())
}

func (ctx *Context) AttachRoutine(priority Priority) {
	mu.Lock()
	defer mu.Unlock()
	botContexts[getGoroutineID()] = &Status{Priority: priority, Context: ctx}
}

func (ctx *Context) SwitchPriority(priority Priority) {
	ctx.executionPriority.Store(int32(priority))
}

func (ctx *Context) GetPriority() Priority {
	return Priority(ctx.executionPriority.Load())
}

func (ctx *Context) DisableItemPickup() {
	ctx.CurrentGame.PickupItems = false
}

func (ctx *Context) EnableItemPickup() {
	ctx.CurrentGame.PickupItems = true
}

func (ctx *Context) SetPickingItems(value bool) {
	ctx.CurrentGame.mutex.Lock()
	ctx.CurrentGame.IsPickingItems = value
	ctx.CurrentGame.mutex.Unlock()
}

// AddCompletedRun records a run as completed in the current game session.
func (ctx *Context) AddCompletedRun(name string) {
	ctx.completedRunsMu.Lock()
	defer ctx.completedRunsMu.Unlock()
	ctx.CompletedRuns = append(ctx.CompletedRuns, name)
}

// GetCompletedRuns returns a copy of completed run names for the current game.
func (ctx *Context) GetCompletedRuns() []string {
	ctx.completedRunsMu.Lock()
	defer ctx.completedRunsMu.Unlock()
	result := make([]string, len(ctx.CompletedRuns))
	copy(result, ctx.CompletedRuns)
	return result
}

// ResetCompletedRuns clears the completed runs list and sets the new game ID.
func (ctx *Context) ResetCompletedRuns(gameID string) {
	ctx.completedRunsMu.Lock()
	defer ctx.completedRunsMu.Unlock()
	ctx.CompletedRuns = nil
	ctx.CompletedGameID = gameID
}

func (s *Status) PauseIfNotPriority() {
	// This prevents bot from trying to move when loading screen is shown.
	if s.Data.OpenMenus.LoadingScreen {
		time.Sleep(time.Millisecond * 5)
	}

	for s.Priority != s.GetPriority() {
		if s.GetPriority() == PriorityStop {
			panic("Bot is stopped")
		}

		time.Sleep(time.Millisecond * 10)
	}
}
func (ctx *Context) WaitForGameToLoad() {
	for ctx.Data.OpenMenus.LoadingScreen {
		time.Sleep(100 * time.Millisecond)
		ctx.RefreshGameData()
	}
	// Add a small buffer to ensure everything is fully loaded
	time.Sleep(300 * time.Millisecond)
}

func (ctx *Context) Cleanup() {
	ctx.Logger.Debug("Resetting blacklisted items")

	// Remove all items from the blacklisted items list
	ctx.CurrentGame.BlacklistedItems = []data.Item{}

	// flag reset in case something goes wrong (barb leveling)
	ctx.IsBossEquipmentActive = false

	// Remove all items from the picked up items map if it exceeds 200 items
	if len(ctx.CurrentGame.PickedUpItems) > 200 {
		ctx.Logger.Debug("Resetting picked up items map due to exceeding 200 items")
		ctx.CurrentGame.PickedUpItems = make(map[int]int)
	}
	// Reset counters on cleanup for a new session
	ctx.CurrentGame.FailedMenuAttempts = 0
}
