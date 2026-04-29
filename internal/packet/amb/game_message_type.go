package amb

// GameMessageType represents the type of game message being sent
type GameMessageType byte

const (
	GameMessage        GameMessageType = 1
	GameWhisper        GameMessageType = 2
	OverheadMessage    GameMessageType = 5
	GameWhisperReceipt GameMessageType = 6
)
