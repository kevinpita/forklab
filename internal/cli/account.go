package cli

import (
	"fmt"
	"io"
	"regexp"
	"text/tabwriter"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/spf13/cobra"
)

func newAccountCmd(a *app) *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "account",
		Short: "List lab keys with balances, and send tokens between them",
	}
	labRefFlag(cmd, &ref)
	cmd.AddCommand(newAccountListCmd(a, &ref), newAccountSendCmd(a, &ref))
	return cmd
}

type accountRow struct {
	Name     string      `json:"name"`
	Address  string      `json:"address"`
	Balances chain.Coins `json:"balances"`
}

type accountList []accountRow

func (l accountList) WriteHuman(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tADDRESS\tBALANCES")
	for _, r := range l {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, r.Address, r.Balances)
	}
	return tw.Flush()
}

func newAccountListCmd(a *app, ref *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the lab's keys, addresses, and balances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			c, _, err := e.liveCLI(cmd.Context())
			if err != nil {
				return err
			}
			out := accountList{}
			for _, acc := range e.cfg.Accounts {
				bal, err := c.Balances(cmd.Context(), acc.Address)
				if err != nil {
					return fmt.Errorf("balances of %s: %w", acc.Name, err)
				}
				out = append(out, accountRow{Name: acc.Name, Address: acc.Address, Balances: bal})
			}
			return a.print(cmd, out)
		},
	}
}

type txView struct {
	chain.TxResult
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Amount string `json:"amount,omitempty"`
}

func (v txView) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "sent %s from %s to %s: tx %s in block %d, code %d\n", v.Amount, v.From, v.To, v.Hash, v.Height, v.Code)
	return err
}

var digits = regexp.MustCompile(`^[0-9]+$`)

func newAccountSendCmd(a *app, ref *string) *cobra.Command {
	return &cobra.Command{
		Use:   "send <from> <to> <amount>",
		Short: "Send tokens from a lab key to a lab key or an address and wait for the block",
		Long: "Send tokens from a lab key to a lab key or an address and wait for the block.\n" +
			"A bare number amount is in the profile's fee denom.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			from, to, amount := args[0], args[1], args[2]
			if _, ok := e.account(from); !ok {
				return output.Usagef("from %q is not a lab key (have %s)", from, e.accountNames())
			}
			toAddr := to
			if acc, ok := e.account(to); ok {
				toAddr = acc.Address
			}
			if digits.MatchString(amount) {
				amount += e.profile.FeeDenom
			}
			c, rpc, err := e.liveCLI(cmd.Context())
			if err != nil {
				return err
			}
			r, err := broadcast(cmd.Context(), c, rpc, from, "bank", "send", from, toAddr, amount)
			if err != nil {
				return err
			}
			return a.print(cmd, txView{TxResult: r, From: from, To: to, Amount: amount})
		},
	}
}
