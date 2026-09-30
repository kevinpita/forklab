package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The forms, one spec per forklab command that takes more than a
// selection. Each build is a pure function from field values to argv.

// profileDoc is the part of a profile document the forms read, from
// profile show and from the snapshot lab list keeps of a lab's profile.
type profileDoc struct {
	BinaryName   string `json:"binary_name"`
	ChainID      string `json:"chain_id"`
	Bech32Prefix string `json:"bech32_prefix"`
	KeyAlgo      string `json:"key_algo"`
	BondDenom    string `json:"bond_denom"`
	FeeDenom     string `json:"fee_denom"`
	GasPrices    string `json:"gas_prices"`
	BlockTime    string `json:"block_time"`
	UpgradeName  string `json:"upgrade_name"`
	Gov          struct {
		VotingPeriod          string `json:"voting_period"`
		ExpeditedVotingPeriod string `json:"expedited_voting_period"`
	} `json:"gov"`
	Binaries map[string]map[string]any `json:"binaries"`
}

// scalar is the flag of each single-value profile field, and its value in
// a document. The flag name is the field key.
var profileScalars = []struct {
	flag, label string
	get         func(profileDoc) string
}{
	{"binary-name", "Binary name", func(d profileDoc) string { return d.BinaryName }},
	{"chain-id", "Chain ID", func(d profileDoc) string { return d.ChainID }},
	{"bech32-prefix", "Bech32 prefix", func(d profileDoc) string { return d.Bech32Prefix }},
	{"key-algo", "Key algo", func(d profileDoc) string { return d.KeyAlgo }},
	{"bond-denom", "Bond denom", func(d profileDoc) string { return d.BondDenom }},
	{"fee-denom", "Fee denom", func(d profileDoc) string { return d.FeeDenom }},
	{"gas-prices", "Gas prices", func(d profileDoc) string { return d.GasPrices }},
	{"block-time", "Block time", func(d profileDoc) string { return d.BlockTime }},
	{"voting-period", "Voting period", func(d profileDoc) string { return d.Gov.VotingPeriod }},
	{"expedited-voting-period", "Expedited vote", func(d profileDoc) string { return d.Gov.ExpeditedVotingPeriod }},
	{"upgrade-name", "Upgrade name", func(d profileDoc) string { return d.UpgradeName }},
}

var binaryKinds = []string{"url", "path", "git", "src"}

func binaryKind(b map[string]any) string {
	for _, k := range binaryKinds {
		if _, ok := b[k]; ok {
			return k
		}
	}
	return "?"
}

// versionLess orders versions numerically part by part, so 11.10.0 comes
// after 11.2.0.
func versionLess(a, b string) bool {
	pa, pb := strings.FieldsFunc(a, notDigit), strings.FieldsFunc(b, notDigit)
	for i := range min(len(pa), len(pb)) {
		x, errx := strconv.Atoi(pa[i])
		y, erry := strconv.Atoi(pb[i])
		if errx == nil && erry == nil && x != y {
			return x < y
		}
	}
	if len(pa) != len(pb) {
		return len(pa) < len(pb)
	}
	return a < b
}

func notDigit(r rune) bool { return r < '0' || r > '9' }

// versionOptions are a profile's binary versions, newest first; current
// sorts last and says so, since upgrading to it is a no-op.
func versionOptions(d profileDoc, current string, kinds ...string) []option {
	var vs []string
	for v, b := range d.Binaries {
		if len(kinds) == 0 || slices.Contains(kinds, binaryKind(b)) {
			vs = append(vs, v)
		}
	}
	sort.Slice(vs, func(i, j int) bool {
		if (vs[i] == current) != (vs[j] == current) {
			return vs[j] == current
		}
		return versionLess(vs[j], vs[i])
	})
	opts := make([]option, 0, len(vs))
	for _, v := range vs {
		label := v + "  " + binaryKind(d.Binaries[v])
		if v == current {
			label += "  (running)"
		}
		opts = append(opts, option{value: v, label: label})
	}
	return opts
}

