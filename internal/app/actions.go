package app

import "github.com/zach-monroe/germ/internal/config"

// doExtended handles actions implemented by later features (prompt
// navigation, tabs and splits). Unhandled actions return false so the key
// reaches the application.
func (a *App) doExtended(act config.Action) bool {
	return false
}
