package cli

import (
	"slices"
	"strconv"
	"strings"

	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/spf13/cobra"
)

func completionValues(values []string, prefix string, directive cobra.ShellCompDirective) ([]string, cobra.ShellCompDirective) {
	values = slices.DeleteFunc(values, func(v string) bool { return !strings.HasPrefix(v, prefix) })
	slices.Sort(values)
	return values, directive
}

func completeLabs(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	var names []string
	if len(args) == 0 {
		if store, err := labs(); err == nil {
			if rows, err := store.List(); err == nil {
				for _, row := range rows {
					names = append(names, row.Name)
				}
			}
		}
	}
	return completionValues(names, prefix, cobra.ShellCompDirectiveNoFileComp)
}

func completeLabRef(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	names, _ := completeLabs(cmd, nil, prefix)
	if len(names) == 0 {
		return nil, cobra.ShellCompDirectiveFilterDirs
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeProfiles(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	var names []string
	if len(args) == 0 {
		if store, err := profile.DefaultStore(); err == nil {
			if rows, err := store.List(); err == nil {
				for _, row := range rows {
					names = append(names, row.Name)
				}
			}
		}
	}
	return completionValues(names, prefix, cobra.ShellCompDirectiveNoFileComp)
}

func completeProfileFile(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	names, _ := completeProfiles(cmd, args, prefix)
	if len(args) == 0 && len(names) == 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completionLab(cmd *cobra.Command) (lab.Config, bool) {
	ref, _ := cmd.Flags().GetString("lab")
	if strings.Contains(ref, "/") || ref == "." || ref == ".." {
		cfg, err := lab.Load(ref)
		return cfg, err == nil
	}
	store, err := labs()
	if err != nil {
		return lab.Config{}, false
	}
	if ref != "" {
		cfg, _, err := store.Get(ref)
		return cfg, err == nil
	}
	rows, err := store.List()
	if err != nil || len(rows) != 1 || rows[0].Config == nil {
		return lab.Config{}, false
	}
	return *rows[0].Config, true
}

func completeNodes(all bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		var values []string
		if len(args) == 0 {
			if cfg, ok := completionLab(cmd); ok {
				for _, node := range cfg.Nodes {
					values = append(values, strconv.Itoa(node.Index))
				}
				if all {
					values = append(values, "all")
				}
			}
		}
		return completionValues(values, prefix, cobra.ShellCompDirectiveNoFileComp)
	}
}

func completeProfileVersions(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	var values []string
	if len(args) == 0 {
		name, _ := cmd.Flags().GetString("profile")
		if store, err := profile.DefaultStore(); err == nil && name != "" {
			if entry, err := store.Get(name); err == nil {
				for version := range entry.Doc.Binaries {
					values = append(values, version)
				}
			}
		}
	}
	return completionValues(values, prefix, cobra.ShellCompDirectiveNoFileComp)
}

func completeLabVersions(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	var values []string
	if len(args) == 0 {
		if cfg, ok := completionLab(cmd); ok {
			for version := range cfg.Profile.Binaries {
				values = append(values, version)
			}
		}
	}
	return completionValues(values, prefix, cobra.ShellCompDirectiveNoFileComp)
}

func registerCompletion(cmd *cobra.Command, flag string, complete cobra.CompletionFunc) {
	if err := cmd.RegisterFlagCompletionFunc(flag, func(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return complete(cmd, nil, prefix)
	}); err != nil {
		panic(err)
	}
}

func registerSharedCompletions(cmd *cobra.Command) {
	if cmd.LocalNonPersistentFlags().Lookup("profile") != nil {
		registerCompletion(cmd, "profile", completeProfiles)
	}
	if cmd.PersistentFlags().Lookup("lab") != nil {
		registerCompletion(cmd, "lab", completeLabRef)
	}
	for _, child := range cmd.Commands() {
		registerSharedCompletions(child)
	}
}

func completeLabBinary(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	values, _ := completeLabVersions(cmd, args, prefix)
	if len(values) == 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return values, cobra.ShellCompDirectiveNoFileComp
}
