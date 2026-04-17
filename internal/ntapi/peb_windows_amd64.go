//go:build windows && amd64

package ntapi

// getPEBDirect reads the Process Environment Block address directly from
// GS:[0x60] — the canonical x64 Windows TEB.ProcessEnvironmentBlock slot.
// Zero syscalls, zero API calls, zero plaintext strings. Implemented in
// peb_windows_amd64.s to avoid the NtQueryInformationProcess bootstrap that
// leaves "NtQueryInformationProcess" and "ntdll.dll" in the binary.
func getPEBDirect() uintptr
