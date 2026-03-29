package main

import "time"

type ResponseTier int

const (
	TierAuto     ResponseTier = 1 // Log, email, forensic capture
	TierContain  ResponseTier = 2 // Quarantine, IP block (auto-expire)
	TierApproval ResponseTier = 3 // Token revocation, network isolation
)

type IncidentType string

const (
	IncidentBruteForce      IncidentType = "brute_force"
	IncidentQuarantine      IncidentType = "quarantine"
	IncidentCredentialTheft IncidentType = "credential_theft"
	IncidentBinaryTamper    IncidentType = "binary_tamper"
	IncidentLateralMovement IncidentType = "lateral_movement"
	IncidentRogueService    IncidentType = "rogue_service"
)

type Incident struct {
	ID         string           `json:"id"`
	Type       IncidentType     `json:"type"`
	Tier       ResponseTier     `json:"tier"`
	Machines   []string         `json:"machines"`
	Events     []SecurityEvent  `json:"events"`
	Actions    []ResponseAction `json:"actions"`
	Status     string           `json:"status"` // active, contained, resolved, approval_pending
	CreatedAt  time.Time        `json:"created_at"`
	ResolvedAt *time.Time       `json:"resolved_at,omitempty"`
}

type ResponseAction struct {
	Type       string     `json:"type"` // ip_block, quarantine, forensic_capture, email, token_revoke
	Machine    string     `json:"machine"`
	Detail     string     `json:"detail"`
	Status     string     `json:"status"` // pending, executing, complete, failed
	ExecutedAt *time.Time `json:"executed_at,omitempty"`
	Result     string     `json:"result,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type ForensicSnapshot struct {
	Machine      string    `json:"machine"`
	CapturedAt   time.Time `json:"captured_at"`
	Processes    string    `json:"processes"`
	Listeners    string    `json:"listeners"`
	RecentLogins string    `json:"recent_logins"`
	CurrentUsers string    `json:"current_users"`
	SSHLogs      string    `json:"ssh_logs"`
	TempFiles    string    `json:"temp_files"`
	Services     string    `json:"services"`
	FileHash     string    `json:"file_hash,omitempty"`
}

func incidentTier(t IncidentType) ResponseTier {
	switch t {
	case IncidentBruteForce:
		return TierContain
	case IncidentBinaryTamper:
		return TierContain
	case IncidentRogueService:
		return TierAuto
	case IncidentCredentialTheft:
		return TierApproval
	case IncidentLateralMovement:
		return TierApproval
	default:
		return TierAuto
	}
}
