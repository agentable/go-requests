package requests

import "fmt"

// Profile contributes coherent client identity options during construction.
type Profile interface {
	Name() string
	Options() []Option
}

func applyProfileOptions(c *Client, profile Profile) error {
	if isNilInterface(profile) {
		return invalidOptionValue("Profile")
	}
	for _, opt := range profile.Options() {
		if opt == nil {
			continue
		}
		if err := opt(c); err != nil {
			return fmt.Errorf("apply profile %q: %w", profile.Name(), err)
		}
	}
	return nil
}
