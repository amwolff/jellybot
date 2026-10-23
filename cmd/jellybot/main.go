// jellybot identifies media files by their contents and organizes them into
// a Jellyfin library.
package main

import (
	"github.com/amwolff/bits/progutil"
	"github.com/zeebo/clingy"
)

func main() {
	progutil.Main("jellybot",
		progutil.Cmd(func(g progutil.Global) (string, string, clingy.Command) {
			return "tui", "preview the library layout in a two-pane TUI and apply it", &cmdTUI{g: g}
		}),
		progutil.Cmd(func(g progutil.Global) (string, string, clingy.Command) {
			return "plan", "print the library layout without changing anything", &cmdPlan{g: g}
		}),
	)
}
