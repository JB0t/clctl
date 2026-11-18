// Command clctl is a ClickUp CLI: tasks, timers, notes and comments from the terminal.
package main

import (
	"os"

	"github.com/JB0t/clctl/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
