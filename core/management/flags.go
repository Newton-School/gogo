package management

import (
	"flag"
	"fmt"
	"strings"
)

// parseFlags permits Django-style flags after positional arguments, while
// retaining flag.FlagSet's typed validation and explicit -- boundary.
func parseFlags(set *flag.FlagSet, args []string) error {
	_, err := parseFlagsWithNames(set, args)
	return err
}

// Names come from the consumed argv tokens, not FlagSet.Visit: Configure or a
// flag value callback may also call Set without the user supplying that flag.
func parseFlagsWithNames(set *flag.FlagSet, args []string) (map[string]bool, error) {
	var flags, positionals []string
	provided := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if before, _, ok := strings.Cut(name, "="); ok {
			provided[before] = true
			continue
		}
		provided[name] = true
		definition := set.Lookup(name)
		if definition == nil {
			if name == "help" || name == "h" {
				continue
			}
			return nil, fmt.Errorf("unknown flag: %s", name)
		}
		boolean, ok := definition.Value.(interface{ IsBoolFlag() bool })
		if ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag requires a value: %s", name)
		}
		i++
		flags = append(flags, args[i])
	}
	return provided, set.Parse(append(append(flags, "--"), positionals...))
}