// Sources

var profilesSource = &source{
	cmd: func(values) Command { return Command{"profile", "list"} },
	parse: func(data json.RawMessage, _ values) ([]option, error) {
		var ps []profileInfo
		if err := json.Unmarshal(data, &ps); err != nil {
			return nil, err
		}
		opts := make([]option, 0, len(ps))
		for _, p := range ps {
			opts = append(opts, option{value: p.Name, label: p.Name + "  " + p.Origin})
		}
		return opts, nil
	},
}

// profileVersions lists the binary versions of the profile the field key
// names.
func profileVersions(key string, kinds ...string) *source {
	return &source{
		cmd: func(v values) Command {
			if v[key] == "" {
				return nil
			}
			return Command{"profile", "show", v[key]}
		},
		parse: func(data json.RawMessage, _ values) ([]option, error) {
			var p struct {
				Profile profileDoc `json:"profile"`
			}
			if err := json.Unmarshal(data, &p); err != nil {
				return nil, err
			}
			return versionOptions(p.Profile, "", kinds...), nil
		},
	}
}

// activeLab is the lab commands act on by default: the running one, else
// the only one.
func activeLab(labs []labInfo) (labInfo, bool) {
	for _, l := range labs {
		if l.Running {
			return l, true
		}
	}
	if len(labs) == 1 {
		return labs[0], true
	}
	return labInfo{}, false
}

// labVersions lists the versions of the profile snapshot the active lab
// was created with, which is what upgrade and restart resolve against.
var labVersions = &source{
	cmd: func(values) Command { return Command{"lab", "list"} },
	parse: func(data json.RawMessage, _ values) ([]option, error) {
		var labs []struct {
			labInfo
			Profile profileDoc `json:"profile"`
		}
		if err := json.Unmarshal(data, &labs); err != nil {
			return nil, err
		}
		infos := make([]labInfo, len(labs))
		for i, l := range labs {
			infos[i] = l.labInfo
		}
		l, ok := activeLab(infos)
		if !ok {
			return nil, errors.New("no running lab")
		}
		for _, x := range labs {
			if x.Name == l.Name {
				return versionOptions(x.Profile, l.Version), nil
			}
		}
		return nil, nil
	},
}

func accountsOf(data json.RawMessage) ([]account, error) {
	var as []account
	err := json.Unmarshal(data, &as)
	return as, err
}

// keysSource lists the lab's keys; other adds a last option that stands
// for any address.
func keysSource(other bool) *source {
	return &source{
		cmd: func(values) Command { return Command{"account", "list"} },
		parse: func(data json.RawMessage, _ values) ([]option, error) {
			as, err := accountsOf(data)
			if err != nil {
				return nil, err
			}
			opts := make([]option, 0, len(as)+1)
			for _, a := range as {
				opts = append(opts, option{value: a.Name, label: a.Name + "  " + shortAddr(a.Address)})
			}
			if other {
				opts = append(opts, option{value: otherAddress, label: "another address…"})
			}
			return opts, nil
		},
	}
}

const otherAddress = "address"

func shortAddr(a string) string {
	if len(a) <= 20 {
		return a
	}
	return a[:12] + "…" + a[len(a)-6:]
}

// denomsSource lists the denoms the sending key holds.
var denomsSource = &source{
	cmd: func(values) Command { return Command{"account", "list"} },
	parse: func(data json.RawMessage, v values) ([]option, error) {
		as, err := accountsOf(data)
		if err != nil {
			return nil, err
		}
		var opts []option
		for _, a := range as {
			if a.Name != v["from"] {
				continue
			}
			for _, c := range a.Balances {
				opts = append(opts, option{value: c.Denom, label: c.Denom + "  " + groupDigits(c.Amount) + " held"})
			}
		}
		return opts, nil
	},
}

