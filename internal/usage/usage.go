// Package usage defines the provider-neutral account quota snapshot exposed
// by backend plugins and rendered by the shell's Dock usage panel.
package usage

// Window is one provider-reported rate-limit window. UsedPercent and ResetAt
// come from the provider; WindowSeconds identifies the window and lets the
// frontend compare weekly consumption with elapsed cycle time.
type Window struct {
	ID            string  `json:"id"`
	UsedPercent   float64 `json:"used_percent"`
	ResetAt       string  `json:"reset_at,omitempty"`
	WindowSeconds int64   `json:"window_seconds"`
}

// Account is one credential's current quota. Errors are per account so one
// unavailable provider does not hide the other providers' snapshots.
type Account struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Provider  string   `json:"provider"`
	Model     string   `json:"model,omitempty"`
	Plan      string   `json:"plan,omitempty"`
	Active    bool     `json:"active,omitempty"`
	FetchedAt string   `json:"fetched_at"`
	Error     string   `json:"error,omitempty"`
	Windows   []Window `json:"windows"`
}
