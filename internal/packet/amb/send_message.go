package amb

// SendMessage represents the packet for sending chat messages in game
// NOTE: Packet 0x15 is NOT documented in PacketOffsetsGo.txt OUT packets list
// This packet is used for in-game chat/whispers and appears to work
// Packet structure:
// [PacketID:1 byte][Type:1 byte][pad0:1 byte][Message:null-terminated string][Recipient:null-terminated string][pad:1 byte]
type SendMessage struct {
	PacketID  byte
	Type      GameMessageType
	pad0      byte
	Message   string
	Recipient string
	pad       byte
}

// NewSendMessage creates a new SendMessage packet for general game messages
func NewSendMessage(messageType GameMessageType, message string) *SendMessage {
	return &SendMessage{
		PacketID:  0x15, // 21 in decimal - SendMessage packet ID
		Type:      messageType,
		pad0:      0,
		Message:   message,
		Recipient: "",
		pad:       0,
	}
}

// NewSendWhisper creates a new SendMessage packet for whispers
func NewSendWhisper(recipient string, message string) *SendMessage {
	return &SendMessage{
		PacketID:  0x15,
		Type:      GameWhisper,
		pad0:      0,
		Message:   message,
		Recipient: recipient,
		pad:       0,
	}
}

// GetPayload converts the SendMessage struct to bytes
func (p *SendMessage) GetPayload() []byte {
	// Structure: PacketID + Type + pad0 + Message (null-terminated) + Recipient (null-terminated) + pad
	buf := make([]byte, 0, 128)

	// PacketID (1 byte)
	buf = append(buf, p.PacketID)

	// Type (1 byte)
	buf = append(buf, byte(p.Type))

	// pad0 (1 byte) - seems to be padding/reserved
	buf = append(buf, 0x00)

	// Message (variable length, null-terminated)
	buf = append(buf, []byte(p.Message)...)
	buf = append(buf, 0x00) // null terminator

	// Recipient (variable length, null-terminated)
	// For non-whisper messages, this is just an empty null-terminated string
	if p.Recipient != "" {
		buf = append(buf, []byte(p.Recipient)...)
	}
	buf = append(buf, 0x00) // null terminator

	// pad (1 byte) - trailing padding
	buf = append(buf, 0x00)

	return buf
}
