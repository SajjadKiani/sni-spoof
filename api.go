//go:build android

package snispoof

type VpnEngine interface {
	Start(tunFd int, config string) error
	Stop() error
	Status() string
}

type Engine struct {
	core *tunEngine
}

func NewVpnEngine() *Engine {
	return &Engine{core: newTunEngine()}
}

func (e *Engine) Start(tunFd int, config string) error {
	return e.core.Start(tunFd, config)
}

func (e *Engine) Stop() error {
	return e.core.Stop()
}

func (e *Engine) Status() string {
	return e.core.Status()
}

var defaultEngine = NewVpnEngine()

func Start(tunFd int, config string) error {
	return defaultEngine.Start(tunFd, config)
}

func Stop() error {
	return defaultEngine.Stop()
}

func Status() string {
	return defaultEngine.Status()
}