var proposalsSource = &source{
	cmd: func(values) Command { return Command{"gov", "list"} },
	parse: func(data json.RawMessage, _ values) ([]option, error) {
		var ps []proposal
		if err := json.Unmarshal(data, &ps); err != nil {
			return nil, err
		}
		// Proposals still in their voting period come first, newest first.
		sort.SliceStable(ps, func(i, j int) bool {
			vi, vj := voting(ps[i]), voting(ps[j])
			if vi != vj {
				return vi
			}
			return ps[i].ID > ps[j].ID
		})
		opts := make([]option, 0, len(ps))
		for _, p := range ps {
			status := strings.ToLower(strings.TrimPrefix(p.Status, "PROPOSAL_STATUS_"))
			opts = append(opts, option{value: strconv.FormatUint(p.ID, 10), label: fmt.Sprintf("#%d %s  %s", p.ID, p.Title, status)})
		}
		return opts, nil
	},
}

func voting(p proposal) bool {
	return strings.TrimPrefix(p.Status, "PROPOSAL_STATUS_") == "VOTING_PERIOD"
}

var nodesSource = &source{
	cmd: func(values) Command { return Command{"node", "list"} },
	parse: func(data json.RawMessage, _ values) ([]option, error) {
		var ns []nodeInfo
		if err := json.Unmarshal(data, &ns); err != nil {
			return nil, err
		}
		opts := make([]option, 0, len(ns)+1)
		for _, n := range ns {
			opts = append(opts, option{value: itoa(n.Index), label: n.Name + "  " + n.State})
		}
		return append(opts, option{value: "all", label: "all nodes"}), nil
	},
}

// Checks

func has(key string, want ...string) func(values) bool {
	return func(v values) bool { return slices.Contains(want, v[key]) }
}

func set(key string) func(values) bool {
	return func(v values) bool { return v[key] != "" }
}

func checkName(s string, _ values) string {
	if strings.ContainsAny(s, " /\\\t") || strings.HasPrefix(s, "-") {
		return "letters, digits, dots, dashes, underscores"
	}
	return ""
}

func checkDigits(s string, _ values) string {
	if strings.Trim(s, "0123456789") != "" || strings.Trim(s, "0") == "" {
		return "a whole number above 0"
	}
	return ""
}

// Forms

// labCreateSpec is the lab create form; wizard asks one question at a time
// in the order a first-time user thinks about a lab.
func labCreateSpec(m *Model, wizard bool) *formSpec {
	taken := map[string]bool{}
	for _, l := range m.labs {
		taken[l.Name] = true
	}
	name := fieldSpec{
		key: "name", label: "Name", kind: fieldText, hint: "the lab's directory name",
		check: func(s string, v values) string {
			if taken[s] {
				return "a lab named " + s + " exists"
			}
			return checkName(s, v)
		},
	}
	fields := []fieldSpec{
		{key: "profile", label: "Profile", kind: fieldSelect, src: profilesSource, hint: "the chain to run"},
		{key: "version", label: "Version", kind: fieldSelect, src: profileVersions("profile"), hint: "binary every node starts with"},
		{key: "validators", label: "Validators", kind: fieldNumber, def: "2"},
		{
			key: "mode", label: "Genesis", kind: fieldSelect, def: "fresh",
			options: []option{{"fresh", "fresh genesis"}, {"fork", "fork a snapshot"}},
		},
		{key: "snapshot", label: "Snapshot", kind: fieldText, show: has("mode", "fork"), hint: "profile snapshot name, URL, or .tar.lz4|gz|zst file"},
		{key: "chain-id", label: "Chain ID", kind: fieldText, optional: true, placeholder: "the profile's", check: checkName},
	}
	if wizard {
		name.def = "devnet"
		fields = append(fields, name)
	} else {
		fields = append([]fieldSpec{name}, fields...)
	}
	title := "New lab"
	if wizard {
		title = "Create your first lab"
	}
	running := false
	for _, l := range m.labs {
		running = running || l.Running
	}
	return &formSpec{
		title: title, fields: fields, stepped: wizard,
		build: func(v values) Command {
			c := Command{"lab", "create", v["name"], "--profile", v["profile"], "--version", v["version"], "--validators", v["validators"]}
			if v["mode"] == "fork" {
				c = append(c, "--fork", v["snapshot"])
			}
			if v["chain-id"] != "" {
				c = append(c, "--chain-id", v["chain-id"])
			}
			return c
		},
		then: func(v values) *pendingRun {
			if running {
				return nil
			}
			return &pendingRun{cmd: Command{"lab", "up", v["name"]}, prompt: "Lab " + v["name"] + " is ready. Bring it up now?"}
		},
	}
}

