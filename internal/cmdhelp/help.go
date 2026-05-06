package cmdhelp

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const relatedAnnotationKey = "related"

// SetRelated records curated related commands for cmd. Each value should be a
// full command line (for example: `rex run attach`).
func SetRelated(cmd *cobra.Command, commands ...string) {
	if cmd == nil {
		return
	}
	items := uniqueNonEmpty(commands)
	if len(items) == 0 {
		return
	}
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[relatedAnnotationKey] = strings.Join(items, "\n")
}

// InstallRelatedHelp wraps Cobra's default help renderer and appends a curated
// or tree-derived "Related Commands" section.
func InstallRelatedHelp(root *cobra.Command) {
	if root == nil {
		return
	}
	baseHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		baseHelp(cmd, args)
		related := RelatedCommands(cmd)
		if len(related) == 0 {
			return
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Related Commands:")
		for _, line := range related {
			fmt.Fprintf(out, "  %s\n", line)
		}
	})
}

// RelatedCommands returns either explicitly curated related commands or a
// minimal fallback derived from the visible command tree.
func RelatedCommands(cmd *cobra.Command) []string {
	if cmd == nil {
		return nil
	}
	if items := relatedFromAnnotations(cmd); len(items) > 0 {
		return items
	}
	if items := visibleChildren(cmd); len(items) > 0 {
		return firstN(items, 3)
	}
	return firstN(relatedFromParent(cmd), 3)
}

func relatedFromAnnotations(cmd *cobra.Command) []string {
	if cmd.Annotations == nil {
		return nil
	}
	raw := strings.TrimSpace(cmd.Annotations[relatedAnnotationKey])
	if raw == "" {
		return nil
	}
	return uniqueNonEmpty(strings.Split(raw, "\n"))
}

func visibleChildren(cmd *cobra.Command) []string {
	var out []string
	for _, sub := range cmd.Commands() {
		if !isVisibleHelpTarget(sub) {
			continue
		}
		out = append(out, sub.CommandPath())
	}
	return out
}

func relatedFromParent(cmd *cobra.Command) []string {
	parent := cmd.Parent()
	if parent == nil {
		return nil
	}
	var out []string
	for _, sub := range parent.Commands() {
		if sub == cmd || !isVisibleHelpTarget(sub) {
			continue
		}
		out = append(out, sub.CommandPath())
	}
	if parent.CommandPath() != "" {
		out = append(out, parent.CommandPath())
	}
	return uniqueNonEmpty(out)
}

func isVisibleHelpTarget(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Hidden {
		return false
	}
	return cmd.Name() != "help"
}

func firstN(items []string, n int) []string {
	items = uniqueNonEmpty(items)
	if len(items) <= n {
		out := make([]string, len(items))
		copy(out, items)
		return out
	}
	out := make([]string, n)
	copy(out, items[:n])
	return out
}

func uniqueNonEmpty(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
