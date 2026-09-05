package cli

import (
	"fmt"

	appconfig "github.com/damirm/lazytrade/internal/config"
)

func commandNotImplemented(name, configPath string, validationCommand appconfig.Command) error {
	if _, err := appconfig.LoadFileFor(configPath, validationCommand); err != nil {
		return err
	}
	return fmt.Errorf("%s: command runtime is not implemented yet", name)
}

func staticCommandNotImplemented(name, configPath string) error {
	if _, err := appconfig.LoadFile(configPath); err != nil {
		return err
	}
	return fmt.Errorf("%s: command runtime is not implemented yet", name)
}