// profileBinaryFields add or replace one binary version of a profile.
var profileBinaryFields = []fieldSpec{
	{key: "bin-version", label: "Add binary", kind: fieldText, optional: true, placeholder: "version, such as 0.53.8", hint: "adds or replaces this version"},
	{
		key: "bin-kind", label: "Source", kind: fieldSelect, def: "path", show: set("bin-version"),
		options: []option{{"path", "path  an existing binary"}, {"url", "url  download"}, {"git", "git  clone and build"}, {"src", "src  local checkout"}},
	},
	{key: "bin-location", label: "Location", kind: fieldText, show: set("bin-version"), hint: "file path, URL, or repository"},
}

func appendBinary(c Command, v values) Command {
	if v["bin-version"] == "" {
		return c
	}
	return append(c, "--binary", v["bin-version"]+"="+v["bin-kind"]+":"+v["bin-location"])
}

func profileCreateSpec(m *Model) *formSpec {
	taken := map[string]bool{}
	for _, p := range m.profiles {
		taken[p.Name+"/"+p.Origin] = true
	}
	fields := []fieldSpec{
		{key: "name", label: "Name", kind: fieldText, check: func(s string, v values) string {
			if taken[s+"/user"] {
				return "profile " + s + " exists; edit it with e"
			}
			return checkName(s, v)
		}},
		{key: "from", label: "Clone", kind: fieldSelect, src: profilesSource, def: "simd", optional: true, hint: "starting point; the fields below override it"},
	}
	for _, s := range profileScalars {
		fields = append(fields, fieldSpec{key: s.flag, label: s.label, kind: fieldText, optional: true, placeholder: "from the clone"})
	}
	fields = append(fields, profileBinaryFields...)
	return &formSpec{
		title: "New profile", fields: fields,
		build: func(v values) Command {
			c := Command{"profile", "create", v["name"]}
			if v["from"] != "" {
				c = append(c, "--from", v["from"])
			}
			for _, s := range profileScalars {
				if x := v[s.flag]; x != "" {
					c = append(c, "--"+s.flag, x)
				}
			}
			return appendBinary(c, v)
		},
	}
}

// profileEditSpec starts from the shown profile; only the fields that
// change become flags.
func profileEditSpec(m *Model) *formSpec {
	p := m.profile
	var doc profileDoc
	_ = json.Unmarshal(p.Profile, &doc)
	fields := slices.Clone(profileBinaryFields)
	var have []string
	for _, o := range versionOptions(doc, "") {
		have = append(have, o.value)
	}
	if len(have) > 0 {
		fields[0].hint = "has " + strings.Join(have, ", ") + "; a version here is added or replaced"
	}
	for _, s := range profileScalars {
		fields = append(fields, fieldSpec{key: s.flag, label: s.label, kind: fieldText, def: s.get(doc), optional: true})
	}
	name := p.Name
	return &formSpec{
		title: "Edit profile " + name, fields: fields,
		build: func(v values) Command {
			c := appendBinary(Command{"profile", "edit", name}, v)
			for _, s := range profileScalars {
				if x := v[s.flag]; x != s.get(doc) {
					c = append(c, "--"+s.flag, x)
				}
			}
			return c
		},
	}
}

