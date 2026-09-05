package management

import (
	"flag"
	"fmt"
	"strings"
)

// parseFlags permits Django-style flags after positional arguments, while
// retaining flag.FlagSet's typed validation and explicit -- boundary.
func parseFlags(set *flag.FlagSet, args []string) error {
	var flags, positionals []string
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
		if strings.Contains(name, "=") {
			continue
		}
		definition := set.Lookup(name)
		if definition == nil {
			if name == "help" || name == "h" {
				continue
			}
			return fmt.Errorf("unknown flag: %s", name)
		}
		boolean, ok := definition.Value.(interface{ IsBoolFlag() bool })
		if ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 >= len(args) {
			return fmt.Errorf("flag requires a value: %s", name)
		}
		i++
		flags = append(flags, args[i])
	}
	return set.Parse(append(append(flags, "--"), positionals...))
}
