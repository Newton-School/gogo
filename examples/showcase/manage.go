// The same entrypoint runs web, migration, administration and worker commands.
package main

import (
	"example.com/gogo-showcase/config"
	"github.com/Newton-School/gogo"
)

func main() { gogo.Main(config.Project()) }
