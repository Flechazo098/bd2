package login

import "fmt"

// PortraitCostumeProvider supplies the saved UserPortraitChange selection.
type PortraitCostumeProvider interface{ PortraitCostume() uint64 }

func (s *LoginSeed) AttachPortrait(p PortraitCostumeProvider) error {
	if p == nil {
		return fmt.Errorf("account: nil portrait provider")
	}
	s.portrait = p
	return nil
}
