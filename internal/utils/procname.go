package utils

// GameExeName returns the target process name, constructed at runtime to avoid static strings.
func GameExeName() string {
	// D=0x44 2=0x32 R=0x52 .=0x2E e=0x65 x=0x78 e=0x65 each XOR 0x55
	enc := []byte{0x11, 0x67, 0x07, 0x7B, 0x30, 0x2D, 0x30}
	for i := range enc {
		enc[i] ^= 0x55
	}
	return string(enc)
}
