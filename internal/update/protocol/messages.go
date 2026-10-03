package protocol

type Review struct {
	Token    string   `json:"token"`
	Digest   string   `json:"digest"`
	Manifest Manifest `json:"manifest"`
	Source   Versions `json:"source"`
	Network  string   `json:"network"`
	// UpdateFirst is guidance instead of a review: the selected release does
	// not admit this node, and its plan names this release to install first.
	// Such an answer has no token and cannot be approved.
	UpdateFirst string `json:"update_first,omitempty"`
}

// Status deliberately excludes local paths, credentials and raw command output.
type Status struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	Phase         string `json:"phase"`
	Step          string `json:"step"`
	Error         string `json:"error,omitempty"`
	Active        bool   `json:"active"`
	Running       bool   `json:"running"`
	Cancellable   bool   `json:"cancellable"`
	WalletNetwork string `json:"wallet_network,omitempty"`
}