func upgradeScheduleSpec(*Model) *formSpec {
	return &formSpec{
		title: "Schedule upgrade",
		fields: []fieldSpec{
			{key: "version", label: "Version", kind: fieldSelect, src: labVersions, hint: "from the lab's profile"},
			{key: "at", label: "When", kind: fieldSelect, def: "in", options: []option{{"in", "blocks from now"}, {"height", "at a height"}}},
			{key: "in", label: "Blocks", kind: fieldNumber, def: "40", show: has("at", "in"), hint: "must outlast the voting period"},
			{key: "height", label: "Height", kind: fieldNumber, show: has("at", "height")},
			{key: "name", label: "Plan name", kind: fieldText, optional: true, placeholder: "the profile's upgrade_name"},
			{key: "no-auto-swap", label: "Manual swap", kind: fieldToggle, hint: "leave halted nodes for R restart on a version"},
			{key: "expedited", label: "Expedited", kind: fieldToggle},
		},
		build: func(v values) Command {
			c := Command{"upgrade", "schedule", v["version"]}
			if v["at"] == "height" {
				c = append(c, "--height", v["height"])
			} else {
				c = append(c, "--in", v["in"])
			}
			if v["name"] != "" {
				c = append(c, "--name", v["name"])
			}
			for _, t := range []string{"no-auto-swap", "expedited"} {
				if v[t] != "" {
					c = append(c, "--"+t)
				}
			}
			return c
		},
	}
}

func sendSpec(m *Model) *formSpec {
	from, to := "", ""
	if a, ok := pick(m.accounts, m.cursor[panelAccounts]); ok {
		from = a.Name
	}
	for _, a := range m.accounts {
		if a.Name != from && to == "" {
			to = a.Name
		}
	}
	return &formSpec{
		title: "Send tokens",
		fields: []fieldSpec{
			{key: "from", label: "From", kind: fieldSelect, src: keysSource(false), def: from},
			{key: "to", label: "To", kind: fieldSelect, src: keysSource(true), def: to, check: func(s string, v values) string {
				if s == v["from"] {
					return "pick another key"
				}
				return ""
			}},
			{key: "address", label: "Address", kind: fieldText, show: has("to", otherAddress)},
			{key: "amount", label: "Amount", kind: fieldNumber, hint: "in base units"},
			{key: "denom", label: "Denom", kind: fieldSelect, src: denomsSource},
		},
		build: func(v values) Command {
			to := v["to"]
			if to == otherAddress {
				to = v["address"]
			}
			return Command{"account", "send", v["from"], to, v["amount"] + v["denom"]}
		},
	}
}

func govSubmitSpec(*Model) *formSpec {
	return &formSpec{
		title: "New proposal",
		fields: []fieldSpec{
			{
				key: "template", label: "Template", kind: fieldSelect, def: "text",
				options: []option{{"text", "text  signaling only"}, {"params", "params  change module params"}, {"upgrade", "upgrade  software upgrade plan"}},
			},
			{key: "title", label: "Title", kind: fieldText, optional: true, placeholder: "per template"},
			{key: "summary", label: "Summary", kind: fieldText, optional: true, placeholder: "the title"},
			{key: "module", label: "Module", kind: fieldText, show: has("template", "params"), hint: "such as staking"},
			{key: "set", label: "Changes", kind: fieldText, show: has("template", "params"), hint: "key=value, space separated; value is JSON or a string", check: func(s string, _ values) string {
				for _, kv := range strings.Fields(s) {
					if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
						return "want key=value"
					}
				}
				return ""
			}},
			{key: "plan", label: "Plan name", kind: fieldText, show: has("template", "upgrade")},
			{key: "height", label: "Height", kind: fieldNumber, show: has("template", "upgrade")},
			{key: "info", label: "Plan info", kind: fieldText, optional: true, show: has("template", "upgrade")},
			{key: "expedited", label: "Expedited", kind: fieldToggle},
			{key: "auto-vote", label: "Auto vote", kind: fieldToggle, def: "true", hint: "vote yes from every lab key that can pass it"},
		},
		build: func(v values) Command {
			c := Command{"gov", "submit", "--template", v["template"]}
			for _, k := range []string{"title", "summary", "module"} {
				if v[k] != "" {
					c = append(c, "--"+k, v[k])
				}
			}
			for _, kv := range strings.Fields(v["set"]) {
				c = append(c, "--set", kv)
			}
			if v["plan"] != "" {
				c = append(c, "--name", v["plan"])
			}
			for _, k := range []string{"height", "info"} {
				if v[k] != "" {
					c = append(c, "--"+k, v[k])
				}
			}
			for _, t := range []string{"expedited", "auto-vote"} {
				if v[t] != "" {
					c = append(c, "--"+t)
				}
			}
			return c
		},
	}
}

