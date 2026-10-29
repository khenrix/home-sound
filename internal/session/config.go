package session

import (
	"encoding/json"
	"errors"
	"github.com/khenrix/home-sound/internal/config"
)

func cloneConfig(c config.Config) config.Config {
	b, _ := json.Marshal(c)
	var copy config.Config
	_ = json.Unmarshal(b, &copy)
	return copy
}

func (e *Engine) Reconfigure(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	e.op.Lock()
	defer e.op.Unlock()
	if e.Status().Active {
		return errors.New("configuration requires idle engine")
	}
	e.cfg = cloneConfig(c)
	return nil
}
