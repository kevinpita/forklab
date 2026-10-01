package tui

import (
	"encoding/json"
	"time"
)

// These types mirror the --json output of the forklab commands the TUI runs.
// They are decoded from the CLI, never shared with it, so the TUI depends on
// the same contract a script does.

type nodeInfo struct {
	Index         int        `json:"index"`
	Name          string     `json:"name"`
	State         string     `json:"state"`
	Binary        string     `json:"binary"`
	PID           int        `json:"pid"`
	Adopted       bool       `json:"adopted"`
	StartedAt     *time.Time `json:"started_at"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	ExitCode      *int       `json:"exit_code"`
	Signal        string     `json:"signal"`
	ExitedAt      *time.Time `json:"exited_at"`
}

type chainPlan struct {
	Name   string `json:"name"`
	Height int64  `json:"height,string"`
	Info   string `json:"info"`
}

type nodeStatus struct {
	Name       string     `json:"name"`
	RPCPort    int        `json:"rpc_port"`
	Up         bool       `json:"up"`
	Error      string     `json:"error"`
	Height     int64      `json:"height"`
	BlockTime  *time.Time `json:"block_time"`
	CatchingUp bool       `json:"catching_up"`
	Peers      int        `json:"peers"`
}

type validatorInfo struct {
	Address      string  `json:"address"`
	Node         string  `json:"node"`
	Moniker      string  `json:"moniker"`
	Operator     string  `json:"operator"`
	VotingPower  int64   `json:"voting_power"`
	PowerPercent float64 `json:"power_percent"`
	Status       string  `json:"status"`
	Jailed       bool    `json:"jailed"`
	Tokens       string  `json:"tokens"`
}

type pauseStatus struct {
	Height int64  `json:"height"`
	Phase  string `json:"phase"`
}

type labStatus struct {
	Pause        *pauseStatus    `json:"pause"`
	Lab          string          `json:"lab"`
	ChainID      string          `json:"chain_id"`
	Height       int64           `json:"height"`
	AvgBlockTime string          `json:"avg_block_time"`
	Nodes        []nodeStatus    `json:"nodes"`
	Validators   []validatorInfo `json:"validators"`
	UpgradePlan  *chainPlan      `json:"upgrade_plan"`
	Warnings     []string        `json:"warnings"`
}

type vote struct {
	Kind  string `json:"kind"`
	Block string `json:"block"`
}

type voteTally struct {
	Bits     string  `json:"bits"`
	Power    int64   `json:"power"`
	Total    int64   `json:"total"`
	Fraction float64 `json:"fraction"`
}

type consensusValidator struct {
	Index       int    `json:"index"`
	Address     string `json:"address"`
	Node        string `json:"node"`
	VotingPower int64  `json:"voting_power"`
	Prevote     vote   `json:"prevote"`
	Precommit   vote   `json:"precommit"`
	LastCommit  vote   `json:"last_commit"`
}

type nodePeers struct {
	Name  string   `json:"name"`
	Up    bool     `json:"up"`
	Error string   `json:"error"`
	Peers []string `json:"peers"`
}

type consensusState struct {
	Source          string               `json:"source"`
	Height          int64                `json:"height"`
	Round           int32                `json:"round"`
	Step            string               `json:"step"`
	ProposerAddress string               `json:"proposer_address"`
	ProposerNode    string               `json:"proposer_node"`
	Prevotes        voteTally            `json:"prevotes"`
	Precommits      voteTally            `json:"precommits"`
	LastCommit      voteTally            `json:"last_commit"`
	Validators      []consensusValidator `json:"validators"`
	Nodes           []nodePeers          `json:"nodes"`
}

type coin struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

type tally struct {
	Yes        string `json:"yes"`
	No         string `json:"no"`
	Abstain    string `json:"abstain"`
	NoWithVeto string `json:"no_with_veto"`
}

type proposal struct {
	MessagePayloads []json.RawMessage `json:"message_payloads"`
	Metadata        string            `json:"metadata"`
	DepositEndTime  *time.Time        `json:"deposit_end_time"`
	VotingStartTime *time.Time        `json:"voting_start_time"`
	ID              uint64            `json:"id"`
	Title           string            `json:"title"`
	Summary         string            `json:"summary"`
	Status          string            `json:"status"`
	Messages        []string          `json:"messages"`
	FinalTally      tally             `json:"final_tally"`
	TotalDeposit    []coin            `json:"total_deposit"`
	SubmitTime      *time.Time        `json:"submit_time"`
	VotingEndTime   *time.Time        `json:"voting_end_time"`
	Expedited       bool              `json:"expedited"`
	FailedReason    string            `json:"failed_reason"`
	Proposer        string            `json:"proposer"`
	// Tally is the live count, set only by gov show.
	Tally *tally `json:"tally"`
}

type upgradeTarget struct {
	Index   int    `json:"index"`
	Binary  string `json:"binary"`
	Version string `json:"version"`
}

type upgradeRecovery struct {
	ID      string          `json:"id"`
	Mode    string          `json:"mode"`
	Targets []upgradeTarget `json:"targets"`
}

type pendingUpgrade struct {
	Previous   []upgradeTarget  `json:"previous"`
	Recovery   *upgradeRecovery `json:"recovery"`
	Name       string           `json:"name"`
	Height     int64            `json:"height"`
	Version    string           `json:"version"`
	Binary     string           `json:"binary"`
	AutoSwap   bool             `json:"auto_swap"`
	ProposalID uint64           `json:"proposal_id"`
}

type halt struct {
	Name   string `json:"name"`
	Height int64  `json:"height"`
	Binary string `json:"binary"`
}

type upgradeNode struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Binary    string `json:"binary"`
	State     string `json:"state"`
	Phase     string `json:"upgrade"`
	Halt      *halt  `json:"halt"`
	SwapError string `json:"swap_error"`
}

type upgradeStatus struct {
	Lab       string          `json:"lab"`
	Plan      *chainPlan      `json:"plan"`
	Pending   *pendingUpgrade `json:"pending"`
	Completed *pendingUpgrade `json:"completed"`
	Nodes     []upgradeNode   `json:"nodes"`
	Warnings  []string        `json:"warnings"`
}

type account struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Balances []coin `json:"balances"`
}

type port struct {
	File string `json:"file"`
	Key  string `json:"key"`
	Port int    `json:"port"`
}

type labNode struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	NodeID    string `json:"node_id"`
	Validator string `json:"validator"`
	Ports     []port `json:"ports"`
}

type labInfo struct {
	Name          string     `json:"name"`
	Dir           string     `json:"dir"`
	Running       bool       `json:"running"`
	Error         string     `json:"error"`
	Mode          string     `json:"mode"`
	ChainID       string     `json:"chain_id"`
	ChainIDSource string     `json:"chain_id_source"`
	Version       string     `json:"version"`
	Validators    int        `json:"validators"`
	CreatedAt     *time.Time `json:"created_at"`
	Profile       struct {
		Name string `json:"name"`
	} `json:"profile"`
	Nodes    []labNode `json:"nodes"`
	Accounts []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"accounts"`
}

type profileInfo struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Path   string `json:"path"`
	// Profile is the document, kept raw so its fields render in file order.
	Profile json.RawMessage `json:"profile"`
}

type binaryInfo struct {
	Profile         string `json:"profile"`
	Version         string `json:"version"`
	Kind            string `json:"kind"`
	Source          string `json:"source"`
	Path            string `json:"path"`
	ReportedVersion string `json:"reported_version"`
	VersionCheck    string `json:"version_check"`
	Size            int64  `json:"size"`
	MtimeUnixNano   int64  `json:"mtime_unix_nano"`
}

type logLine struct {
	Node int    `json:"node"`
	Line string `json:"line"`
}

type execResult struct {
	Args     []string `json:"args"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}