func govVoteSpec(m *Model) *formSpec {
	id := ""
	if p, ok := m.selectedProposal(); ok {
		id = strconv.FormatUint(p.ID, 10)
	}
	return &formSpec{
		title: "Vote",
		fields: []fieldSpec{
			{key: "proposal", label: "Proposal", kind: fieldSelect, src: proposalsSource, def: id},
			{
				key: "option", label: "Option", kind: fieldSelect, def: "yes",
				options: []option{{"yes", "yes"}, {"no", "no"}, {"abstain", "abstain"}, {"veto", "no with veto"}},
			},
			{key: "from", label: "From keys", kind: fieldText, def: "gov", hint: "comma separated; a fresh lab's validators are val0, val1, …"},
		},
		build: func(v values) Command {
			return Command{"gov", "vote", v["proposal"], v["option"], "--from", v["from"]}
		},
	}
}

func nodeRestartSpec(m *Model) *formSpec {
	node := ""
	if n, ok := m.selectedNode(); ok {
		node = itoa(n.Index)
	}
	return &formSpec{
		title: "Restart on a version",
		fields: []fieldSpec{
			{key: "node", label: "Node", kind: fieldSelect, src: nodesSource, def: node},
			{key: "version", label: "Version", kind: fieldSelect, src: labVersions, hint: "recorded in lab.yaml; later restarts keep it"},
		},
		build: func(v values) Command {
			return Command{"node", "restart", v["node"], "--binary", v["version"]}
		},
	}
}

// binarySpec fetches or builds a profile's binary version; build takes only
// the versions built from source.
func binarySpec(verb string) func(*Model) *formSpec {
	return func(m *Model) *formSpec {
		prof := ""
		if b, ok := pick(m.binaries, m.cursor[panelBinaries]); ok {
			prof = b.Profile
		}
		if l, ok := activeLab(m.labs); ok && prof == "" {
			prof = l.Profile.Name
		}
		src, title := profileVersions("profile"), "Fetch binary"
		if verb == "build" {
			src, title = profileVersions("profile", "git", "src"), "Build binary"
		}
		return &formSpec{
			title: title,
			fields: []fieldSpec{
				{key: "profile", label: "Profile", kind: fieldSelect, src: profilesSource, def: prof},
				{key: "version", label: "Version", kind: fieldSelect, src: src},
				{key: "no-verify", label: "Skip check", kind: fieldToggle, hint: "accept a binary whose version output differs"},
			},
			build: func(v values) Command {
				c := Command{"binary", verb, v["version"], "--profile", v["profile"]}
				if v["no-verify"] != "" {
					c = append(c, "--no-verify")
				}
				return c
			},
		}
	}
}

func execSpec(*Model) *formSpec {
	return &formSpec{
		title: "Run the chain binary", show: true,
		fields: []fieldSpec{
			{key: "args", label: "Arguments", kind: fieldText, hint: "such as q bank total; forklab adds --home --node --chain-id", check: func(s string, _ values) string {
				if _, err := splitArgs(s); err != nil {
					return err.Error()
				}
				return ""
			}},
		},
		build: func(v values) Command {
			args, _ := splitArgs(v["args"])
			return append(Command{"exec", "--"}, args...)
		},
	}
}
