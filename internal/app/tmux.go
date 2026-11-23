package app

// tmux control mode (Phase 7). These stubs keep the app compiling until
// the controller is implemented.

type tmuxController struct{}

type tmuxBackend struct{}

func (b *tmuxBackend) Write([]byte)    {}
func (b *tmuxBackend) Resize(int, int) {}
func (b *tmuxBackend) Close()          {}
func (b *tmuxBackend) Cwd() string     { return "" }

func (a *App) startTmux(session string) error { return nil }

func (c *tmuxController) process()                          {}
func (c *tmuxController) detach()                           {}
func (c *tmuxController) selectPane(*pane)                  {}
func (c *tmuxController) selectWindow(*tab)                 {}
func (c *tmuxController) newWindow(cwd string)              {}
func (c *tmuxController) splitPane(*pane, splitDir, string) {}
func (c *tmuxController) zoomPane(*pane)                    {}
func (c *tmuxController) killWindow(*tab)                   {}
func (c *tmuxController) clientSize(cellRect)               {}
